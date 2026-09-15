package gitmeta

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	git("commit", "--allow-empty", "-m", "initial")
	short := git("rev-parse", "--short", "HEAD")
	check := func(want string) {
		t.Helper()
		info, err := Read(context.Background(), dir)
		if err != nil || info.Version != want {
			t.Fatalf("Read = %+v, %v; want %s", info, err, want)
		}
	}
	check("dev-g" + short)
	git("tag", "v1.0.0")
	check("v1.0.0")
	git("commit", "--allow-empty", "-m", "next")
	check("v1.0.0-dev")
	if err := os.WriteFile(filepath.Join(dir, "untracked"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	check("v1.0.0-dev-dirty")
}

func TestWithoutGit(t *testing.T) {
	info, err := Read(context.Background(), t.TempDir())
	if err != nil || info.Version != "dev" || info.HasGit {
		t.Fatalf("Read = %+v, %v", info, err)
	}
}
