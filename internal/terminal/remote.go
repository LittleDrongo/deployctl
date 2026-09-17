// Package terminal styles human-readable output on the local terminal.
package terminal

import (
	"io"
	"os"
	"strings"
)

const (
	reset         = "\x1b[0m"
	green         = "\x1b[32m"
	red           = "\x1b[31m"
	yellow        = "\x1b[33m"
	redBackground = "\x1b[41;97m"
)

// Remote receives complete lines from the SSH redactor, never raw secrets.
// Log commands are left untouched; colors are only added to CLI diagnostics.
func Remote(out io.Writer, phase string) io.Writer {
	if phase == "logs" || strings.HasPrefix(phase, "logs ") || !enabled(out) {
		return out
	}
	return &remoteWriter{out: out, status: phase == "status"}
}

func enabled(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := out.(*os.File)
	return ok && terminalFile(f)
}

type remoteWriter struct {
	out    io.Writer
	status bool
}

func (w *remoteWriter) Write(data []byte) (int, error) {
	var result strings.Builder
	for _, part := range strings.SplitAfter(string(data), "\n") {
		line := strings.TrimSuffix(part, "\n")
		line = strings.TrimSuffix(line, "\r")
		suffix := part[len(line):]
		color := ""
		if strings.HasPrefix(line, "remote : Обновление отменено;") || strings.HasPrefix(line, "remote : Откат не завершён.") {
			color = redBackground
		} else if w.status {
			key, value, ok := strings.Cut(line, ": ")
			if ok {
				switch key {
				case "Состояние":
					color = red
					if value == "running" {
						color = green
					}
				case "Health":
					switch value {
					case "healthy":
						color = green
					case "unhealthy":
						color = red
					case "starting":
						color = yellow
					}
				case "Запущен":
					switch value {
					case "true":
						color = green
					case "false":
						color = red
					}
				case "Перезапускается":
					switch value {
					case "true":
						color = red
					case "false":
						color = green
					}
				case "Код выхода":
					if value == "0" {
						color = green
					} else {
						color = red
					}
				}
			}
		}
		if color != "" {
			result.WriteString(color)
		}
		result.WriteString(line)
		if color != "" {
			result.WriteString(reset)
		}
		result.WriteString(suffix)
	}
	text := result.String()
	n, err := io.WriteString(w.out, text)
	if err != nil {
		return 0, err
	}
	if n != len(text) {
		return 0, io.ErrShortWrite
	}
	return len(data), nil
}

// StatusValue colors one table cell without affecting its measured width.
func StatusValue(out io.Writer, key, value string) string {
	if !enabled(out) {
		return value
	}
	color := ""
	switch key {
	case "Состояние":
		if value == "running" {
			color = green
		} else if value != "-" {
			color = red
		}
	case "Health":
		switch value {
		case "healthy":
			color = green
		case "unhealthy":
			color = red
		case "starting":
			color = yellow
		}
	case "Запущен":
		switch value {
		case "true":
			color = green
		case "false":
			color = red
		}
	case "Перезапускается":
		switch value {
		case "true":
			color = red
		case "false":
			color = green
		}
	case "Код выхода":
		if value == "0" {
			color = green
		} else if value != "-" {
			color = red
		}
	}
	if color == "" {
		return value
	}
	return color + value + reset
}
