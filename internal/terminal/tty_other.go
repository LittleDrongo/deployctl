//go:build !windows

package terminal

import "os"

func terminalFile(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0 && f.Name() != os.DevNull
}
