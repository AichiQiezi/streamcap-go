package sysutil

import (
	"context"
	"os/exec"
	"time"
)

// ClipboardText returns the clipboard's text via pbpaste (at most maxClipboard bytes).
func ClipboardText() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pbpaste").Output()
	if err != nil {
		return "", err
	}
	if len(out) > maxClipboard {
		out = out[:maxClipboard]
	}
	return string(out), nil
}
