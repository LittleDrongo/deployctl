// Package cli implements the command interface. It owns no global process state.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/build"
	"github.com/LittleDrongo/deployctl/internal/config"
	"github.com/LittleDrongo/deployctl/internal/gitmeta"
	"github.com/LittleDrongo/deployctl/internal/project"
	"github.com/LittleDrongo/deployctl/internal/setup"
)

const helpFormat = `deployctl — сборка и развёртывание Go-приложений

Доступные команды:
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s
  %-28s  %s

Корень приложения — ближайший go.mod вверх от текущего каталога.
--root задаёт каталог модуля явно, относительно текущего каталога.
Вложенные модули выбираются независимо; go.work не задаёт корень.
Параметры команд: deployctl build --help, deployctl up --help.
`

func writeHelp(out io.Writer) error {
	_, err := fmt.Fprintf(out, helpFormat,
		"version", "Версия утилиты",
		"info [--root DIR] [--json]", "Версия приложения из Git",
		"build [options]", "Собрать приложение",
		"init [options]", "Подготовить существующий модуль",
		"new <name> [options]", "Создать приложение",
		"config check [options]", "Проверить deploy.yaml",
		"up <target> [options]", "Собрать и развернуть",
		"stop <target> [options]", "Остановить контейнер",
		"restart <target> [options]", "Перезапустить существующий",
		"remove <target> [options]", "Остановить и удалить контейнер",
		"help", "Справка",
	)
	return err
}

func Run(ctx context.Context, args []string, out io.Writer, version string) error {
	if len(args) == 0 {
		return writeHelp(out)
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return fmt.Errorf("help does not accept arguments")
		}
		return writeHelp(out)
	case "version", "--version":
		if len(args) != 1 {
			return fmt.Errorf("version does not accept arguments")
		}
		_, err := fmt.Fprintln(out, "deployctl", version)
		return err
	case "info":
		return info(ctx, args[1:], out)
	case "build":
		return runBuild(ctx, args[1:], out)
	case "init":
		return runInit(ctx, args[1:], out)
	case "new":
		return runNew(ctx, args[1:], out)
	case "config":
		if len(args) < 2 || args[1] != "check" {
			return fmt.Errorf("usage: deployctl config check [--root DIR] [--config FILE]")
		}
		return checkConfig(args[2:], out)
	case "up", "stop", "restart", "remove":
		return runRemote(ctx, args[0], args[1:], out)
	default:
		return fmt.Errorf("unknown command %q; use deployctl help", args[0])
	}
}

func runBuild(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(out)
	var o build.Options
	filename := flags.String("config", "", "config path relative to module root (default deploy.yaml)")
	flags.StringVar(&o.Root, "root", "", "application module directory")
	flags.StringVar(&o.Platform, "platform", runtime.GOOS+"-"+runtime.GOARCH, "target OS-ARCH, e.g. linux-amd64")
	flags.StringVar(&o.Package, "package", ".", "main package relative to module root")
	flags.StringVar(&o.Tag, "tag", "", "local Git tag to build in a temporary checkout")
	flags.BoolVar(&o.DryRun, "dry-run", false, "print plan without building or changing Git/files")
	flags.DurationVar(&o.Timeout, "timeout", 20*time.Minute, "maximum build duration")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("build does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	o.Root, err = project.Root(cwd, o.Root)
	if err != nil {
		return err
	}
	c, err := config.Load(config.Path(o.Root, *filename))
	if err != nil && (*filename != "" || !os.IsNotExist(err)) {
		return err
	}
	if err == nil {
		set := map[string]bool{}
		flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if !set["package"] {
			o.Package = c.Build.Package
		}
		if !set["platform"] && c.Build.Platform != "" {
			o.Platform = c.Build.Platform
		}
	}
	_, err = build.Run(ctx, o, out)
	return err
}

func runInit(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("init", flag.ContinueOnError)
	f.SetOutput(out)
	var o setup.Options
	f.StringVar(&o.Root, "root", "", "application module directory")
	f.StringVar(&o.Config, "config", "", "config path relative to module root")
	f.BoolVar(&o.DryRun, "dry-run", false, "preview changes without writing files or downloading dependencies")
	f.BoolVar(&o.Scaffold, "scaffold", false, "create main.go if absent")
	f.BoolVar(&o.Makefile, "makefile", false, "create optional Makefile if absent")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("init does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	o.Root, err = project.Root(cwd, o.Root)
	if err != nil {
		return err
	}
	return setup.Run(ctx, o, out)
}

func runNew(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("new", flag.ContinueOnError)
	f.SetOutput(out)
	module := f.String("module", "", "Go module path (default directory name)")
	dry := f.Bool("dry-run", false, "preview without creating files")
	// Accept both new NAME --module PATH and new --module PATH NAME.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 1 {
		return fmt.Errorf("usage: deployctl new NAME [--module MODULE]")
	}
	dir, err := filepath.Abs(f.Arg(0))
	if err != nil {
		return err
	}
	return setup.New(ctx, dir, *module, *dry, out)
}

func checkConfig(args []string, out io.Writer) error {
	f := flag.NewFlagSet("config check", flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("root", "", "application module directory")
	filename := f.String("config", "", "config path relative to module root")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("config check does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	resolved, err := project.Root(cwd, *root)
	if err != nil {
		return err
	}
	file := config.Path(resolved, *filename)
	c, err := config.Load(file)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Конфиг корректен: %s (схема %d, targets: %d). SSH и Docker не проверялись.\n", file, c.Version, len(c.Targets))
	return err
}

func info(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("info", flag.ContinueOnError)
	flags.SetOutput(out)
	rootFlag := flags.String("root", "", "explicit application module directory")
	asJSON := flags.Bool("json", false, "print structured application metadata")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("info does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := project.Root(cwd, *rootFlag)
	if err != nil {
		return err
	}
	metadata, err := gitmeta.Read(ctx, root)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(struct {
			Root string `json:"root"`
			gitmeta.Info
		}{root, metadata})
	}
	_, err = fmt.Fprintf(out, "BUILD_VERSION=%s\n", metadata.Version)
	return err
}
