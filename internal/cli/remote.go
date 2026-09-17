package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/build"
	"github.com/LittleDrongo/deployctl/internal/config"
	"github.com/LittleDrongo/deployctl/internal/deploy"
	"github.com/LittleDrongo/deployctl/internal/project"
)

func runRemote(ctx context.Context, action string, args []string, out io.Writer) error {
	f := flag.NewFlagSet(action, flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("root", "", "application module directory")
	filename := f.String("config", "", "config path relative to module root")
	dry := f.Bool("dry-run", false, "print plan without building or connecting")
	var tag, pkg string
	var keep, follow bool
	tail := 100
	timeout := 20 * time.Minute
	if action == "up" {
		f.StringVar(&tag, "tag", "", "local tag to build in a separate checkout")
		f.StringVar(&pkg, "package", "", "main package override")
		f.DurationVar(&timeout, "timeout", timeout, "build timeout")
		f.BoolVar(&keep, "keep-artifact", false, "retain local binary after successful deployment")
	}
	if action == "logs" {
		f.IntVar(&tail, "tail", tail, "number of recent log lines")
		f.BoolVar(&follow, "f", false, "follow new log output until interrupted")
		f.BoolVar(&follow, "follow", false, "follow new log output until interrupted")
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if (action == "status" && f.NArg() > 1) || (action != "status" && f.NArg() != 1) {
		if action == "status" {
			return fmt.Errorf("usage: deployctl status [TARGET] [options]")
		}
		return fmt.Errorf("usage: deployctl %s TARGET [options]", action)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir, err := project.Root(cwd, *root)
	if err != nil {
		return err
	}
	c, err := config.Load(config.Path(dir, *filename))
	if err != nil {
		return err
	}
	transport := deploy.OpenSSH{Out: out}
	if action == "status" {
		if f.NArg() == 0 {
			return deploy.Statuses(ctx, transport, c.Targets, *dry, out)
		}
		t, ok := c.Targets[f.Arg(0)]
		if !ok {
			return fmt.Errorf("unknown target %q; configure targets in %s", f.Arg(0), config.Path(dir, *filename))
		}
		return deploy.Status(ctx, transport, t, f.Arg(0), *dry, out)
	}
	t, ok := c.Targets[f.Arg(0)]
	if !ok {
		return fmt.Errorf("unknown target %q; configure targets in %s", f.Arg(0), config.Path(dir, *filename))
	}
	if action == "doctor" {
		if !*dry {
			if err := deploy.CheckLocalClients(); err != nil {
				return err
			}
		}
		return deploy.Doctor(ctx, transport, t, f.Arg(0), *dry, out)
	}
	if action == "logs" {
		return deploy.Logs(ctx, transport, t, f.Arg(0), tail, follow, *dry, out)
	}
	if action != "up" {
		return deploy.Manage(ctx, transport, t, action, *dry, out)
	}
	if pkg == "" {
		pkg = c.Build.Package
	}
	result, err := build.Run(ctx, build.Options{Root: dir, Platform: t.BuildTarget, Package: pkg, Tag: tag, DryRun: *dry, Timeout: timeout}, out)
	if err != nil {
		return err
	}
	t.Image, err = deploy.VersionedImage(t.Image, result.ImageVersion)
	if err != nil {
		return err
	}
	if err := deploy.Up(ctx, transport, t, f.Arg(0), result.Artifact, *dry, out); err != nil {
		return err
	}
	if !keep && !*dry {
		if err := os.Remove(result.Artifact); err != nil {
			fmt.Fprintf(out, "Сервис обновлён; не удалось удалить локальный бинарник: %v\n", err)
		}
	}
	return nil
}
