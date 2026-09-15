package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoot(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	child := filepath.Join(inner, "child")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{outer, inner} {
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ start, explicit, want string }{
		{child, "", inner}, {outer, "", outer}, {child, "../..", outer},
	} {
		got, err := Root(tc.start, tc.explicit)
		if err != nil || got != tc.want {
			t.Fatalf("Root(%q, %q) = %q, %v; want %q", tc.start, tc.explicit, got, err, tc.want)
		}
	}
	if _, err := Root(child, "."); err == nil {
		t.Fatal("explicit directory without go.mod accepted")
	}
	if _, err := Root(base, ""); err == nil {
		t.Fatal("missing module accepted")
	}
}
