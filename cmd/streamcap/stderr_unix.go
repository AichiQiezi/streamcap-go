//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// redirectStderr points fd 2 at f, so Go runtime panics and native (cgo/AppKit) messages land in
// the log file instead of nowhere when the app is started from Finder.
func redirectStderr(f *os.File) error {
	return unix.Dup2(int(f.Fd()), 2)
}

// isTerminal reports whether f is an interactive terminal. (/dev/null, where Finder points
// stderr, is a character device too, so the file mode alone isn't enough.)
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}
