// Package build compiles applications without changing the caller's working directory.
package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/gitmeta"
)

const BuildcardPath = "github.com/LittleDrongo/buildcard"

type Options struct {
	Root, Platform, Package, Tag string
	DryRun                       bool
	Timeout                      time.Duration
}

type Result struct {
	Artifact     string
	Version      string
	ImageVersion string
}

func command(ctx context.Context, root string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = root, env
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func environment(platform string) []string {
	parts := strings.Split(platform, "-")
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOWORK":
		default:
			env = append(env, entry)
		}
	}
	return append(env, "GOOS="+parts[0], "GOARCH="+parts[1], "CGO_ENABLED=0", "GOWORK=off")
}

var platformPattern = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)
var modulePattern = regexp.MustCompile(`(?m)^\s*module\s+("[^"]+"|[^\s/]+(?:/[^\s]+)*)`)
var majorSuffix = regexp.MustCompile(`^v[2-9][0-9]*$|^v1[0-9]+$`)

func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	match := modulePattern.FindSubmatch(data)
	if len(match) != 2 {
		return "", fmt.Errorf("module directive missing from go.mod")
	}
	value := string(match[1])
	if strings.HasPrefix(value, "\"") {
		return strconv.Unquote(value)
	}
	return value, nil
}

func filePart(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, value)
}

