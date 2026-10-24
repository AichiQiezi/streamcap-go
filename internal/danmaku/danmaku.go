// Package danmaku keeps a live's comments next to its recording and turns them into an .ass
// subtitle file with the same name as the video, which players (IINA, mpv, PotPlayer, VLC) load
// automatically and can switch on and off.
//
// While recording, comments are appended to <base>.danmaku.jsonl with the local time they arrived.
// After the 场 is finalised, each output video's timeline is rebuilt from the wall-clock windows of
// the recorded files (Window), so comments land where the video shows them even when the live was
// recorded in several parts and merged: comments that came in during a gap between parts are
// dropped, because the merged video has no frames for that time.
package danmaku

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// line is one comment in the .jsonl file.
type line struct {
	T     int64  `json:"t"` // unix milliseconds
	User  string `json:"u,omitempty"`
	Text  string `json:"x"`
	Color uint32 `json:"c,omitempty"`
	Mode  int    `json:"m,omitempty"`
}

// FlushEvery is how often buffered comments reach the file: often enough that the file visibly
// grows during a quiet live and a crash loses little, rarely enough to stay cheap on a busy one.
var FlushEvery = 10 * time.Second

// Writer appends comments to a .jsonl file. Safe for concurrent use.
type Writer struct {
	mu    sync.Mutex
	f     *os.File
	w     *bufio.Writer
	n     int
	dirty bool
	stop  chan struct{}
}

// Create opens path for appending (a resumed session keeps writing the same file).
func Create(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	w := &Writer{f: f, w: bufio.NewWriter(f), stop: make(chan struct{})}
	go w.flusher()
	return w, nil
}

// flusher writes buffered comments out every FlushEvery; a lone comment in a quiet room would
// otherwise sit in the buffer until the next one, or until the session ends.
func (w *Writer) flusher() {
	t := time.NewTicker(FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.mu.Lock()
			if w.w != nil && w.dirty {
				_ = w.w.Flush()
				w.dirty = false
			}
			w.mu.Unlock()
		}
	}
}

// Write records one comment.
func (w *Writer) Write(d platform.Danmaku) {
	b, _ := json.Marshal(line{T: d.At.UnixMilli(), User: d.User, Text: d.Text, Color: d.Color, Mode: d.Mode})
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.w == nil {
		return
	}
	_, _ = w.w.Write(append(b, '\n'))
	w.n++
	w.dirty = true
}

// Count is how many comments were written.
func (w *Writer) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// Close flushes and closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.w == nil {
		return nil
	}
	close(w.stop)
	err := w.w.Flush()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.w = nil
	return err
}

// Read loads a .jsonl file, sorted by time; malformed lines are skipped.
func Read(path string) ([]platform.Danmaku, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []platform.Danmaku
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Text == "" {
			continue
		}
		mode := l.Mode
		if mode == 0 {
			mode = platform.DanmakuScroll
		}
		color := l.Color
		if color == 0 {
			color = 0xFFFFFF
		}
		out = append(out, platform.Danmaku{At: time.UnixMilli(l.T), User: l.User, Text: l.Text, Color: color, Mode: mode})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, sc.Err()
}

// Window is the stretch of wall-clock time one recorded file covers.
type Window struct {
	Start time.Time
	Dur   time.Duration
}

// Cue is a comment placed on a video's timeline.
type Cue struct {
	At time.Duration
	platform.Danmaku
}

// Place maps comments onto the timeline of a video made of windows played back to back.
// Comments outside every window are dropped.
func Place(items []platform.Danmaku, windows []Window) []Cue {
	var cues []Cue
	var offset time.Duration
	for _, w := range windows {
		end := w.Start.Add(w.Dur)
		for _, d := range items {
			if !d.At.Before(w.Start) && d.At.Before(end) {
				cues = append(cues, Cue{At: offset + d.At.Sub(w.Start), Danmaku: d})
			}
		}
		offset += w.Dur
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].At < cues[j].At })
	return cues
}

// Layout tunables.
const (
	scrollDur = 8 * time.Second // time a scrolling comment takes to cross the screen
	fixedDur  = 4 * time.Second // time a top/bottom comment stays
	gapPx     = 24              // minimum horizontal gap between comments in a lane
)

