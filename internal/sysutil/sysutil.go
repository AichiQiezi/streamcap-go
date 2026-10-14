// Package sysutil holds OS-specific helpers: free disk space and showing files in the file manager.
package sysutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// OpenFolder opens dir in the system file manager, creating it first if needed.
func OpenFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", dir).Start()
	case "darwin":
		return exec.Command("open", dir).Run()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

// Reveal shows file selected in its folder (falls back to opening the folder on Linux).
func Reveal(file string) error {
	abs, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		// explorer exits non-zero even on success, so don't treat its status as an error.
		return exec.Command("explorer", "/select,", abs).Start()
	case "darwin":
		return exec.Command("open", "-R", abs).Run()
	default:
		return exec.Command("xdg-open", filepath.Dir(abs)).Start()
	}
}

// DiskFree returns free and total bytes of the filesystem holding path (walking up to an existing dir).
func DiskFree(path string) (free, total uint64, err error) {
	p, _ := filepath.Abs(path)
	for {
		if _, statErr := os.Stat(p); statErr == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return diskFree(p)
}
