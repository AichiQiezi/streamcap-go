//go:build !windows && !darwin

package sysutil

import "errors"

// ClipboardText is not supported here.
func ClipboardText() (string, error) { return "", errors.New("clipboard not supported on this platform") }
