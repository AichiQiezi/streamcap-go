package recorder

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reason says why a part ended.
type Reason string

const (
	ReasonStopped Reason = "stopped" // we asked it to stop (user / monitor off / shutdown)
	ReasonHandoff Reason = "handoff" // stopped on purpose after a fresh-URL part took over
	ReasonEOF     Reason = "eof"     // the CDN closed the stream cleanly (expired URL, stream ended, …)
	ReasonStalled Reason = "stalled" // nothing was written for StallTimeout
	ReasonError   Reason = "error"   // ffmpeg failed
)

// StallTimeout is how long a part may write nothing before it is considered dead.
var StallTimeout = 45 * time.Second

const sampleEvery = 2 * time.Second

// Job describes one ffmpeg run.
type Job struct {
	URL        string
	Headers    map[string]string
	Proxy      string
	Format     string // TS MP4 FLV MKV MOV / MP3 M4A AAC WAV
	Segment    bool
	SegmentSec int
	BitrateK   int
	Dir        string // output directory
	BaseName   string // file name without extension (unique per part)
}

// Progress is a live snapshot of a running part.
type Progress struct {
	File     string  // newest output file
	Bytes    int64   // total bytes written by this part
	SpeedBps float64 // smoothed write speed
}

// Result is the outcome of a finished part.
type Result struct {
	Reason   Reason
	ExitCode int
	Detail   string   // tail of ffmpeg's stderr, or our own explanation
	Files    []string // non-empty files produced
	Bytes    int64
	Started  time.Time
	Ended    time.Time
}

// Part is one running ffmpeg process.
type Part struct {
	job     *Job
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *tailBuffer
	started time.Time
	pattern string // output path (may contain %03d for segments)

	stopOnce sync.Once
	stopReq  chan Reason
	done     chan struct{}

	mu       sync.Mutex
	progress Progress
	result   Result
}

// Start launches ffmpeg for job. onProgress (optional) is called from the part's goroutine.
func Start(job *Job, onProgress func(Progress)) (*Part, error) {
	ffmpeg, _ := Binaries()
	if ffmpeg == "" {
		return nil, errors.New("找不到 ffmpeg，请安装 ffmpeg 或把 ffmpeg 放到程序同目录")
	}
	if err := os.MkdirAll(job.Dir, 0o755); err != nil {
		return nil, err
	}
	ext := Ext(job.Format)
	pattern := filepath.Join(job.Dir, job.BaseName+"."+ext)
	if job.Segment && job.SegmentSec > 0 {
		pattern = filepath.Join(job.Dir, job.BaseName+"_%03d."+ext)
	}
	cmd := exec.Command(ffmpeg, buildArgs(job, pattern)...)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{max: 4096}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 ffmpeg 失败: %w", err)
	}
	guardChild(cmd, pattern)
	p := &Part{
		job: job, cmd: cmd, stdin: stdin, stderr: tail, started: time.Now(), pattern: pattern,
		stopReq: make(chan Reason, 1), done: make(chan struct{}),
	}
	go p.run(onProgress)
	return p, nil
}

// Stop asks ffmpeg to finish gracefully ("q"), killing it if it doesn't exit in time. Non-blocking.
func (p *Part) Stop(reason Reason) {
	p.stopOnce.Do(func() { p.stopReq <- reason })
}

// Done is closed when the part has fully ended.
func (p *Part) Done() <-chan struct{} { return p.done }

// Wait blocks until the part ends and returns its result.
func (p *Part) Wait() Result {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

// Progress returns the latest snapshot.
func (p *Part) Progress() Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.progress
}

// Started is when ffmpeg was launched.
func (p *Part) Started() time.Time { return p.started }

func (p *Part) run(onProgress func(Progress)) {
	defer close(p.done)
	exited := make(chan error, 1)
	go func() {
		err := p.cmd.Wait()
		releaseChild(p.cmd)
		exited <- err
	}()

	ticker := time.NewTicker(sampleEvery)
	defer ticker.Stop()
	var (
		lastBytes  int64 = -1
		lastSample       = time.Now()
		lastGrowth       = time.Now()
		speed      float64
		reason     Reason
		waitErr    error
		finished   bool
	)

	for !finished {
		select {
		case waitErr = <-exited:
			finished = true
		case r := <-p.stopReq:
			reason = r
			waitErr = p.gracefulStop(exited)
			finished = true
		case now := <-ticker.C:
			file, total := p.measure()
			if total != lastBytes {
				if lastBytes >= 0 {
					inst := float64(total-lastBytes) / now.Sub(lastSample).Seconds()
					if speed == 0 {
						speed = inst
					} else {
						speed = speed*0.6 + inst*0.4
					}
				}
				lastBytes, lastGrowth = total, now
			} else {
				speed *= 0.5
			}
			lastSample = now
			p.mu.Lock()
			p.progress = Progress{File: file, Bytes: max64(total, 0), SpeedBps: speed}
			snap := p.progress
			p.mu.Unlock()
			if onProgress != nil {
				onProgress(snap)
			}
			if now.Sub(lastGrowth) >= StallTimeout {
				slog.Warn("no data written, stopping stalled part", "file", p.pattern, "for", StallTimeout)
				reason = ReasonStalled
				waitErr = p.gracefulStop(exited)
				finished = true
			}
		}
	}

	files, total := p.files()
	res := Result{Started: p.started, Ended: time.Now(), Files: files, Bytes: total}
	if p.cmd.ProcessState != nil {
		res.ExitCode = p.cmd.ProcessState.ExitCode()
	}
	switch {
	case reason != "":
		res.Reason = reason
	case waitErr == nil:
		res.Reason = ReasonEOF
	default:
		res.Reason = ReasonError
	}
	res.Detail = strings.TrimSpace(p.stderr.String())
	if res.Reason == ReasonStalled {
		res.Detail = fmt.Sprintf("%v 内没有收到任何数据", StallTimeout)
	}
	p.mu.Lock()
	p.result = res
	p.progress.SpeedBps = 0
	p.mu.Unlock()
}

// gracefulStop sends "q" and waits; ffmpeg then finalises the container. Kill after 10s.
func (p *Part) gracefulStop(exited <-chan error) error {
	_, _ = p.stdin.Write([]byte("q"))
	_ = p.stdin.Close()
	select {
	case err := <-exited:
		return err
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		return <-exited
	}
}

// measure returns the newest output file and the total bytes written so far.
func (p *Part) measure() (string, int64) {
	files, total := p.files()
	if len(files) == 0 {
		return "", total
	}
	return files[len(files)-1], total
}

// files lists the part's output files (segments in order) and their total size.
func (p *Part) files() ([]string, int64) {
	if !strings.Contains(p.pattern, "%03d") {
		st, err := os.Stat(p.pattern)
		if err != nil || st.Size() == 0 {
			return nil, 0
		}
		return []string{p.pattern}, st.Size()
	}
	dir := filepath.Dir(p.pattern)
	prefix := strings.SplitN(filepath.Base(p.pattern), "%03d", 2)[0]
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	var out []string
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
		total += info.Size()
	}
	return out, total // ReadDir is sorted by name, so segments are in order
}

// tailBuffer keeps the last max bytes written to it (ffmpeg stderr).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(b)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(t.buf.String()), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}

func itoa(n int) string { return strconv.Itoa(n) }

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
