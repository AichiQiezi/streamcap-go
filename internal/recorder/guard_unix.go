//go:build !windows

package recorder

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Unix has no portable "kill children with the parent", so long-running ffmpeg PIDs are recorded in
// <data>/children.json. If we die without cleaning up (kill -9, crash), the next start finds
// leftovers that are still running and stops them gracefully, so none keep recording orphaned.

type childRec struct {
	PID    int    `json:"pid"`
	Output string `json:"output"`
	// Started is `ps -o lstart=` at spawn: with the PID it identifies the process uniquely, so a
	// recycled PID is never mistaken for our ffmpeg. (Matching the output path in `ps` output
	// doesn't work: ps escapes non-ASCII bytes, and room names are usually Chinese.)
	Started string `json:"started"`
}

var (
	childMu   sync.Mutex
	children  = map[int]childRec{}
	childFile string
)

// SetStateDir sets where children.json is kept.
func SetStateDir(dir string) {
	childMu.Lock()
	childFile = filepath.Join(dir, "children.json")
	childMu.Unlock()
}

func guardChild(cmd *exec.Cmd, output string) {
	if cmd.Process == nil {
		return
	}
	started := processStart(cmd.Process.Pid)
	childMu.Lock()
	children[cmd.Process.Pid] = childRec{PID: cmd.Process.Pid, Output: output, Started: started}
	saveChildrenLocked()
	childMu.Unlock()
}

func releaseChild(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	childMu.Lock()
	delete(children, cmd.Process.Pid)
	saveChildrenLocked()
	childMu.Unlock()
}

func saveChildrenLocked() {
	if childFile == "" {
		return
	}
	list := make([]childRec, 0, len(children))
	for _, c := range children {
		list = append(list, c)
	}
	raw, _ := json.Marshal(list)
	tmp := childFile + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, childFile)
	}
}

// ReapOrphans stops ffmpeg processes left running by a previous instance. It only touches a PID
// whose command line is ffmpeg writing the recorded output path, so a reused PID is never killed.
func ReapOrphans() {
	childMu.Lock()
	path := childFile
	childMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var list []childRec
	_ = json.Unmarshal(raw, &list)
	for _, c := range list {
		comm, err := exec.Command("ps", "-o", "comm=", "-p", itoa(c.PID)).Output()
		if err != nil {
			continue // not running any more
		}
		if !strings.Contains(string(comm), "ffmpeg") || c.Started == "" || processStart(c.PID) != c.Started {
			continue // PID reused by something else
		}
		slog.Warn("stopping ffmpeg left over from a previous run", "pid", c.PID, "output", c.Output)
		_ = syscall.Kill(c.PID, syscall.SIGINT) // ffmpeg finalises the file on SIGINT
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if syscall.Kill(c.PID, 0) != nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if syscall.Kill(c.PID, 0) == nil {
			_ = syscall.Kill(c.PID, syscall.SIGKILL)
		}
	}
	_ = os.Remove(path)
}

// processStart returns the process start time as printed by ps ("" if unknown).
func processStart(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
