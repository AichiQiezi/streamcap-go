package sysutil

import (
	"log/slog"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var (
	awakeOnce sync.Once
	awakeCh   = make(chan bool, 1)
)

// KeepAwake asks Windows not to sleep on idle (on) or lets it again (off). The display may still
// turn off, and closing the lid still follows the power settings. The request belongs to the
// thread that made it, so one locked OS thread owns it.
func KeepAwake(on bool) {
	awakeOnce.Do(func() { go awakeLoop() })
	select { // keep only the latest wish
	case <-awakeCh:
	default:
	}
	awakeCh <- on
}

func awakeLoop() {
	runtime.LockOSThread()
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	cur := false
	for on := range awakeCh {
		if on == cur {
			continue
		}
		flags := uintptr(esContinuous)
		if on {
			flags |= esSystemRequired
		}
		if r, _, err := proc.Call(flags); r == 0 {
			slog.Warn("keep awake: SetThreadExecutionState failed", "on", on, "err", err)
			continue
		}
		cur = on
		slog.Info("keep awake", "on", on)
	}
}
