package terminal

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRemoteStyles(t *testing.T) {
	for _, tc := range []struct{ line, color string }{
		{"Состояние: running", green},
		{"Состояние: exited", red},
		{"Состояние: restarting", red},
		{"Состояние: отсутствует", red},
		{"Health: healthy", green},
		{"Health: unhealthy", red},
		{"Health: starting", yellow},
		{"Health: none", ""},
		{"Образ: app:running", ""},
		{"Запущен: true", green},
		{"Запущен: false", red},
		{"Перезапускается: true", red},
		{"Перезапускается: false", green},
		{"Код выхода: 0", green},
		{"Код выхода: 1", red},
		{"remote : Обновление отменено; предыдущее состояние восстановлено", redBackground},
		{"remote : Откат не завершён. Резервный бинарник: /app/previous", redBackground},
	} {
		t.Run(tc.line, func(t *testing.T) {
			var out bytes.Buffer
			w := &remoteWriter{out: &out, status: true}
			data := tc.line + "\n"
			n, err := io.WriteString(w, data)
			want := data
			if tc.color != "" {
				want = tc.color + tc.line + reset + "\n"
			}
			if err != nil || n != len(data) || out.String() != want {
				t.Fatalf("Write = %d, %v, %q; want %q", n, err, out.String(), want)
			}
		})
	}
}

func TestPlainOutput(t *testing.T) {
	var out bytes.Buffer
	input := "Состояние: running\nremote : Обновление отменено; предыдущее состояние восстановлено\n"
	io.WriteString(Remote(&out, "status"), input)
	if out.String() != input {
		t.Fatal("redirected output styled")
	}
	for _, variable := range []string{"NO_COLOR", "TERM"} {
		t.Run(variable, func(t *testing.T) {
			value := "1"
			if variable == "TERM" {
				value = "dumb"
			}
			t.Setenv(variable, value)
			if enabled(os.Stdout) {
				t.Fatal("color override ignored")
			}
		})
	}
	if Remote(&out, "logs --follow") != &out {
		t.Fatal("logs decorated")
	}
}

func TestStylesOnlyStatusAndPreservesLines(t *testing.T) {
	var out bytes.Buffer
	w := &remoteWriter{out: &out}
	input := "Состояние: running\r\nordinary line\nlast line"
	io.WriteString(w, input)
	if out.String() != input {
		t.Fatalf("non-status output changed: %q", out.String())
	}
	out.Reset()
	w.status = true
	io.WriteString(w, input)
	if !strings.HasPrefix(out.String(), green+"Состояние: running"+reset+"\r\n") || !strings.HasSuffix(out.String(), "ordinary line\nlast line") {
		t.Fatal(out.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestOutputError(t *testing.T) {
	w := &remoteWriter{out: failingWriter{}, status: true}
	if _, err := io.WriteString(w, "Состояние: running\n"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
