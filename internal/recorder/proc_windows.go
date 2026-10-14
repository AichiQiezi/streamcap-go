//go:build windows

package recorder

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps ffmpeg from flashing a console window when started from the GUI-less exe.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
