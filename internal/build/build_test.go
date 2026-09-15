package build

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, name, data string) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

// A tiny independent consumer exercises the linker contract without fetching modules.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/service/v2\n\ngo 1.25.0\n\nrequire github.com/LittleDrongo/buildcard v1.0.0\nreplace github.com/LittleDrongo/buildcard => ./card\n")
	write(t, root, "card/go.mod", "module github.com/LittleDrongo/buildcard\n\ngo 1.25.0\n")
	write(t, root, "card/card.go", `package buildcard
var buildVersion, buildCommitHash, buildCommitShort, buildCommitDate, buildDirty, buildTime, buildRepository string
func Values() []string { return []string{buildVersion, buildCommitHash, buildCommitShort, buildCommitDate, buildDirty, buildTime, buildRepository} }
`)
	write(t, root, "main.go", `package main
import ("encoding/json"; "os"; "github.com/LittleDrongo/buildcard")
func main() { json.NewEncoder(os.Stdout).Encode(buildcard.Values()) }
`)
	write(t, root, ".gitignore", "/.bin/\n")
	return root
}

func run(t *testing.T, root string, args ...string) Result {
	t.Helper()
	o := Options{Root: root, Timeout: time.Minute}
	if len(args) != 0 {
		o.Tag = args[0]
	}
	var out bytes.Buffer
	result, err := Run(context.Background(), o, &out)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out.String())
	}
	return result
}

func values(t *testing.T, artifact string) []string {
	t.Helper()
	data, err := exec.Command(artifact).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return got
}

func TestBuildLinkerContractAndFailedBuild(t *testing.T) {
	root := fixture(t)
	result := run(t, root)
	got := values(t, result.Artifact)
	if got[0] != "dev" || got[1] != "unknown" || got[4] != "false" || got[6] != "example.com/service/v2" {
		t.Fatalf("metadata: %v", got)
	}
	if _, err := time.Parse(time.RFC3339, got[5]); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(result.Artifact), "service-dev-") {
		t.Fatal(result.Artifact)
	}
	// Rebuilding the same version must replace the artifact on Windows too.
	run(t, root)
	before, err := os.ReadFile(result.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "main.go", "package main\nfunc main() { doesNotExist() }\n")
	_, err = Run(context.Background(), Options{Root: root, Timeout: time.Minute}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("broken application built successfully")
	}
	after, err := os.ReadFile(result.Artifact)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed build changed previous artifact", err)
	}
}

func TestDryRunDoesNotRunGoOrWriteFiles(t *testing.T) {
	root := fixture(t)
	t.Setenv("PATH", "")
	var out bytes.Buffer
	result, err := Run(context.Background(), Options{Root: root, Platform: "linux-amd64", DryRun: true, Timeout: time.Minute}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), result.Artifact) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".bin")); !os.IsNotExist(err) {
		t.Fatal("dry-run created artifacts", err)
	}
}

func TestTagBuildPreservesWorktree(t *testing.T) {
	root := fixture(t)
	gitCommand := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	gitCommand("init")
	gitCommand("config", "user.name", "Test")
	gitCommand("config", "user.email", "test@example.com")
	gitCommand("add", ".")
	gitCommand("commit", "-m", "release")
	gitCommand("tag", "-a", "1.2.3", "-m", "release")
	commit := gitCommand("rev-parse", "HEAD")
	write(t, root, "main.go", "this worktree intentionally does not compile\n")
	status := gitCommand("status", "--porcelain")
	worktrees := gitCommand("worktree", "list", "--porcelain")
	_, err := Run(context.Background(), Options{Root: root, Tag: "v1.2.3", DryRun: true, Timeout: time.Minute}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".bin")); !os.IsNotExist(err) {
		t.Fatal("tag dry-run wrote artifacts")
	}
	result := run(t, root, "v1.2.3")
	got := values(t, result.Artifact)
	if got[0] != "v1.2.3" || got[1] != commit || got[4] != "false" {
		t.Fatalf("tag metadata: %v", got)
	}
	if gitCommand("status", "--porcelain") != status || gitCommand("rev-parse", "HEAD") != commit || gitCommand("worktree", "list", "--porcelain") != worktrees {
		t.Fatal("tag build changed caller Git state")
	}
}

func TestLegacyAndMissingMetadata(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/legacy\n\ngo 1.25.0\n")
	write(t, root, "internal/buildinfo/info.go", "package buildinfo\nvar buildVersion string\nfunc Version() string { return buildVersion }\n")
	write(t, root, "main.go", "package main\nimport (\"fmt\"; \"example.com/legacy/internal/buildinfo\")\nfunc main() {fmt.Print(buildinfo.Version())}\n")
	result := run(t, root)
	data, err := exec.Command(result.Artifact).Output()
	if err != nil || string(data) != "dev" {
		t.Fatalf("legacy: %s %v", data, err)
	}
	write(t, root, "main.go", "package main\nfunc main() {}\n")
	var out bytes.Buffer
	_, err = Run(context.Background(), Options{Root: root, Timeout: time.Minute}, &out)
	if err != nil || !strings.Contains(out.String(), "Предупреждение:") {
		t.Fatalf("missing metadata: %v %s", err, out.String())
	}
}

func TestTaggedLegacyBuildinfo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/legacy\n\ngo 1.25.0\n")
	write(t, root, "internal/buildinfo/info.go", "package buildinfo\nvar buildVersion string\nfunc Version() string { return buildVersion }\n")
	write(t, root, "main.go", "package main\nimport (\"fmt\"; \"example.com/legacy/internal/buildinfo\")\nfunc main() { fmt.Print(buildinfo.Version()) }\n")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	git("add", ".")
	git("commit", "-m", "legacy release")
	git("tag", "v0.9.0")
	write(t, root, "main.go", "package main\nfunc main() {}\n")

	result := run(t, root, "v0.9.0")
	data, err := exec.Command(result.Artifact).Output()
	if err != nil || string(data) != "v0.9.0" {
		t.Fatalf("tagged legacy buildinfo: %s %v", data, err)
	}
}

func TestEnvironmentOverridesCaseInsensitively(t *testing.T) {
	t.Setenv("GOOS", "invalid")
	t.Setenv("GOARCH", "invalid")
	t.Setenv("GOWORK", "invalid")
	if runtime.GOOS == "windows" {
		t.Setenv("cgo_enabled", "1")
	}
	env := strings.Join(environment("linux-arm64"), "\n")
	if strings.Contains(env, "invalid") || !strings.Contains(env, "GOOS=linux\nGOARCH=arm64\nCGO_ENABLED=0\nGOWORK=off") {
		t.Fatal("incorrect build environment")
	}
}
