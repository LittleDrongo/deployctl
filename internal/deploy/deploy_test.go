package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LittleDrongo/deployctl/internal/config"
)

type fakeTransport struct {
	calls       []string
	uploadError bool
}

func (f *fakeTransport) Shell(_ context.Context, _ config.Target, _ string, phase string) error {
	f.calls = append(f.calls, phase)
	return nil
}
func (f *fakeTransport) Upload(_ context.Context, _ config.Target, _, _ string) error {
	f.calls = append(f.calls, "upload")
	if f.uploadError {
		return errors.New("upload failed")
	}
	return nil
}

func TestTransportLifecycle(t *testing.T) {
	target := config.Target{Host: "alias", BuildTarget: "linux-amd64", RemoteDir: "/opt/app", Binary: "app", Container: "app", Image: "app:v1", Base: "alpine:3.20", Mounts: []config.Mount{{HostPath: "/opt/app", ContainerPath: "/app"}}, ReadyTimeout: 120, ReadyInterval: 2, ReadyStable: 5, SSHTimeout: 1200}
	var out bytes.Buffer
	transport := &fakeTransport{}
	if err := Up(context.Background(), transport, target, "prod", "missing-file", true, &out); err != nil || len(transport.calls) != 0 {
		t.Fatal("dry-run touched transport", err)
	}
	file := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(file, []byte("app binary"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Up(context.Background(), transport, target, "prod", file, false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(transport.calls, ",") != "prepare,upload,up,cleanup" {
		t.Fatal(transport.calls)
	}
	transport = &fakeTransport{uploadError: true}
	if err := Up(context.Background(), transport, target, "prod", file, false, &out); err == nil {
		t.Fatal("upload failure ignored")
	}
	if strings.Join(transport.calls, ",") != "prepare,upload,cleanup" {
		t.Fatal(transport.calls)
	}
}

func TestFailureDiagnosticsBeforeRollback(t *testing.T) {
	for _, old := range []bool{false, true} {
		for failure, message := range map[string]string{"dsn": "некорректный DSN database", "panic": "panic: application failed", "loop": "application is restarting", "silent": "Логи приложения отсутствуют", "logfail": "Не удалось получить логи приложения"} {
			t.Run(fmt.Sprintf("old=%t/%s", old, failure), func(t *testing.T) {
				dir := t.TempDir()
				for _, name := range []string{"containers", "service/.stage"} {
					if err := os.MkdirAll(filepath.Join(dir, name), 0755); err != nil {
						t.Fatal(err)
					}
				}
				files := map[string]string{"clock": "100\n", "service/.stage/app.new": "new binary"}
				if old {
					files["containers/app"] = "old"
					files["service/app"] = "old binary"
				}
				for name, value := range files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0755); err != nil {
						t.Fatal(err)
					}
				}
				target := target{Target: config.Target{Container: "app", Binary: "app", RemoteDir: "./service", Image: "app:v1", Base: "alpine:3.20", ReadyTimeout: 20, ReadyInterval: 1, ReadyStable: 3, Mounts: []config.Mount{{HostPath: "./service", ContainerPath: "/app"}}}}
				script := transactionMock + deployScript(target, "./service/.stage", fmt.Sprintf("%x", sha256.Sum256([]byte("new binary"))), "release")
				script = strings.ReplaceAll(script, "/tmp/deployctl-lock-", "./locks/")
				if err := os.WriteFile(filepath.Join(dir, "test.sh"), []byte(script), 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(testShell(t), "test.sh")
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "FAIL="+failure, "RELEASE=previous", "OLD_HEALTH=none")
				var out bytes.Buffer
				redactor := newRedactor(&out, target.Target)
				cmd.Stdout, cmd.Stderr = redactor, redactor
				err := cmd.Run()
				if flushErr := redactor.Flush(); flushErr != nil {
					t.Fatal(flushErr)
				}
				if err == nil || !strings.Contains(out.String(), message) || strings.Contains(out.String(), "topsecret") || !strings.Contains(out.String(), "exit=1") {
					calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
					t.Fatalf("%v: %s\ncalls: %s", err, out.String(), calls)
				}
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				logIndex := strings.Index(string(calls), "logs --tail 100 app\n")
				removeIndex := strings.Index(string(calls), "rm -f app\n")
				if logIndex < 0 || removeIndex < logIndex {
					t.Fatalf("logs lost before rollback: %s", calls)
				}
				data, err := os.ReadFile(filepath.Join(dir, "service/app"))
				if old && (err != nil || string(data) != "old binary") {
					t.Fatal("rollback lost previous binary")
				}
				if !old && !os.IsNotExist(err) {
					t.Fatal("failed first deploy left binary")
				}
			})
		}
	}
}

func TestManagementSemanticsAndLocks(t *testing.T) {
	for _, action := range []string{"stop", "restart", "remove"} {
		for _, exists := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exists=%t", action, exists), func(t *testing.T) {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, "containers"), 0755); err != nil {
					t.Fatal(err)
				}
				os.WriteFile(filepath.Join(dir, "clock"), []byte("100"), 0644)
				if exists {
					os.WriteFile(filepath.Join(dir, "containers/app"), []byte("old"), 0644)
				}
				target := target{Target: config.Target{Container: "app", RemoteDir: "./service", ReadyTimeout: 20, ReadyInterval: 1, ReadyStable: 3}}
				script := strings.ReplaceAll(transactionMock+manageScript(target, action), "/tmp/deployctl-lock-", "./locks/")
				cmd := exec.Command(testShell(t), "-c", script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "FAIL=", "OLD_HEALTH=healthy")
				out, err := cmd.CombinedOutput()
				if (err != nil) != (action == "restart" && !exists) {
					t.Fatalf("%v %s", err, out)
				}
				_, err = os.Stat(filepath.Join(dir, "containers/app"))
				if (err == nil) != (exists && action != "remove") {
					t.Fatal("wrong container lifecycle")
				}
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				if strings.Contains(string(calls), "create ") || strings.Contains(string(calls), "image ") {
					t.Fatal("management created container or removed images")
				}
			})
		}
	}
	for _, fd := range []string{"8", "9"} {
		dir := t.TempDir()
		script := "flock() { [ \"$2\" != " + quote(fd) + " ]; }\ndocker() { echo called > docker-called; }\n" + deployScript(target{Target: config.Target{Container: "app", RemoteDir: "./service"}}, "./service/.stage", "hash", "release")
		script = strings.ReplaceAll(script, "/tmp/deployctl-lock-", "./locks/")
		cmd := exec.Command(testShell(t), "-c", script)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "another operation") {
			t.Fatalf("lock %s ignored: %v %s", fd, err, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "docker-called")); !os.IsNotExist(err) {
			t.Fatal("Docker called under busy lock")
		}
	}
}

