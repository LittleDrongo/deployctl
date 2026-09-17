package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LittleDrongo/deployctl/internal/config"
	"github.com/LittleDrongo/deployctl/internal/terminal"
)

type streamTransport interface {
	Stream(context.Context, config.Target, string, string) error
}

type statusCapture interface {
	Capture(context.Context, config.Target, string) ([]byte, error)
}

type containerStatus struct {
	State      string `json:"state"`
	Running    bool   `json:"running"`
	Restarting bool   `json:"restarting"`
	Health     string `json:"health"`
	ExitCode   int    `json:"exit_code"`
	Image      string `json:"image"`
	Restarts   int    `json:"restarts"`
	Created    string `json:"created"`
}

func statusScript(t config.Target) string {
	// A missing container is a valid state, but Docker errors must remain errors.
	format := `{"state":{{json .State.Status}},"running":{{.State.Running}},"restarting":{{.State.Restarting}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}"none"{{end}},"exit_code":{{.State.ExitCode}},"image":{{json .Config.Image}},"restarts":{{.RestartCount}},"created":{{json .Created}}}`
	return "set -eu\nnames=$(docker container ls -a --format '{{.Names}}')\n" +
		"found=0\nfor name in $names; do\n if [ \"$name\" = " + quote(t.Container) + " ]; then found=1; break; fi\ndone\n" +
		"if [ \"$found\" -eq 0 ]; then printf '%s\\n' '{\"state\":\"absent\"}'; exit 0; fi\n" +
		"docker container inspect --format " + quote(format) + " " + quote(t.Container) + "\n"
}

func logsScript(t config.Target, tail int, follow bool) string {
	c := quote(t.Container)
	script := "set -eu\nif ! docker container inspect " + c + " >/dev/null 2>&1; then printf '%s\\n' " + quote("container not found: "+t.Container) + " >&2; exit 1; fi\nexec docker logs --tail " + quote(strconv.Itoa(tail))
	if follow {
		script += " --follow"
	}
	return script + " " + c + "\n"
}

// Status prints one target with the same columns as the all-target view.
func Status(ctx context.Context, transport Transport, t config.Target, name string, dry bool, out io.Writer) error {
	return Statuses(ctx, transport, map[string]config.Target{name: t}, dry, out)
}

// Statuses prints configured targets in name order, including failed targets.
func Statuses(ctx context.Context, transport Transport, targets map[string]config.Target, dry bool, out io.Writer) error {
	if len(targets) == 0 {
		return fmt.Errorf("no targets configured")
	}
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	if dry {
		for _, name := range names {
			t := targets[name]
			if err := config.ValidateTarget(t); err != nil {
				return fmt.Errorf("target %s: %w", name, err)
			}
			if _, err := fmt.Fprintf(out, "План status %s: контейнер %s на %s; SSH не выполняется.\n", name, t.Container, t.Host); err != nil {
				return err
			}
		}
		return nil
	}
	capture, ok := transport.(statusCapture)
	if !ok {
		return fmt.Errorf("transport does not support status capture")
	}
	rows := make([][]string, 0, len(names))
	var failures []error
	for _, name := range names {
		t := targets[name]
		row := []string{name, t.Container, "-", "-", "-", "-", "-", "-", "-", "-"}
		if err := config.ValidateTarget(t); err != nil {
			row[2] = "ошибка"
			failures = append(failures, fmt.Errorf("target %s: %w", name, err))
		} else if data, err := capture.Capture(ctx, t, statusScript(t)); err != nil {
			row[2] = "ошибка"
			failures = append(failures, fmt.Errorf("target %s: %w", name, err))
		} else {
			var state containerStatus
			if err := json.Unmarshal(data, &state); err != nil || state.State == "" {
				row[2] = "ошибка"
				failures = append(failures, fmt.Errorf("target %s: invalid Docker status response", name))
			} else {
				row[2] = state.State
				if state.State != "absent" {
					row[3] = strconv.FormatBool(state.Running)
					row[4] = strconv.FormatBool(state.Restarting)
					row[5] = state.Health
					row[6] = strconv.Itoa(state.ExitCode)
					row[7] = state.Image
					row[8] = strconv.Itoa(state.Restarts)
					row[9] = state.Created
				}
			}
		}
		rows = append(rows, row)
	}
	if err := printStatusTable(out, rows); err != nil {
		return err
	}
	return errors.Join(failures...)
}

var statusHeaders = []string{"Target", "Контейнер", "Состояние", "Запущен", "Перезапускается", "Health", "Код выхода", "Образ", "Перезапуски", "Создан"}

func printStatusTable(out io.Writer, rows [][]string) error {
	widths := make([]int, len(statusHeaders))
	for i, header := range statusHeaders {
		widths[i] = utf8.RuneCountInString(header)
	}
	for _, row := range rows {
		for i, value := range row {
			row[i] = strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, value)
			if n := utf8.RuneCountInString(row[i]); n > widths[i] {
				widths[i] = n
			}
		}
	}
	writeRow := func(row []string, color bool) error {
		for i, value := range row {
			if i > 0 {
				if _, err := io.WriteString(out, "  "); err != nil {
					return err
				}
			}
			pad := ""
			if i < len(row)-1 {
				pad = strings.Repeat(" ", widths[i]-utf8.RuneCountInString(value))
			}
			if color {
				value = terminal.StatusValue(out, statusHeaders[i], value)
			}
			if _, err := io.WriteString(out, value+pad); err != nil {
				return err
			}
		}
		_, err := io.WriteString(out, "\n")
		return err
	}
	if err := writeRow(statusHeaders, false); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writeRow(row, true); err != nil {
			return err
		}
	}
	return nil
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