// WriteASS writes cues as an .ass file for a w×h video and returns how many fitted on screen
// (the rest would have overlapped; bilibili's player drops those too).
func WriteASS(path string, cues []Cue, w, h int) (int, error) {
	if w <= 0 || h <= 0 {
		w, h = 1920, 1080
	}
	font, scrollLanes, fixedLanes := layout(w, h)
	lineH := float64(font) * 1.25

	type lane struct {
		start time.Duration
		width float64
	}
	scroll := make([]*lane, scrollLanes)
	top := make([]time.Duration, fixedLanes)
	bottom := make([]time.Duration, fixedLanes)
	for i := range top {
		top[i], bottom[i] = -fixedDur, -fixedDur
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Script Info]\nTitle: StreamCap danmaku\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\nWrapStyle: 2\nScaledBorderAndShadow: yes\n\n", w, h)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Danmaku,PingFang SC,%d,&H26FFFFFF,&H26FFFFFF,&H26000000,&H00000000,1,0,0,0,100,100,0,0,1,%.1f,0,7,0,0,0,1\n\n", font, max(float64(font)/22, 1))
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	placed := 0
	W := float64(w)
	for _, c := range cues {
		text := assText(c.Text)
		width := textWidth(c.Text, font)
		color := ""
		if c.Color != 0xFFFFFF && c.Color != 0 {
			color = fmt.Sprintf(`\c&H%02X%02X%02X&`, c.Color&0xFF, c.Color>>8&0xFF, c.Color>>16)
		}
		switch c.Mode {
		case platform.DanmakuTop, platform.DanmakuBottom:
			lanes, an := top, 8
			if c.Mode == platform.DanmakuBottom {
				lanes, an = bottom, 2
			}
			i := freeFixed(lanes, c.At)
			if i < 0 {
				continue
			}
			lanes[i] = c.At
			margin := float64(font) * 0.3 // keep clear of the picture's edge
			y := margin + lineH*float64(i)
			if c.Mode == platform.DanmakuBottom {
				y = float64(h) - margin - lineH*float64(i)
			}
			fmt.Fprintf(&b, "Dialogue: 1,%s,%s,Danmaku,,0,0,0,,{\\an%d\\pos(%d,%d)%s}%s\n",
				assTime(c.At), assTime(c.At+fixedDur), an, w/2, int(y), color, text)
		default:
			// A lane takes a new comment once the previous one has fully entered (plus a gap) and
			// the new one, moving at its own speed, can't catch up before the previous one leaves.
			i := -1
			for k, l := range scroll {
				if l == nil {
					i = k
					break
				}
				vPrev := (W + l.width) / scrollDur.Seconds()
				vNew := (W + width) / scrollDur.Seconds()
				entered := l.start + time.Duration((l.width+gapPx)/vPrev*float64(time.Second))
				noCatch := c.At+time.Duration(W/vNew*float64(time.Second)) >= l.start+scrollDur
				if c.At >= entered && noCatch {
					i = k
					break
				}
			}
			if i < 0 {
				continue
			}
			scroll[i] = &lane{start: c.At, width: width}
			y := int(float64(font)*0.3 + lineH*float64(i))
			fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Danmaku,,0,0,0,,{\\move(%d,%d,%d,%d)%s}%s\n",
				assTime(c.At), assTime(c.At+scrollDur), w, y, -int(width), y, color, text)
		}
		placed++
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return 0, err
	}
	return placed, os.Rename(tmp, path)
}

// layout sizes the comments for a w×h video. Landscape: lanes over the top 60%, the way bilibili's
// player does it. Portrait (phone streams, mostly douyin) puts a face in the upper middle and the
// width is narrow, so comments get a smaller font scaled to the width and only the top band, at
// most 8 lanes; the rest are dropped as a crowded screen would anyway.
func layout(w, h int) (font, scrollLanes, fixedLanes int) {
	if h > w {
		font = max(int(float64(w)*0.038), 14)
		lineH := float64(font) * 1.25
		return font, min(max(int(float64(h)*0.28/lineH), 1), 8), 2
	}
	font = max(int(float64(h)*0.045), 18)
	lineH := float64(font) * 1.25
	return font, max(int(float64(h)*0.6/lineH), 1), max(int(float64(h)*0.3/lineH), 1)
}

func freeFixed(lanes []time.Duration, at time.Duration) int {
	for i, busySince := range lanes {
		if at >= busySince+fixedDur {
			return i
		}
	}
	return -1
}

// textWidth estimates the rendered width: CJK and emoji are about one em, Latin about half.
func textWidth(s string, font int) float64 {
	var w float64
	for _, r := range s {
		if r < 0x2E80 {
			w += 0.55
		} else {
			w++
		}
	}
	return w * float64(font)
}

// assText neutralises what .ass would read as markup: {…} override blocks and \N-style escapes.
func assText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer(`\`, `＼`, "{", "｛", "}", "｝").Replace(s)
}

func assTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	cs := d.Milliseconds() / 10
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}