func TestSSHArgumentsAndRedaction(t *testing.T) {
	target := config.Target{Host: "production"}
	for _, scp := range []bool{true, false} {
		if strings.Contains(strings.Join(sshArgs(target, scp), " "), "-p ") || strings.Contains(strings.Join(sshArgs(target, scp), " "), "-P ") {
			t.Fatal("overrode SSH port")
		}
		if destination(target, scp) != "production" {
			t.Fatal("lost SSH alias")
		}
	}
	port := 2222
	target.Port = &port
	target.User = "deploy"
	if !strings.Contains(strings.Join(sshArgs(target, true), " "), "-P 2222") || destination(target, false) != "deploy@production" {
		t.Fatal("explicit SSH settings lost")
	}
	var out bytes.Buffer
	r := newRedactor(&out, config.Target{StartArgs: config.Arguments{{Key: "--key", Value: "private-value"}}})
	for _, part := range []string{"error private-", "value postgres://user:secret@host/db password=hidden\n", strings.Repeat("x", 64*1024), "secret\n", "panic: normal message"} {
		if _, err := r.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	r.Flush()
	for _, secret := range []string{"private-value", "user:secret", "hidden", "secret"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("leaked %s: %s", secret, out.String())
		}
	}
	if !strings.Contains(out.String(), "panic: normal message") || !strings.Contains(out.String(), "oversized") {
		t.Fatal(out.String())
	}
}

