// Package setup prepares applications without overwriting existing source files.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/config"
)

const Buildcard = "github.com/LittleDrongo/buildcard"
const BuildcardVersion = "v1.0.1"

type Options struct {
	Root, Config               string
	DryRun, Scaffold, Makefile bool
}

type moduleInfo struct {
	Module  struct{ Path string }
	Require []struct{ Path, Version string }
}

func goCommand(ctx context.Context, root string, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "GOWORK") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GOWORK=off")
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}

	return nil
}

func readRegular(filename string) ([]byte, error) {
	st, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file (not a symlink)", filename)
	}
	return os.ReadFile(filename)
}

type edit struct {
	path       string
	data       []byte
	appendOnly bool
}

// Run plans every file edit before fetching the fixed dependency or writing files.
func Run(ctx context.Context, o Options, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := readRegular(filepath.Join(o.Root, "go.mod")); err != nil {
		return err
	}
	var buf strings.Builder
	if err := goCommand(ctx, o.Root, &buf, "mod", "edit", "-json"); err != nil {
		return fmt.Errorf("read go.mod: %w: %s", err, buf.String())
	}
	var module moduleInfo
	if err := json.Unmarshal([]byte(buf.String()), &module); err != nil {
		return err
	}
	if module.Module.Path == "" {
		return fmt.Errorf("module directive missing from go.mod")
	}
	version := ""
	for _, r := range module.Require {
		if r.Path == Buildcard {
			version = r.Version
		}
	}
	var edits []edit
	planFile := func(filename string, data []byte) error {
		_, err := readRegular(filename)
		if err == nil {
			_, err = fmt.Fprintf(out, "Сохранён существующий файл: %s\n", filename)
			return err
		}
		if !os.IsNotExist(err) {
			return err
		}
		edits = append(edits, edit{path: filename, data: data})
		return nil
	}
	filename := config.Path(o.Root, o.Config)
	for _, reserved := range []string{"go.mod", "go.sum", ".gitignore", "main.go", "Makefile"} {
		if strings.EqualFold(filepath.Clean(filename), filepath.Join(o.Root, reserved)) {
			return fmt.Errorf("config path conflicts with project file %s", reserved)
		}
	}
	if _, err := os.Lstat(filename); err == nil {
		if _, err := readRegular(filename); err != nil {
			return err
		}
		if _, err := config.Load(filename); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := planFile(filename, []byte(template(module.Module.Path))); err != nil {
		return err
	}
	ignorePath := filepath.Join(o.Root, ".gitignore")
	ignore, err := readRegular(ignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	ignored := false
	for _, line := range strings.Split(string(ignore), "\n") {
		switch strings.TrimSpace(line) {
		case "/.bin/", ".bin/", "/.bin", ".bin":
			ignored = true
		}
	}
	if !ignored {
		newline := "\n"
		if strings.Contains(string(ignore), "\r\n") {
			newline = "\r\n"
		}
		addition := ""
		if len(ignore) > 0 && ignore[len(ignore)-1] != '\n' {
			addition = newline
		}
		addition += "/.bin/" + newline
		edits = append(edits, edit{path: ignorePath, data: []byte(addition), appendOnly: !os.IsNotExist(err)})
	}
	if o.Scaffold {
		if err := planFile(filepath.Join(o.Root, "main.go"), []byte(scaffold)); err != nil {
			return err
		}
	}
	if o.Makefile {
		if err := planFile(filepath.Join(o.Root, "Makefile"), []byte(makefile)); err != nil {
			return err
		}
	}
	for _, e := range edits {
		verb := "Создать"
		if e.appendOnly {
			verb = "Добавить строки в"
		}
		if _, err := fmt.Fprintf(out, "%s %s:\n%s\n", verb, e.path, e.data); err != nil {
			return err
		}
	}
	if version == "" {
		if _, err := fmt.Fprintf(out, "Подключить: go get %s@%s\n", Buildcard, BuildcardVersion); err != nil {
			return err
		}
		if !o.DryRun {
			if err := goCommand(ctx, o.Root, out, "get", Buildcard+"@"+BuildcardVersion); err != nil {
				return err
			}
		}
	} else {
		if _, err := fmt.Fprintf(out, "Сохранена зависимость %s@%s (версия, проверенная с CLI: %s).\n", Buildcard, version, BuildcardVersion); err != nil {
			return err
		}
	}
	if o.DryRun {
		_, err := fmt.Fprintln(out, "Dry-run: файлы и зависимости не изменены.")
		return err
	}
	for _, e := range edits {
		if err := os.MkdirAll(filepath.Dir(e.path), 0755); err != nil {
			return err
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if e.appendOnly {
			flags = os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(e.path, flags, 0644)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(e.data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	_, err = fmt.Fprintln(out, "Инициализация завершена. Для метаданных используйте в приложении:\n  info := buildcard.Snapshot()\nИмпорт: github.com/LittleDrongo/buildcard. Snapshot() ничего не печатает; используйте info в логике приложения.\nЗаполните targets и проверьте план: deployctl up <target> --dry-run.\nДля автодополнения Bash: deployctl completion install bash.")
	return err
}

var major = regexp.MustCompile(`^v([2-9]|[1-9][0-9]+)$`)

func template(module string) string {
	name := path.Base(module)
	if major.MatchString(name) {
		name = path.Base(path.Dir(module))
	}
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.ToLower(name))
	name = strings.Trim(name, "-_")
	if name == "" {
		name = "app"
	}
	return fmt.Sprintf(`# Install: go install github.com/LittleDrongo/deployctl@latest
# deployctl config schema. Local paths are relative to the Go module root.
version: 1
build:
  package: .
  # Comment out or remove platforms you do not need.
  platforms:
    - windows-amd64
    - windows-arm64
    - linux-amd64
    - linux-arm64
    - android-arm64
    - windows-386
    - linux-386
    - darwin-amd64
    - darwin-arm64

defaults:
  build_target: linux-amd64
  remote_dir: /opt/%[1]s
  binary_name: %[1]s
  docker_container: %[1]s
  docker_image: %[1]s:latest
  docker_base_image: alpine:3.20
  # Use the base image user (root in alpine). Host files may require sudo.
  # Set ssh to use the SSH user's UID/GID and application HOME/XDG directories.
  container_user: image
  docker_run_args: []
  docker_mounts:
    - host_path: /opt/%[1]s
      container_path: /app
      create_host_path: dir
  start_args: {}

# Add an SSH Host alias from %%USERPROFILE%%\.ssh\config (Windows) or ~/.ssh/config.
# Omit user and port to inherit OpenSSH settings.
targets: {}
# targets:
#   prod:
#     host: production
`, name)
}

const scaffold = `package main

import "github.com/LittleDrongo/buildcard"

func main() {
	info := buildcard.Snapshot()
	_ = info // Use metadata in your application; initialization produces no output.
}
`

const makefile = `.PHONY: build info
build:
	deployctl build
info:
	deployctl info
`
