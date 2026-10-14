package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr makes f the process's standard error handle. The GUI build has no console, so
// without this a Go runtime panic is printed nowhere. (The runtime looks the handle up on every
// write, so changing it after startup works.)
func redirectStderr(f *os.File) error {
	return windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
}

// isTerminal reports whether f is a console. False in the GUI build, which has none.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
