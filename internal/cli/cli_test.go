package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	for _, action := range []string{"up", "stop", "restart", "remove"} {
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
