//go:build !windows && !darwin

package desktop

import (
	"fmt"
	"os/exec"
)

// Run is not available here (Linux etc.): the caller falls back to the browser.
func Run(Options) error { return ErrUnsupported }

// SetStatus is a no-op without a tray.
func SetStatus(string) {}

// Alert prints nothing special off Windows/macOS; errors go to the log and stderr.
func Alert(string, string) {}

// Foreground is unknown without a native window.
func Foreground() (fg, ok bool) { return false, false }

// OpenNotificationSettings has no common target across Linux desktops.
func OpenNotificationSettings() error {
	return fmt.Errorf("请在桌面环境的设置里管理通知")
}

// Notify uses notify-send (libnotify) when it's installed.
func Notify(title, body, _ string) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return fmt.Errorf("没有找到 notify-send，无法弹出系统通知")
	}
	return exec.Command("notify-send", "-a", "StreamCap", title, body).Run()
}
