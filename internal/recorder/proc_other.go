//go:build !windows

package recorder

import (
	"os/exec"
	"syscall"
)

// hideWindow puts ffmpeg in its own process group so a Ctrl+C in our terminal doesn't kill it
// mid-write; we stop it ourselves with "q" during shutdown.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
