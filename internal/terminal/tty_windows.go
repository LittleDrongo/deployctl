package terminal

import (
	"os"
	"syscall"
	"unsafe"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var getConsoleMode = kernel32.NewProc("GetConsoleMode")
var setConsoleMode = kernel32.NewProc("SetConsoleMode")

func terminalFile(f *os.File) bool {
	var mode uint32
	ok, _, _ := getConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	if ok == 0 {
		return false
	}
	const virtualTerminalProcessing = 0x0004
	if mode&virtualTerminalProcessing != 0 {
		return true
	}
	ok, _, _ = setConsoleMode.Call(f.Fd(), uintptr(mode|virtualTerminalProcessing))
	return ok != 0
}
