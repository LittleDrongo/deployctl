package setup

import (
	"archive/zip"
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LittleDrongo/deployctl/internal/config"
)

func put(t *testing.T, root, name, data string) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInitPreservesFilesAndDependency(t *testing.T) {
	root := t.TempDir()
	mod := "module example.com/service/v2\n\ngo 1.25.0\nrequire " + Buildcard + " v1.2.3\n"
	put(t, root, "go.mod", mod)
	put(t, root, "main.go", "existing source")
	put(t, root, "Makefile", "existing makefile")
	put(t, root, ".gitignore", "# existing\r\ncache/")
	t.Setenv("GOPROXY", "off")
	o := Options{Root: root, Scaffold: true, Makefile: true}
	var out bytes.Buffer
	if err := Run(context.Background(), o, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if _, err := config.Load(filepath.Join(root, config.Filename)); err != nil {
		t.Fatal(err)
	}
	if generated := read(t, root, config.Filename); !strings.Contains(generated, "docker_base_image: alpine:3.20") || strings.Contains(generated, "debian:bookworm-slim") {
		t.Fatalf("unexpected default base image:\n%s", generated)
	}
	if got := read(t, root, ".gitignore"); got != "# existing\r\ncache/\r\n/.bin/\r\n" {
		t.Fatalf("gitignore %q", got)
	}
	before := map[string]string{}
	for _, name := range []string{"go.mod", "main.go", "Makefile", ".gitignore", config.Filename} {
		before[name] = read(t, root, name)
	}
	if err := Run(context.Background(), o, &out); err != nil {
		t.Fatal(err)
	}
	for name, original := range before {
		if read(t, root, name) != original {
			t.Fatalf("second init changed %s", name)
		}
	}
	if read(t, root, "go.mod") != mod || read(t, root, "main.go") != "existing source" || read(t, root, "Makefile") != "existing makefile" {
		t.Fatal("existing files overwritten")
	}
}

func TestDryRunAndInvalidExistingConfig(t *testing.T) {
	root := t.TempDir()
	put(t, root, "go.mod", "module example.com/app\n\ngo 1.25.0\n")
	t.Setenv("GOPROXY", "off")
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Root: root, DryRun: true, Scaffold: true, Makefile: true}, &out); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("dry-run wrote files", err)
	}
	put(t, root, config.Filename, "version: 999\n")
	if err := Run(context.Background(), Options{Root: root}, &out); err == nil {
		t.Fatal("invalid existing config accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
		t.Fatal("mutated files before validation")
	}
}

// Supply a local file proxy to exercise real go get without Internet access.
func localProxy(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	base := "github.com/!little!drongo/buildcard/@v/"
	mod := "module " + Buildcard + "\n\ngo 1.25.0\n"
	put(t, root, base+"v1.0.1.mod", mod)
	put(t, root, base+"v1.0.1.info", `{"Version":"v1.0.1","Time":"2026-01-01T00:00:00Z"}`)
	put(t, root, base+"list", "v1.0.1\n")
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for name, contents := range map[string]string{"go.mod": mod, "card.go": "package buildcard\ntype Info struct{}\nfunc Snapshot() Info { return Info{} }\n"} {
		w, err := z.Create(Buildcard + "@v1.0.1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	put(t, root, base+"v1.0.1.zip", data.String())
	proxyPath := filepath.ToSlash(root)
	if !strings.HasPrefix(proxyPath, "/") {
		proxyPath = "/" + proxyPath
	}
	t.Setenv("GOPROXY", (&url.URL{Scheme: "file", Path: proxyPath}).String())
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", t.TempDir())
}

func TestInitWithDependencyAndRetry(t *testing.T) {
	localProxy(t)
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "go.mod", "module example.com/newapp\n\ngo 1.25.0\n")
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Root: dir, Scaffold: true}, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(read(t, dir, "go.mod"), Buildcard+" v1.0.1") {
		t.Fatal("dependency is not pinned")
	}
	if _, err := config.Load(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := goCommand(context.Background(), dir, &out, "run", "."); err != nil {
		t.Fatal(err, out.String())
	}
	if out.Len() != 0 {
		t.Fatal("scaffold prints output", out.String())
	}
	sum := read(t, dir, "go.sum")
	t.Setenv("GOPROXY", "off")
	if err := Run(context.Background(), Options{Root: dir, Scaffold: true}, &out); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "go.sum") != sum {
		t.Fatal("retry changed dependencies")
	}
}

func TestInitIncludesAllBuildPlatforms(t *testing.T) {
	root := t.TempDir()
	put(t, root, "go.mod", "module example.com/app\n\ngo 1.25.0\nrequire "+Buildcard+" v1.0.1\n")
	t.Setenv("GOPROXY", "off")
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Root: root}, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(filepath.Join(root, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	want := "windows-amd64,windows-arm64,linux-amd64,linux-arm64,android-arm64,windows-386,linux-386,darwin-amd64,darwin-arm64"
	if got := strings.Join(c.Build.Platforms, ","); got != want {
		t.Fatalf("platforms = %s; want %s", got, want)
	}
}

func TestInitConfigNameAndInstallHeader(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "legacy"}[legacy], func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "go.mod", "module example.com/app\n\ngo 1.25.0\nrequire "+Buildcard+" v1.0.1\n")
			t.Setenv("GOPROXY", "off")
			old := "version: 1\nbuild:\n  platform: linux-arm64\ntargets: {}\n"
			if legacy {
				put(t, root, config.LegacyFilename, old)
			}
			var out bytes.Buffer
			if err := Run(context.Background(), Options{Root: root}, &out); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if read(t, root, config.LegacyFilename) != old {
					t.Fatal("legacy config changed")
				}
				if _, err := os.Stat(filepath.Join(root, config.Filename)); !os.IsNotExist(err) {
					t.Fatal("second config created")
				}
			} else {
				first, _, _ := strings.Cut(read(t, root, config.Filename), "\n")
				if first != "# Install: go install github.com/LittleDrongo/deployctl@latest" {
					t.Fatal(first)
				}
			}
		})
	}
}
