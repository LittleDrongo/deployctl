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
	"runtime"
	"time"

	"github.com/LittleDrongo/deployctl/internal/build"
	"github.com/LittleDrongo/deployctl/internal/clean"
	"github.com/LittleDrongo/deployctl/internal/completion"
	"github.com/LittleDrongo/deployctl/internal/config"
	"github.com/LittleDrongo/deployctl/internal/gitmeta"
	"github.com/LittleDrongo/deployctl/internal/project"
	"github.com/LittleDrongo/deployctl/internal/release"
	"github.com/LittleDrongo/deployctl/internal/setup"
)

func writeHelp(out io.Writer, version string) error {
	if _, err := fmt.Fprintf(out, "deployctl — сборка и развёртывание Go-приложений\nВерсия утилиты: %s\n\nДоступные команды:\n", version); err != nil {
		return err
	}
	commands := [][2]string{
		{"version", "Версия утилиты"},
		{"info [--root DIR] [--json]", "Версия приложения из Git"},
		{"build [options]", "Собрать приложение"},
		{"init [options]", "Подготовить существующий модуль"},
		{"config check [options]", "Проверить config_deploy.yaml"},
		{"doctor <target> [options]", "Проверить окружение сервера"},
		{"status [target] [options]", "Показать состояние одного или всех контейнеров"},
		{"logs <target> [options]", "Показать логи контейнера"},
		{"up <target> [options]", "Собрать и развернуть"},
		{"stop <target> [options]", "Остановить контейнер"},
		{"restart <target> [options]", "Перезапустить существующий"},
		{"remove <target> [options]", "Остановить и удалить контейнер"},
		{"release [options]", "Создать тег; отправить при наличии origin"},
		{"clean [options]", "Удалить локальные артефакты"},
		{"completion bash|install bash", "Автодополнение команд, флагов и targets"},
		{"help", "Справка"},
	}
	for _, command := range commands {
		if _, err := fmt.Fprintf(out, "  %-28s  %s\n", command[0], command[1]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(out, "\nКорень приложения — ближайший go.mod вверх от текущего каталога.\n--root задаёт каталог модуля явно, относительно текущего каталога.\nВложенные модули выбираются независимо; go.work не задаёт корень.\nПараметры команд: deployctl build --help, deployctl up --help, deployctl logs --help.\n")
	return err
}

func Run(ctx context.Context, args []string, out io.Writer, version string) error {
	if len(args) == 0 {
		return writeHelp(out, version)
	}
	switch args[0] {
	case "completion":
		return completion.Run(args[1:], out)
	case "__complete":
		return completion.Complete(args[1:], out)
	case "help", "--help", "-h":
		if len(args) != 1 {
			return fmt.Errorf("help does not accept arguments")
		}
		return writeHelp(out, version)
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
	case "config":
		if len(args) < 2 || args[1] != "check" {
			return fmt.Errorf("usage: deployctl config check [--root DIR] [--config FILE]")
		}
		return checkConfig(args[2:], out)
	case "up", "doctor", "status", "logs", "stop", "restart", "remove":
		return runRemote(ctx, args[0], args[1:], out)
	case "release":
		return runRelease(ctx, args[1:], out)
	case "clean":
		return runClean(args[1:], out)
	default:
		return fmt.Errorf("unknown command %q; use deployctl help", args[0])
	}
}

func runClean(args []string, out io.Writer) error {
	f := flag.NewFlagSet("clean", flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("root", "", "application module directory")
	dry := f.Bool("dry-run", false, "show the artifact directory without removing it")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("clean does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir, err := project.Root(cwd, *root)
	if err != nil {
		return err
	}
	return clean.Run(clean.Options{Root: dir, DryRun: *dry}, out)
}

func runRelease(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("release", flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("root", "", "application module directory")
	dry := f.Bool("dry-run", false, "show the next tag without creating or pushing it")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("release does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir, err := project.Root(cwd, *root)
	if err != nil {
		return err
	}
	return release.Run(ctx, release.Options{Root: dir, DryRun: *dry}, out)
}

func runBuild(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(out)
	var o build.Options
	filename := flags.String("config", "", "config path relative to module root (default config_deploy.yaml; fallback deploy.yaml)")
	flags.StringVar(&o.Root, "root", "", "application module directory")
	flags.StringVar(&o.Platform, "platform", runtime.GOOS+"-"+runtime.GOARCH, "build only this OS-ARCH, overriding configured platforms")
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
	platforms := []string{o.Platform}
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
		if !set["platform"] {
			if len(c.Build.Platforms) > 0 {
				platforms = c.Build.Platforms
			} else if c.Build.Platform != "" {
				platforms = []string{c.Build.Platform}
			}
		}
	}
	var failures []error
	for _, platform := range platforms {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		o.Platform = platform
		if _, err := build.Run(ctx, o, out); err != nil {
			failure := fmt.Errorf("%s: %w", platform, err)
			failures = append(failures, failure)
			if _, writeErr := fmt.Fprintf(out, "Ошибка сборки %s\n", failure); writeErr != nil {
				return errors.Join(append(failures, writeErr)...)
			}
		}
	}
	return errors.Join(failures...)
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
