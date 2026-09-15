// Package project locates application modules independently of the working directory.
package project

import (
	"fmt"
	"os"
	"path/filepath"
)

// Root chooses the nearest go.mod. An explicit root must itself contain go.mod.
// go.work does not replace a module root; nested modules take precedence.
func Root(start, explicit string) (string, error) {
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			explicit = filepath.Join(start, explicit)
		}
		start = explicit
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		info, err := os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s: go.mod is not a regular file", dir)
			}
			return dir, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if explicit != "" || parent == dir {
			return "", fmt.Errorf("no go.mod found from %s", start)
		}
		dir = parent
	}
}
