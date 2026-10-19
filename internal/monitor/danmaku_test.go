package monitor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/danmaku"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

func TestWriteDanmakuASSMergedAndPerFile(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now().Truncate(time.Second)
	jsonl := filepath.Join(dir, "s.danmaku.jsonl")
	w, _ := danmaku.Create(jsonl)
	w.Write(platform.Danmaku{At: t0.Add(2 * time.Second), Text: "in part 1"})
	w.Write(platform.Danmaku{At: t0.Add(32 * time.Second), Text: "in part 2"})
	w.Close()
	windows := [][]danmaku.Window{
		{{Start: t0, Dur: 20 * time.Second}},
		{{Start: t0.Add(30 * time.Second), Dur: 20 * time.Second}},
	}

	merged := filepath.Join(dir, "s.mp4")
	if n := writeDanmakuASS(jsonl, windows, []string{merged}, 1280, 720); n != 2 {
		t.Fatalf("merged: placed %d, want 2", n)
	}
	ass, _ := os.ReadFile(filepath.Join(dir, "s.ass"))
	if !strings.Contains(string(ass), "0:00:22.00") { // part 2's comment, 2s into part 2 = 22s into the merged video
		t.Fatalf("merged timing wrong:\n%s", ass)
	}

	one, two := filepath.Join(dir, "a.mp4"), filepath.Join(dir, "b.mp4")
	if n := writeDanmakuASS(jsonl, windows, []string{one, two}, 1280, 720); n != 2 {
		t.Fatalf("per file: placed %d, want 2", n)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "b.ass"))
	if !strings.Contains(string(b), "0:00:02.00") || strings.Contains(string(b), "in part 1") {
		t.Fatalf("b.ass should hold only part 2's comment at 2s:\n%s", b)
	}
}

func TestFileWindowsAnchorOnTheLiveEdge(t *testing.T) {
	durs := map[string]time.Duration{"a.ts": 122 * time.Second, "s1.ts": 10 * time.Second, "s2.ts": 5 * time.Second}
	old := probeDuration
	probeDuration = func(_ context.Context, f string) (time.Duration, error) { return durs[f], nil }
	t.Cleanup(func() { probeDuration = old })

	t0 := time.Date(2026, 10, 19, 10, 10, 30, 0, time.Local)
	ws := fileWindows(context.Background(), []partFiles{
		// Data first arrived 3s after launch, but the CDN's burst means the 122s video ends at the live edge.
		{files: []string{"a.ts"}, dataAt: t0.Add(3 * time.Second), endAt: t0.Add(120 * time.Second)},
		// A segmented part: segments follow each other back from its end.
		{files: []string{"s1.ts", "s2.ts"}, dataAt: t0.Add(200 * time.Second), endAt: t0.Add(215 * time.Second)},
	})
	if got, want := ws[0][0].Start, t0.Add(-2*time.Second); !got.Equal(want) {
		t.Errorf("part 1 starts %v, want %v (end minus duration)", got.Format("15:04:05"), want.Format("15:04:05"))
	}
	if !ws[1][0].Start.Equal(t0.Add(200*time.Second)) || !ws[1][1].Start.Equal(t0.Add(210*time.Second)) {
		t.Errorf("segments: %v / %v", ws[1][0].Start.Format("15:04:05"), ws[1][1].Start.Format("15:04:05"))
	}
}
