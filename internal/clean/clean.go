// Package clean removes local deployctl build artifacts.
package clean

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Options struct {
	Root   string
	DryRun bool
}

// Run removes the fixed .bin entry directly below an already resolved module
// root. Symlinks and junctions are refused instead of followed or removed.
func Run(options Options, out io.Writer) error {
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return err
	}
	artifacts := filepath.Join(root, ".bin")
	if filepath.Dir(artifacts) != root || filepath.Base(artifacts) != ".bin" {
		return fmt.Errorf("refusing unsafe artifact path %s", artifacts)
	}
	info, err := os.Lstat(artifacts)
	if os.IsNotExist(err) {
		_, err = fmt.Fprintf(out, "Каталог артефактов уже отсутствует: %s\n", artifacts)
		return err
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeType != 0 && !info.IsDir() {
		return fmt.Errorf("refusing to clean non-directory artifact path %s", artifacts)
	}
	if options.DryRun {
		_, err = fmt.Fprintf(out, "Удалить каталог локальных артефактов: %s\nDry-run: файлы не изменены.\n", artifacts)
		return err
	}
	if err := os.RemoveAll(artifacts); err != nil {
		return fmt.Errorf("remove %s: %w", artifacts, err)
	}
	_, err = fmt.Fprintf(out, "Каталог локальных артефактов удалён: %s\n", artifacts)
	return err
}
