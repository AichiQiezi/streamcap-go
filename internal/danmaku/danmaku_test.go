package danmaku

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

var t0 = time.Date(2026, 10, 19, 20, 0, 0, 0, time.Local)

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

func TestWriteRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.danmaku.jsonl")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(platform.Danmaku{At: at(2), User: "b", Text: "second", Color: 0xFF0000, Mode: platform.DanmakuTop})
	w.Write(platform.Danmaku{At: at(1), User: "a", Text: "first", Color: 0xFFFFFF, Mode: platform.DanmakuScroll})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || len(got) != 2 {
		t.Fatalf("read %v, %v", got, err)
	}
	if got[0].Text != "first" || got[1].Color != 0xFF0000 || got[1].Mode != platform.DanmakuTop || !got[0].At.Equal(at(1)) {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestPlaceAcrossPartsDropsTheGap(t *testing.T) {
	// Part 1 recorded 0–10s, then a reconnect gap, part 2 recorded 15–25s. Merged, the video is
	// 20s long and its second half starts at the first frame of part 2.
	windows := []Window{{Start: at(0), Dur: 10 * time.Second}, {Start: at(15), Dur: 10 * time.Second}}
	items := []platform.Danmaku{
		{At: at(-1), Text: "before recording"},
		{At: at(3), Text: "part 1"},
		{At: at(12), Text: "in the gap"},
		{At: at(16), Text: "part 2"},
		{At: at(30), Text: "after"},
	}
	cues := Place(items, windows)
	if len(cues) != 2 {
		t.Fatalf("cues = %+v, want the two inside parts", cues)
	}
	if cues[0].Text != "part 1" || cues[0].At != 3*time.Second {
		t.Errorf("part 1 at %v", cues[0].At)
	}
	if cues[1].Text != "part 2" || cues[1].At != 11*time.Second { // 10s of part 1, then 1s into part 2
		t.Errorf("part 2 at %v, want 11s", cues[1].At)
	}
}

func TestWriteASS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ass")
	cues := []Cue{
		{At: time.Second, Danmaku: platform.Danmaku{Text: "同时一", Mode: platform.DanmakuScroll, Color: 0xFFFFFF}},
		{At: time.Second, Danmaku: platform.Danmaku{Text: "同时二", Mode: platform.DanmakuScroll, Color: 0xFF8800}},
		{At: 2 * time.Second, Danmaku: platform.Danmaku{Text: `{\pos(0,0)}注入`, Mode: platform.DanmakuTop, Color: 0xFFFFFF}},
		{At: 3725*time.Second + 470*time.Millisecond, Danmaku: platform.Danmaku{Text: "late", Mode: platform.DanmakuBottom, Color: 0xFFFFFF}},
	}
	n, err := WriteASS(path, cues, 1920, 1080)
	if err != nil || n != 4 {
		t.Fatalf("placed %d, %v", n, err)
	}
	raw, _ := os.ReadFile(path)
	ass := string(raw)
	var events []string
	for _, l := range strings.Split(ass, "\n") {
		if strings.HasPrefix(l, "Dialogue:") {
			events = append(events, l)
		}
	}
	if !strings.Contains(ass, "PlayResX: 1920") || len(events) != 4 {
		t.Fatalf("ass:\n%s", ass)
	}
	// Two comments at the same moment go to different lanes (different y).
	if lane := func(e string) string { return e[strings.Index(e, `\move(1920,`)+11:][:4] }; lane(events[0]) == lane(events[1]) {
		t.Errorf("simultaneous comments share a lane:\n%s\n%s", events[0], events[1])
	}
	if !strings.Contains(events[1], `\c&H0088FF&`) { // 0xRRGGBB -> &HBBGGRR&
		t.Errorf("colour not converted: %s", events[1])
	}
	if strings.Contains(events[2], `}{\pos`) || !strings.Contains(events[2], "｛＼pos(0,0)｝注入") {
		t.Errorf("markup in the text was not neutralised: %s", events[2])
	}
	if !strings.Contains(events[3], "1:02:05.47,1:02:09.47") || !strings.Contains(events[3], `\an2`) {
		t.Errorf("bottom comment timing/alignment: %s", events[3])
	}
}

func TestWriteASSDropsWhatWouldOverlap(t *testing.T) {
	var cues []Cue
	for i := 0; i < 200; i++ { // a burst far beyond what fits on screen
		cues = append(cues, Cue{At: time.Second, Danmaku: platform.Danmaku{Text: "刷屏刷屏刷屏", Mode: platform.DanmakuScroll}})
	}
	n, err := WriteASS(filepath.Join(t.TempDir(), "v.ass"), cues, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || n >= 200 {
		t.Fatalf("placed %d of 200 simultaneous comments; want only as many as there are lanes", n)
	}
}

func TestLayoutKeepsPortraitFacesClear(t *testing.T) {
	for _, c := range []struct {
		w, h             int
		font, lanes, fix int
	}{
		{1920, 1080, 48, 10, 5}, // landscape: unchanged from before
		{1280, 720, 32, 10, 5},
		{540, 960, 20, 8, 2}, // portrait: smaller font, capped to the top band
		{720, 1280, 27, 8, 2},
	} {
		font, lanes, fix := layout(c.w, c.h)
		if font != c.font || lanes != c.lanes || fix != c.fix {
			t.Errorf("layout(%d×%d) = font %d, %d lanes, %d fixed; want %d, %d, %d", c.w, c.h, font, lanes, fix, c.font, c.lanes, c.fix)
		}
		if c.h > c.w && float64(lanes)*float64(font)*1.25 > float64(c.h)*0.3 {
			t.Errorf("portrait %d×%d: lanes reach %.0f%% of the height", c.w, c.h, float64(lanes)*float64(font)*125/float64(c.h))
		}
	}
}

func TestWriterFlushesOnATimer(t *testing.T) {
	old := FlushEvery
	FlushEvery = 20 * time.Millisecond
	t.Cleanup(func() { FlushEvery = old })
	path := filepath.Join(t.TempDir(), "q.danmaku.jsonl")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Write(platform.Danmaku{At: at(1), Text: "a quiet room's only comment"})
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if got, _ := Read(path); len(got) == 1 {
			return // reached the file without waiting for more comments or Close
		}
	}
	t.Fatal("a single comment never reached the file before Close")
}