// Run builds one main package. Dry-run only reads local files and Git metadata.
func Run(ctx context.Context, o Options, out io.Writer) (result Result, err error) {
	if o.Timeout <= 0 {
		return result, fmt.Errorf("build timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if o.Platform == "" {
		o.Platform = runtime.GOOS + "-" + runtime.GOARCH
	}
	if !platformPattern.MatchString(o.Platform) {
		return result, fmt.Errorf("invalid platform %q; expected OS-ARCH", o.Platform)
	}
	if o.Package == "" {
		o.Package = "."
	}
	if o.Package != "." && (!strings.HasPrefix(o.Package, "./") || strings.Contains(o.Package, "...") || strings.Contains(o.Package, "\\") || path.Clean(o.Package) == ".." || strings.HasPrefix(path.Clean(o.Package), "../")) {
		return result, fmt.Errorf("package must be . or a relative package such as ./cmd/server")
	}
	o.Root, err = filepath.Abs(o.Root)
	if err != nil {
		return result, err
	}
	source := o.Root
	metadata, err := gitmeta.Read(ctx, source)
	if err != nil {
		return result, err
	}
	module, err := modulePath(source)
	if err != nil {
		return result, err
	}
	if o.Tag != "" {
		selected, e := resolveTag(ctx, o.Root, o.Tag)
		if e != nil {
			return result, e
		}
		metadata = selected.Info
		module = selected.Module
		if !o.DryRun {
			var cleanup func() error
			source, cleanup, err = checkout(ctx, o.Root, selected)
			if err != nil {
				return result, err
			}
			defer func() {
				if e := cleanup(); e != nil {
					err = errors.Join(err, e)
				}
			}()
		}
	}
	name := path.Base(module)
	if majorSuffix.MatchString(name) {
		name = path.Base(path.Dir(module))
	}
	name = filePart(name) + "-" + filePart(metadata.Version) + "-" + o.Platform
	if strings.HasPrefix(o.Platform, "windows-") {
		name += ".exe"
	}
	result = Result{Artifact: filepath.Join(o.Root, ".bin", name), Version: metadata.Version}
	result.ImageVersion = metadata.Version
	if metadata.Tag == "" {
		result.ImageVersion = "dev"
		if metadata.Dirty {
			result.ImageVersion += "-dirty"
		}
	}
	if o.DryRun {
		_, err = fmt.Fprintf(out, "Корень: %s\nПакет: %s\nПлатформа: %s (CGO_ENABLED=0, GOWORK=off)\nВерсия: %s\nАртефакт: %s\n", o.Root, o.Package, o.Platform, result.Version, result.Artifact)
		if err != nil {
			return result, err
		}
		if o.Tag != "" {
			if _, err = fmt.Fprintf(out, "Тег: %s; отдельный checkout коммита %s\n", o.Tag, metadata.Commit); err != nil {
				return result, err
			}
		}
		_, err = fmt.Fprintln(out, "Dry-run: сборка и создание checkout не выполняются; платформа и зависимости будут проверены при сборке.")
		return result, err
	}
	env := environment(o.Platform)
	data, err := command(ctx, source, env, "go", "tool", "dist", "list").Output()
	if err != nil {
		return result, fmt.Errorf("list Go platforms: %w", err)
	}
	if !strings.Contains("\n"+string(data), "\n"+strings.Replace(o.Platform, "-", "/", 1)+"\n") {
		return result, fmt.Errorf("unsupported Go platform %q", o.Platform)
	}
	packages := command(ctx, source, env, "go", "list", "-mod=readonly", "-deps", "-json", o.Package)
	packages.Stderr = out
	data, err = packages.Output()
	if err != nil {
		return result, fmt.Errorf("inspect build dependencies: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var targets []string
	mainPackage := false
	for {
		var p struct {
			ImportPath, Name string
			DepOnly          bool
		}
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return result, err
		}
		if !p.DepOnly {
			if mainPackage || p.Name != "main" {
				return result, fmt.Errorf("build requires exactly one main package")
			}
			mainPackage = true
		}
		if p.ImportPath == BuildcardPath || p.ImportPath == module+"/internal/buildinfo" {
			targets = append(targets, p.ImportPath)
		}
	}
	if !mainPackage {
		return result, fmt.Errorf("main package not found")
	}
	if len(targets) == 0 {
		if _, err = fmt.Fprintln(out, "Предупреждение: buildcard и internal/buildinfo не используются; версия, дата сборки и остальные метаданные deployctl не будут внедрены."); err != nil {
			return result, err
		}
	}
	flags, err := linkerFlags(targets, metadata, module, time.Now().UTC())
	if err != nil {
		return result, err
	}
	if err = os.MkdirAll(filepath.Dir(result.Artifact), 0755); err != nil {
		return result, err
	}
	// Compile into a separate file, preserving any previous artifact on failure.
	temp, err := os.CreateTemp(filepath.Dir(result.Artifact), ".build-*")
	if err != nil {
		return result, err
	}
	tempPath := temp.Name()
	if err = temp.Close(); err != nil {
		os.Remove(tempPath)
		return result, err
	}
	defer os.Remove(tempPath)
	args := []string{"build", "-mod=readonly", "-trimpath", "-ldflags", flags, "-o", tempPath, o.Package}
	cmd := command(ctx, source, env, "go", args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("build %s: %w", o.Platform, err)
	}
	if err = os.Rename(tempPath, result.Artifact); err != nil {
		return result, fmt.Errorf("publish artifact: %w", err)
	}
	_, err = fmt.Fprintf(out, "Готово: %s\n", result.Artifact)
	return result, err
}

func linkerFlags(targets []string, metadata gitmeta.Info, repository string, now time.Time) (string, error) {
	values := [][2]string{
		{"buildVersion", metadata.Version}, {"buildCommitHash", metadata.Commit}, {"buildCommitShort", metadata.Short},
		{"buildCommitDate", metadata.CommitDate}, {"buildDirty", strconv.FormatBool(metadata.Dirty)},
		{"buildTime", now.Format(time.RFC3339)}, {"buildRepository", repository},
	}
	var flags []string
	for _, target := range targets {
		for _, pair := range values {
			value := target + "." + pair[0] + "=" + pair[1]
			quote := "'"
			if strings.Contains(value, quote) {
				quote = "\""
			}
			if strings.Contains(value, quote) || strings.ContainsAny(value, "\x00\r\n") {
				return "", fmt.Errorf("metadata cannot be represented in linker flags: %s", pair[0])
			}
			flags = append(flags, "-X", quote+value+quote)
		}
	}
	return strings.Join(flags, " "), nil
}
