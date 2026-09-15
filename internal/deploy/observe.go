package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/LittleDrongo/deployctl/internal/config"
)

type streamTransport interface {
	Stream(context.Context, config.Target, string, string) error
}

func statusScript(t config.Target) string {
	c := quote(t.Container)
	format := `Состояние: {{.State.Status}}
Запущен: {{.State.Running}}
Перезапускается: {{.State.Restarting}}
Health: {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}
Код выхода: {{.State.ExitCode}}
Перезапуски: {{.RestartCount}}
Образ: {{.Config.Image}}
Создан: {{.Created}}`
	return "set -eu\nif docker container inspect " + c + " >/dev/null 2>&1; then\n" +
		" printf '%s\\n' " + quote("Контейнер: "+t.Container) + "\n" +
		" docker container inspect --format " + quote(format) + " " + c + "\n" +
		"else\n printf '%s\\n' " + quote("Контейнер: "+t.Container) + " " + quote("Состояние: отсутствует") + "\nfi\n"
}

func logsScript(t config.Target, tail int, follow bool) string {
	c := quote(t.Container)
	script := "set -eu\nif ! docker container inspect " + c + " >/dev/null 2>&1; then printf '%s\\n' " + quote("container not found: "+t.Container) + " >&2; exit 1; fi\nexec docker logs --tail " + quote(strconv.Itoa(tail))
	if follow {
		script += " --follow"
	}
	return script + " " + c + "\n"
}

// Status prints the current state of the configured project container.
func Status(ctx context.Context, transport Transport, t config.Target, name string, dry bool, out io.Writer) error {
	if err := config.ValidateTarget(t); err != nil {
		return err
	}
	if dry {
		_, err := fmt.Fprintf(out, "План status %s: контейнер %s на %s; SSH не выполняется.\n", name, t.Container, t.Host)
		return err
	}
	return transport.Shell(ctx, t, statusScript(t), "status")
}

// Logs prints a bounded log tail or follows it until the context is cancelled.
func Logs(ctx context.Context, transport Transport, t config.Target, name string, tail int, follow, dry bool, out io.Writer) error {
	if err := config.ValidateTarget(t); err != nil {
		return err
	}
	if tail < 1 || tail > 1_000_000 {
		return fmt.Errorf("tail must be 1..1000000")
	}
	if dry {
		_, err := fmt.Fprintf(out, "План logs %s: контейнер %s на %s, tail=%d, follow=%t; SSH не выполняется.\n", name, t.Container, t.Host, tail, follow)
		return err
	}
	script := logsScript(t, tail, follow)
	if !follow {
		return transport.Shell(ctx, t, script, "logs")
	}
	stream, ok := transport.(streamTransport)
	if !ok {
		return fmt.Errorf("transport does not support streaming logs")
	}
	err := stream.Stream(ctx, t, script, "logs --follow")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
