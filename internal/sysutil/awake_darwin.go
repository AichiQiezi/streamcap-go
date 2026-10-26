package sysutil

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

var (
	awakeMu  sync.Mutex
	awakeCmd *exec.Cmd
)

// KeepAwake holds a `caffeinate -i` (no idle sleep) while on. -w ties it to this process, so it
// also goes away if StreamCap is killed.
func KeepAwake(on bool) {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	switch {
	case on && awakeCmd == nil:
		c := exec.Command("/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
		if err := c.Start(); err != nil {
			slog.Warn("keep awake: caffeinate failed", "err", err)
			return
		}
		go func() { _ = c.Wait() }()
		awakeCmd = c
		slog.Info("keep awake", "on", true)
	case !on && awakeCmd != nil:
		_ = awakeCmd.Process.Kill()
		awakeCmd = nil
		slog.Info("keep awake", "on", false)
	}
}
