package deploy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LittleDrongo/deployctl/internal/config"
)

type doctorTransport struct {
	calls  int
	script string
	phase  string
}

func (d *doctorTransport) Shell(_ context.Context, _ config.Target, script, phase string) error {
	d.calls++
	d.script, d.phase = script, phase
	return nil
}

func (d *doctorTransport) Upload(context.Context, config.Target, string, string) error {
	return nil
}

func doctorTarget() config.Target {
	return config.Target{
		Host: "mini", BuildTarget: "linux-amd64", RemoteDir: "/opt/app", Binary: "app",
		Container: "app", Image: "app:dev", Base: "alpine:3.20",
		Mounts:       []config.Mount{{HostPath: "/opt/app", ContainerPath: "/app"}},
		ReadyTimeout: 120, ReadyInterval: 2, ReadyStable: 5, SSHTimeout: 1200,
	}
}

func TestDoctorDryRunAndTransport(t *testing.T) {
	transport := &doctorTransport{}
	var out bytes.Buffer
	if err := Doctor(context.Background(), transport, doctorTarget(), "prod", true, &out); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 0 || !strings.Contains(out.String(), "SSH не выполняется") {
		t.Fatalf("dry-run touched transport or omitted plan: calls=%d output=%s", transport.calls, out.String())
	}
	if err := Doctor(context.Background(), transport, doctorTarget(), "prod", false, &out); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || transport.phase != "doctor" || !strings.Contains(transport.script, "docker image inspect 'alpine:3.20'") {
		t.Fatalf("unexpected doctor call: %+v", transport)
	}
}

func TestDoctorScriptIsReadOnlyAndReportsAllProblems(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "required"), 0755); err != nil {
		t.Fatal(err)
	}
	target := config.Target{
		RemoteDir: "service", Base: "alpine:3.20",
		Mounts: []config.Mount{
			{HostPath: "required", ContainerPath: "/required"},
			{HostPath: "generated/data", ContainerPath: "/data", Create: "dir"},
			{HostPath: "generated/config", ContainerPath: "/config", Create: "file"},
		},
	}
	prefix := "flock() { :; }\ndocker() { [ \"$1\" = info ] && return 0; [ \"$1 $2\" = 'image inspect' ] && [ \"${FAIL_IMAGE:-}\" != yes ]; }\n"
	run := func(extraEnv ...string) (string, error) {
		cmd := exec.Command(testShell(t), "-c", prefix+doctorScript(target))
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), extraEnv...)
		data, err := cmd.CombinedOutput()
		return string(data), err
	}
	output, err := run()
	if err != nil || !strings.Contains(output, "Doctor: environment is ready") {
		t.Fatalf("successful doctor failed: %v\n%s", err, output)
	}
	for _, name := range []string{"service", "generated"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("doctor created %s", name)
		}
	}
	target.Mounts = append(target.Mounts, config.Mount{HostPath: "missing", ContainerPath: "/missing"})
	cmd := exec.Command(testShell(t), "-c", prefix+doctorScript(target))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FAIL_IMAGE=yes")
	data, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(data), "base image is unavailable") || !strings.Contains(string(data), "docker save -o deployctl-base-image.tar alpine:3.20") || !strings.Contains(string(data), "docker load -i deployctl-base-image.tar") || !strings.Contains(string(data), "mount[3] is missing") || !strings.Contains(string(data), "2 problem(s)") {
		t.Fatalf("doctor did not aggregate failures: %v\n%s", err, data)
	}
}