func TestContainerUserSelection(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		args         []string
		want         string
	}{
		{"default", "", nil, ""},
		{"ssh", "ssh", nil, "--user 1234:5678"},
		{"image", "image", nil, ""},
		{"explicit", "", []string{"--user", "42:43"}, "--user 42:43"},
		{"short", "", []string{"-u", "42:43"}, "-u 42:43"},
		{"equals", "", []string{"--user=42:43"}, "--user=42:43"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := target{Target: config.Target{ContainerUser: tc.policy, RunArgs: tc.args, Container: "app", RemoteDir: "/srv/app", Binary: "app", Image: "app:v1", Mounts: []config.Mount{{HostPath: "/srv/app", ContainerPath: "/app"}}}}
			script := `set -eu
id() { case $1 in -u) echo 1234 ;; -g) echo 5678 ;; esac; }
docker() { printf '%s ' "$@" > args; }
` + createContainer(target, "app", "release", "hash")
			cmd := exec.Command(testShell(t), "-c", script)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			if tc.want == "" {
				if strings.Contains(got, "--user") || strings.Contains(got, "-u ") {
					t.Fatal(got)
				}
			} else if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if tc.name == "explicit" && strings.Contains(got, "1234") {
				t.Fatal("overrode explicit user")
			}
		})
	}
}

func TestRuntimeHomeDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, policy, mount string
		args                []string
		wantHome            string
	}{
		{"default", "", "/app", nil, ""},
		{"ssh", "ssh", "/app", nil, `ENV HOME="/app"`},
		{"custom mount", "ssh", "/srv/my app", nil, `ENV HOME="/srv/my app"`},
		{"literal path", "ssh", `/srv/$name"quoted`, nil, `ENV HOME="/srv/\$name\"quoted"`},
		{"image user", "image", "/app", nil, ""},
		{"explicit user", "ssh", "/app", []string{"--user", "42:43"}, ""},
		{"explicit environment", "ssh", "/app", []string{"--env", "HOME=/custom", "--env-file", "/srv/env"}, `ENV HOME="/app"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := target{Target: config.Target{ContainerUser: tc.policy, RunArgs: tc.args, RemoteDir: "./service", Binary: "app", Image: "app:v1", Base: "alpine:3.20", Mounts: []config.Mount{{HostPath: "./service", ContainerPath: tc.mount, Create: "dir"}}}}
			// Capture the Dockerfile after shell processing, including literal $ and quotes.
			script := "set -eu\ndocker() { cat > Dockerfile; }\n" + runtimeScript(target)
			cmd := exec.Command(testShell(t), "-c", script)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			if tc.wantHome == "" {
				if strings.Contains(got, "ENV ") {
					t.Fatalf("changed image user's environment: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantHome+"\n") {
				t.Fatalf("missing %s in %s", tc.wantHome, got)
			}
			if tc.mount == "/app" {
				for key, value := range map[string]string{"XDG_CONFIG_HOME": "/app/.config", "XDG_CACHE_HOME": "/app/.cache", "XDG_DATA_HOME": "/app/.local/share", "XDG_STATE_HOME": "/app/.local/state"} {
					if !strings.Contains(got, fmt.Sprintf("ENV %s=\"%s\"\n", key, value)) {
						t.Fatalf("missing %s in %s", key, got)
					}
				}
			}
			// Defaults belong to the image so Docker's --env-file can override them.
			create := createContainer(target, "app", "release", "hash")
			if strings.Contains(create, "HOME=/app") {
				t.Fatalf("CLI defaults would override env-file: %s", create)
			}
			if tc.name == "explicit environment" && (!strings.Contains(create, "'HOME=/custom'") || !strings.Contains(create, "'--env-file' '/srv/env'")) {
				t.Fatalf("lost environment overrides: %s", create)
			}
		})
	}
}

func TestContainerMetadata(t *testing.T) {
	dir := t.TempDir()
	target := target{Target: config.Target{Container: "service", Image: "service:v2", ContainerUser: "image"}}
	script := `set -eu
id() { [ "$1" = -un ]; printf '%s\n' 'deployer user'; }
docker() {
 if [ "$1" = info ]; then printf '%s\n' 'engine host'; return; fi
 printf '%s\n' "$@" > args
}
` + createContainer(target, "temporary-candidate", "release", "hash")
	cmd := exec.Command(testShell(t), "-c", script)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BUILDCARD_CONTAINER=true", "BUILDCARD_CONTAINER_HOST=engine host", "BUILDCARD_CONTAINER_USER=deployer user", "BUILDCARD_CONTAINER_NAME=service", "BUILDCARD_CONTAINER_IMAGE=service:v2"} {
		if !strings.Contains(string(data), "--env\n"+want+"\n") {
			t.Fatalf("missing argument %q in %s", want, data)
		}
	}
}
