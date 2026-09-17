package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInfoAndToolVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"version"}, &out, "v9.8.7"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "deployctl v9.8.7\n" {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := Run(context.Background(), []string{"info", "--root", dir, "--json"}, &out, "v9.8.7"); err != nil {
		t.Fatal(err)
	}
	var got struct{ Version, Root string }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "dev" || got.Root != dir {
		t.Fatalf("application metadata: %+v", got)
	}
	if err := Run(context.Background(), []string{"up"}, &out, "dev"); err == nil {
		t.Fatal("up without a target succeeded")
	}
}

func TestBuildConfigFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "cmd", "app")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "custom.yaml"), []byte("version: 1\nbuild:\n  package: ./cmd/app\n  platform: linux-arm64\ntargets: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"build", "--config", "custom.yaml", "--dry-run"}, &out, "dev"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Пакет: ./cmd/app")) || !bytes.Contains(out.Bytes(), []byte("Платформа: linux-arm64")) {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := Run(context.Background(), []string{"build", "--config", "custom.yaml", "--dry-run", "--package", ".", "--platform", "windows-amd64"}, &out, "dev"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Пакет: .\n")) || !bytes.Contains(out.Bytes(), []byte("Платформа: windows-amd64")) {
		t.Fatal(out.String())
	}
	if err := Run(context.Background(), []string{"build", "--config", "missing.yaml", "--dry-run"}, &out, "dev"); err == nil {
		t.Fatal("missing explicit config ignored")
	}
}

func TestRemoteDryRunDoesNotRunCommands(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	yaml := `version: 1
defaults:
  build_target: linux-amd64
  remote_dir: /opt/app
  binary_name: app
  docker_container: app
  docker_image: app:latest
  docker_base_image: alpine:3.20
  docker_mounts:
    - host_path: /opt/app
      container_path: /app
  start_args:
    --dsn: supersecret
targets:
  prod:
    host: production
`
	if err := os.WriteFile(filepath.Join(root, "deploy.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	for _, action := range []string{"up", "doctor", "status", "logs", "stop", "restart", "remove"} {
		var out bytes.Buffer
		if err := Run(context.Background(), []string{action, "prod", "--root", root, "--dry-run"}, &out, "dev"); err != nil {
			t.Fatal(action, err)
		}
		if bytes.Contains(out.Bytes(), []byte("supersecret")) {
			t.Fatal("dry-run exposed secret")
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".bin")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote artifacts")
	}
}

func TestStatusWithoutTargetDryRun(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	config := `version: 1
defaults:
  build_target: linux-amd64
  remote_dir: /opt/app
  binary_name: app
  docker_container: app
  docker_image: app:latest
  docker_base_image: alpine:3.20
  docker_mounts:
    - host_path: /opt/app
      container_path: /app
targets:
  prod:
    host: production
  dev:
    host: development
`
	if err := os.WriteFile(filepath.Join(root, "service.yaml"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"status", "--root", root, "--dry-run"}, &out, "dev"); err != nil {
		t.Fatal(err)
	}
	if strings.Index(out.String(), "status dev") > strings.Index(out.String(), "status prod") || strings.Count(out.String(), "SSH не выполняется") != 2 {
		t.Fatal(out.String())
	}
	if err := Run(context.Background(), []string{"status", "prod", "dev", "--root", root, "--dry-run"}, &out, "dev"); err == nil {
		t.Fatal("two targets accepted")
	}
}

func TestBuildPlatformSelection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, fields string
		args, want   []string
	}{
		{"list", "platforms: [linux-arm64, windows-amd64]", nil, []string{"linux-arm64", "windows-amd64"}},
		{"override", "platforms: [linux-arm64, windows-amd64]", []string{"--platform", "darwin-arm64"}, []string{"darwin-arm64"}},
		{"empty", "platforms: []", nil, []string{runtime.GOOS + "-" + runtime.GOARCH}},
		{"legacy", "platform: linux-amd64", nil, []string{"linux-amd64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "deploy.yaml"), []byte("version: 1\nbuild:\n  "+tc.fields+"\ntargets: {}\n"), 0644); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			args := append([]string{"build", "--root", root, "--dry-run"}, tc.args...)
			if err := Run(context.Background(), args, &out, "dev"); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, line := range strings.Split(out.String(), "\n") {
				if value, ok := strings.CutPrefix(line, "Платформа: "); ok {
					got = append(got, strings.Fields(value)[0])
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("platforms = %v; want %v; output:\n%s", got, tc.want, out.String())
			}
			if _, err := os.Stat(filepath.Join(root, ".bin")); !os.IsNotExist(err) {
				t.Fatal("dry-run created artifacts")
			}
		})
	}
}

func TestBuildContinuesAfterPlatformFailure(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":      "module example.com/app\n\ngo 1.25.0\n",
		"main.go":     "package main\nfunc main() {}\n",
		"deploy.yaml": "version: 1\nbuild:\n  platforms: [invalid-invalid, " + runtime.GOOS + "-" + runtime.GOARCH + "]\ntargets: {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	err := Run(context.Background(), []string{"build", "--root", root}, &out, "dev")
	if err == nil || !strings.Contains(err.Error(), "invalid-invalid") {
		t.Fatalf("error = %v; output:\n%s", err, out.String())
	}
	files, err := filepath.Glob(filepath.Join(root, ".bin", "app-*-"+runtime.GOOS+"-"+runtime.GOARCH+"*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("second platform produced no artifact: %v; output:\n%s", err, out.String())
	}
}
