package release

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNext(t *testing.T) {
	for tag, want := range map[string]string{
		"":          "v1.0.0",
		"1.0.0":     "v1.0.1",
		"v1.0.9":    "v1.0.10",
		"v1.1.99":   "v1.2.0",
		"v1.9.99":   "v1.10.0",
		"v12.34.56": "v12.34.57",
		"release":   "v1.0.0",
	} {
		if got := next(tag); got != want {
			t.Errorf("next(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestNearestReleaseReadsTenCommitsAtATime(t *testing.T) {
	var skips []int
	run := func(_ context.Context, args ...string) (string, error) {
		skip, err := strconv.Atoi(args[len(args)-2])
		if err != nil {
			t.Fatal(args)
		}
		skips = append(skips, skip)
		var lines []string
		for i := 0; i < batchSize; i++ {
			commit := skip + i
			decorations := ""
			if commit == 23 {
				decorations = "tag: v1.4.8, tag: 1.4.9, tag: nightly"
			}
			lines = append(lines, fmt.Sprintf("%040d\t%s", commit, decorations))
		}
		return strings.Join(lines, "\n"), nil
	}
	tag, err := nearestRelease(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "1.4.9" {
		t.Fatalf("nearest tag = %q", tag)
	}
	if fmt.Sprint(skips) != "[0 10 20]" {
		t.Fatalf("batch offsets = %v", skips)
	}
}

func TestRunCreatesAndPushesOnlyNearestRelease(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitOutside(t, "init", "--bare", remote)
	git := func(args ...string) string { return gitIn(t, dir, args...) }
	git("init")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "Test")
	git("remote", "add", "origin", remote)
	git("commit", "--allow-empty", "-m", "initial")
	git("tag", "v1.4.9")
	for i := 0; i < 23; i++ {
		git("commit", "--allow-empty", "-m", fmt.Sprintf("commit %d", i))
	}
	// A higher tag outside HEAD history must not influence the next version.
	head := git("rev-parse", "HEAD")
	git("checkout", "--orphan", "unrelated")
	git("commit", "--allow-empty", "-m", "unrelated")
	git("tag", "v99.0.0")
	git("checkout", "--detach", head)

	var dry bytes.Buffer
	if err := Run(context.Background(), Options{Root: dir, DryRun: true}, &dry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dry.String(), "v1.4.9") || !strings.Contains(dry.String(), "v1.4.10") {
		t.Fatal(dry.String())
	}
	if git("tag", "--points-at", head) != "" {
		t.Fatal("dry-run created a tag")
	}
	if err := Run(context.Background(), Options{Root: dir}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := git("rev-parse", "refs/tags/v1.4.10"); got != head {
		t.Fatalf("tag points to %s, want %s", got, head)
	}
	if got := git("ls-remote", "origin", "refs/tags/v1.4.10"); !strings.HasPrefix(got, head) {
		t.Fatalf("new tag was not pushed: %s", got)
	}
	if got := git("ls-remote", "origin", "refs/tags/v1.4.9", "refs/tags/v99.0.0"); got != "" {
		t.Fatalf("unrelated tags were pushed: %s", got)
	}
	if err := Run(context.Background(), Options{Root: dir}, io.Discard); err == nil || !strings.Contains(err.Error(), "already has release tag") {
		t.Fatalf("same commit released twice: %v", err)
	}
}

func TestRunRequiresCleanTreeAndPreservesTagAfterPushFailure(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return gitIn(t, dir, args...) }
	git("init")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "Test")
	git("commit", "--allow-empty", "-m", "initial")
	git("remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if err := os.WriteFile(filepath.Join(dir, "pending"), []byte("change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), Options{Root: dir}, io.Discard); err == nil || !strings.Contains(err.Error(), "clean working tree") {
		t.Fatalf("dirty tree accepted: %v", err)
	}
	git("add", "pending")
	git("commit", "-m", "pending")
	err := Run(context.Background(), Options{Root: dir}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "local tag preserved") || !strings.Contains(err.Error(), "git push --no-follow-tags") {
		t.Fatalf("push failure recovery missing: %v", err)
	}
	if got := git("tag", "--points-at", "HEAD"); got != "v1.0.0" {
		t.Fatalf("local tag missing: %s", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func gitOutside(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}
