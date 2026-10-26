//go:build !windows && !darwin

package sysutil

// KeepAwake is a no-op here; use the desktop's inhibit settings (e.g. systemd-inhibit).
func KeepAwake(bool) {}
