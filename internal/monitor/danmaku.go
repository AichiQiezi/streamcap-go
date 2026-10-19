package monitor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/danmaku"
	"github.com/AichiQiezi/streamcap-go/internal/media"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

// dmRecorder follows a live's comments for the length of one recording session.
type dmRecorder struct {
	path   string
	w      *danmaku.Writer
	cancel context.CancelFunc
	done   chan struct{}
}

// partFiles is what one recorder part produced, and when its data arrived on the local clock.
type partFiles struct {
	files  []string
	dataAt time.Time // first data written
	endAt  time.Time // last data written
}

// probeDuration measures a file (tests replace it).
var probeDuration = media.Duration

// startDanmaku begins recording comments for the session when the site offers them and the user
// wants them (not for audio-only formats: there is no picture to show them on).
func (rt *runtime) startDanmaku(room store.Room, s store.Settings, dir, base string) *dmRecorder {
	if !s.Bool("record_danmaku") || store.AudioFormats[strings.ToUpper(room.Format)] {
		return nil
	}
	src, ok := platform.ByKey(room.Platform).(platform.DanmakuSource)
	if !ok {
		return nil
	}
	if r, ok := src.(platform.DanmakuReady); ok && !r.DanmakuReady() {
		slog.Info("danmaku: not available in this run mode", "room", room.URL)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("danmaku: no folder", "dir", dir, "err", err)
		return nil
	}
	path := filepath.Join(dir, base+".danmaku.jsonl")
	w, err := danmaku.Create(path)
	if err != nil {
		slog.Warn("danmaku: cannot write", "file", path, "err", err)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &dmRecorder{path: path, w: w, cancel: cancel, done: make(chan struct{})}
	opt := platform.Options{Cookie: rt.m.Cookies()[room.Platform], Proxy: rt.m.proxyFor(room.Platform, s)}
	go func() {
		defer close(d.done)
		if err := src.Danmaku(ctx, room.URL, opt, w.Write); err != nil {
			slog.Warn("danmaku: stopped", "room", room.URL, "err", err)
		}
	}()
	slog.Info("danmaku: recording", "room", room.URL, "file", path)
	return d
}

// stop ends the comment stream and closes the file.
func (d *dmRecorder) stop() {
	d.cancel()
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
	}
	_ = d.w.Close()
}

// fileWindows measures each recorded file and lays them out on the local clock, in recording order.
//
// A part's video is anchored at its end, not its start: on connecting, the CDN sends a burst of
// buffered footage (seconds of it), so the first frame is older than the moment data first arrived,
// while the last frame is the live edge the viewers were reacting to.
func fileWindows(ctx context.Context, parts []partFiles) [][]danmaku.Window {
	out := make([][]danmaku.Window, 0, len(parts))
	for _, p := range parts {
		durs := make([]time.Duration, len(p.files))
		var total time.Duration
		for i, f := range p.files {
			d, err := probeDuration(ctx, f)
			if err != nil {
				slog.Warn("danmaku: no duration, its comments are skipped", "file", f, "err", err)
				d = 0
			}
			durs[i], total = d, total+d
		}
		start := p.dataAt
		if !p.endAt.IsZero() {
			start = p.endAt.Add(-total)
		}
		ws := make([]danmaku.Window, 0, len(p.files))
		for _, d := range durs {
			ws = append(ws, danmaku.Window{Start: start, Dur: d})
			start = start.Add(d)
		}
		out = append(out, ws)
	}
	return out
}

// writeDanmakuASS writes <video>.ass for each finalised output and returns how many comments made
// it on screen. windows come from fileWindows, measured before finalising (merging may delete the
// parts). A merged output plays every file back to back; otherwise outputs map one to one.
func writeDanmakuASS(jsonl string, windows [][]danmaku.Window, outputs []string, w, h int) int {
	items, err := danmaku.Read(jsonl)
	if err != nil || len(items) == 0 {
		return 0
	}
	var flat []danmaku.Window
	for _, ws := range windows {
		flat = append(flat, ws...)
	}
	var plan [][]danmaku.Window
	switch {
	case len(outputs) == 1:
		plan = [][]danmaku.Window{flat}
	case len(outputs) == len(flat):
		for _, w := range flat {
			plan = append(plan, []danmaku.Window{w})
		}
	default:
		slog.Warn("danmaku: outputs don't match the recorded files, no .ass written", "outputs", len(outputs), "files", len(flat))
		return 0
	}
	total := 0
	for i, out := range outputs {
		ass := strings.TrimSuffix(out, filepath.Ext(out)) + ".ass"
		n, err := danmaku.WriteASS(ass, danmaku.Place(items, plan[i]), w, h)
		if err != nil {
			slog.Warn("danmaku: write .ass failed", "file", ass, "err", err)
			continue
		}
		total += n
	}
	return total
}
