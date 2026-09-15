package clean

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, ".bin", "nested", "app")
	if err := os.MkdirAll(filepath.Dir(artifact), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("binary"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(Options{Root: root, DryRun: true}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatal("dry-run removed artifact", err)
	}
	if err := Run(Options{Root: root}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".bin")); !os.IsNotExist(err) {
		t.Fatal("artifact directory remains", err)
	}
	if err := Run(Options{Root: root}, &out); err != nil {
		t.Fatal("repeated clean must be successful", err)
	}
}

func TestRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".bin")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := Run(Options{Root: root}, &bytes.Buffer{}); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("symlink target changed", err)
	}
}
