//go:build windows

package recorder

import (
	"log/slog"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Every ffmpeg we start joins one job object with KILL_ON_JOB_CLOSE: when this process exits for
// any reason (tray quit, crash, killed in Task Manager) Windows kills the ffmpeg children too,
// so none keep recording orphaned.
var (
	jobOnce sync.Once
	job     windows.Handle
)

func jobHandle() windows.Handle {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			slog.Warn("create job object failed", "err", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			slog.Warn("configure job object failed", "err", err)
			windows.CloseHandle(h)
			return
		}
		job = h
	})
	return job
}

func guardChild(cmd *exec.Cmd, _ string) {
	j := jobHandle()
	if j == 0 || cmd.Process == nil {
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(p)
	if err := windows.AssignProcessToJobObject(j, p); err != nil {
		slog.Debug("assign to job failed", "pid", cmd.Process.Pid, "err", err)
	}
}

func releaseChild(*exec.Cmd) {}

// SetStateDir is a no-op on Windows: the job object makes a PID file unnecessary.
func SetStateDir(string) {}

// ReapOrphans is a no-op on Windows (see guardChild).
func ReapOrphans() {}
