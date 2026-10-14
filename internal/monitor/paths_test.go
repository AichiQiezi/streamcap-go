package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

func TestAsciiNameNeverEmpty(t *testing.T) {
	cases := map[string]string{
		"张三":            "zhangsan",
		"玩机器丶Machine":   "wanjiqizhuMachine",
		"🌸🌸🌸":           "room_x", // emoji-only: falls back instead of yielding "" (crashed the Python version)
		"김민지":           "room_x",
		"  a/b:c  ":     "a_b_c",
	}
	for in, want := range cases {
		if got := asciiName(in, "room_x"); got != want {
			t.Errorf("asciiName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		`a<b>c:"d"/e\f|g?h*`: "a_b_c_d_e_f_g_h",
		"🌸":                 "fb",
		" .hidden. ":         "hidden",
		"正常名字":              "正常名字",
	}
	for in, want := range cases {
		if got := safeName(in, "fb"); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("长", 100)
	if n := len([]rune(safeName(long, "fb"))); n != 60 {
		t.Errorf("long name not truncated: %d runes", n)
	}
}

func TestSessionPaths(t *testing.T) {
	now := time.Date(2026, 10, 13, 21, 5, 9, 0, time.Local)
	room := store.Room{ID: "abcdef123456", Platform: "douyin", Name: "主播🌸"}
	info := &platform.StreamInfo{Title: "标题"}
	s := store.Settings{"folder_name_author": true, "filename_includes_title": true}
	dir, base := sessionPaths("/rec", room, info, s, now)
	if dir != "/rec/主播" || base != "主播_2026-10-13_21-05-09_标题" {
		t.Errorf("got %q %q", dir, base)
	}
	s["ascii_save_path"] = true
	room.Name = "🌸"
	_, base = sessionPaths("/rec", room, info, s, now)
	if base != "room_abcdef12_2026-10-13_21-05-09_biaoti" {
		t.Errorf("ascii base = %q", base)
	}
}

func TestInScheduleAcrossMidnight(t *testing.T) {
	r := store.Room{Scheduled: true, ScheduleStart: "22:00", ScheduleHours: 4}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 13, h, m, 0, 0, time.Local) }
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(21, 59), false}, {at(22, 0), true}, {at(23, 30), true}, {at(1, 59), true}, {at(2, 0), false}, {at(12, 0), false}} {
		if got := r.InSchedule(c.t); got != c.want {
			t.Errorf("InSchedule(%s) = %v, want %v", c.t.Format("15:04"), got, c.want)
		}
	}
}
