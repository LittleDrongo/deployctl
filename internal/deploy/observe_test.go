package deploy

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/LittleDrongo/deployctl/internal/config"
)

type observeTransport struct {
	shells, streams int
	script, phase   string
	captured        []string
	responses       map[string][]byte
	failures        map[string]error
}

func (o *observeTransport) Shell(_ context.Context, _ config.Target, script, phase string) error {
	o.shells++
	o.script, o.phase = script, phase
	return nil
}
func (o *observeTransport) Stream(_ context.Context, _ config.Target, script, phase string) error {
	o.streams++
	o.script, o.phase = script, phase
	return nil
}
func (o *observeTransport) Capture(_ context.Context, target config.Target, script string) ([]byte, error) {
	o.captured = append(o.captured, target.Host)
	o.script = script
	return o.responses[target.Host], o.failures[target.Host]
}
func (o *observeTransport) Upload(context.Context, config.Target, string, string) error { return nil }

func TestStatusAndLogsTransport(t *testing.T) {
	target := doctorTarget()
	transport := &observeTransport{responses: map[string][]byte{"mini": []byte(`{"state":"running","running":true,"restarting":false,"health":"healthy","exit_code":0,"image":"app:v1","restarts":2,"created":"2026-09-14T10:00:00Z"}`)}}
	var out bytes.Buffer
	if err := Status(context.Background(), transport, target, "prod", true, &out); err != nil || len(transport.captured) != 0 {
		t.Fatalf("status dry-run: %v calls=%d", err, len(transport.captured))
	}
	out.Reset()
	if err := Status(context.Background(), transport, target, "prod", false, &out); err != nil {
		t.Fatal(err)
	}
	if len(transport.captured) != 1 || !strings.Contains(transport.script, ".RestartCount") || !strings.Contains(out.String(), "app:v1") || strings.Contains(out.String(), "|") {
		t.Fatalf("status output/script: %+v\n%s", transport, out.String())
	}
	if err := Logs(context.Background(), transport, target, "prod", 250, false, false, &out); err != nil {
		t.Fatal(err)
	}
	if transport.shells != 1 || !strings.Contains(transport.script, "docker logs --tail '250' 'app'") {
		t.Fatalf("tail script: %+v", transport)
	}
	if err := Logs(context.Background(), transport, target, "prod", 10, true, false, &out); err != nil {
		t.Fatal(err)
	}
	if transport.streams != 1 || transport.phase != "logs --follow" || !strings.Contains(transport.script, "--follow 'app'") {
		t.Fatalf("follow script: %+v", transport)
	}
	if err := Logs(context.Background(), transport, target, "prod", 0, false, false, &out); err == nil {
		t.Fatal("invalid tail accepted")
	}
}

func TestStatusesShowsAllTargetsAfterFailure(t *testing.T) {
	first, second, third := doctorTarget(), doctorTarget(), doctorTarget()
	first.Host, second.Host, third.Host = "first", "second", "third"
	first.Container, second.Container, third.Container = "a", "b", "c"
	transport := &observeTransport{
		responses: map[string][]byte{
			"first": []byte(`{"state":"running","running":true,"restarting":false,"health":"none","exit_code":0,"image":"app:v2","restarts":0,"created":"date"}`),
			"third": []byte(`{"state":"absent"}`),
		},
		failures: map[string]error{"second": errors.New("ssh unavailable")},
	}
	var out bytes.Buffer
	err := Statuses(context.Background(), transport, map[string]config.Target{"z": third, "a": first, "m": second}, false, &out)
	if err == nil || !strings.Contains(err.Error(), "target m") {
		t.Fatalf("error = %v", err)
	}
	if strings.Join(transport.captured, ",") != "first,second,third" {
		t.Fatalf("capture order = %v", transport.captured)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || strings.Fields(lines[1])[0] != "a" || strings.Fields(lines[2])[0] != "m" || strings.Fields(lines[3])[0] != "z" || !strings.Contains(lines[2], "ошибка") || !strings.Contains(lines[3], "absent") {
		t.Fatalf("table:\n%s", out.String())
	}
	if strings.Contains(out.String(), "|") || !strings.Contains(lines[0], "Образ") {
		t.Fatalf("invalid table:\n%s", out.String())
	}
}

func TestStatusScriptDistinguishesMissingFromDockerFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows bash compatibility varies by installation")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	target := doctorTarget()
	for _, tc := range []struct {
		name, docker, want string
		fail               bool
	}{
		{"absent", `docker() { if [ "$1 $2" = "container ls" ]; then return 0; fi; return 8; }`, `{"state":"absent"}`, false},
		{"daemon error", `docker() { return 8; }`, "", true},
		{"inspect error", `docker() { if [ "$1 $2" = "container ls" ]; then printf 'app\n'; return 0; fi; return 8; }`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bash, "-c", tc.docker+"\n"+statusScript(target))
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || (!tc.fail && strings.TrimSpace(string(output)) != tc.want) {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
}
