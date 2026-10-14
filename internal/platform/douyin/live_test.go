package douyin

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// Live integration test against the real douyin APIs. Run with:
//
//	STREAMCAP_LIVE_TEST=1 go test -v -run Live ./internal/platform/douyin/
//
// STREAMCAP_DOUYIN_ROOMS (space separated URLs) overrides the candidate room list.
var liveCandidates = []string{
	"https://live.douyin.com/898334614374",
	"https://live.douyin.com/18239354974",
	"https://live.douyin.com/921169302662",
	"https://live.douyin.com/898691998235",
	"https://live.douyin.com/278228793217",
	"https://live.douyin.com/648819501004",
	"https://live.douyin.com/80017709309",
}

func skipUnlessLive(t *testing.T) {
	if os.Getenv("STREAMCAP_LIVE_TEST") != "1" {
		t.Skip("set STREAMCAP_LIVE_TEST=1 to run live douyin tests")
	}
}

func TestLiveResolve(t *testing.T) {
	skipUnlessLive(t)
	rooms := liveCandidates
	if env := os.Getenv("STREAMCAP_DOUYIN_ROOMS"); env != "" {
		rooms = strings.Fields(env)
	}
	r := resolver{}
	var live *platform.StreamInfo
	for _, u := range rooms {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		info, err := r.Resolve(ctx, u, platform.Options{Quality: platform.QualityOD})
		cancel()
		if err != nil {
			t.Errorf("%s: %v", u, err)
			continue
		}
		t.Logf("%s live=%v anchor=%q title=%q quality=%s codec=%s expires=%s\n  record=%s",
			u, info.IsLive, info.Anchor, info.Title, info.Quality, info.Codec, info.ExpiresAt.Format(time.RFC3339), info.RecordURL)
		if info.IsLive && live == nil {
			live = info
		}
	}
	if live == nil {
		t.Fatal("no candidate room is live; pass STREAMCAP_DOUYIN_ROOMS")
	}

	// Same room at every quality, plus ffprobe of OD and SD.
	for _, q := range platform.Qualities {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		info, err := r.Resolve(ctx, live.RoomURL, platform.Options{Quality: q})
		cancel()
		if err != nil {
			t.Fatalf("%s %s: %v", live.RoomURL, q, err)
		}
		if !info.IsLive || info.RecordURL == "" {
			t.Fatalf("%s %s: not live / no url: %+v", live.RoomURL, q, info)
		}
		line := ""
		if q == platform.QualityOD || q == platform.QualitySD {
			line = ffprobe(t, info.RecordURL)
		}
		t.Logf("requested=%s selected=%s codec=%s flv=%v hls=%v ffprobe=%q", q, info.Quality, info.Codec,
			info.RecordURL == info.FlvURL, info.RecordURL == info.HlsURL, line)
	}

	// PreferHLS returns the m3u8 URL, which must also stream.
	info, err := r.Resolve(context.Background(), live.RoomURL, platform.Options{Quality: platform.QualitySD, PreferHLS: true})
	if err != nil || info.RecordURL != info.HlsURL || info.HlsURL == "" {
		t.Fatalf("PreferHLS: %+v %v", info, err)
	}
	t.Logf("SD HLS ffprobe=%q", ffprobe(t, info.RecordURL))
}

func ffprobe(t *testing.T, u string) string {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Log("ffprobe not found, skipping stream check")
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_name,width,height",
		"-of", "compact", "-rw_timeout", "10000000", u).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil || !strings.Contains(s, "width=") {
		t.Errorf("ffprobe %s failed: %v: %s", u, err, s)
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

func TestLiveOtherURLShapes(t *testing.T) {
	skipUnlessLive(t)
	r := resolver{}
	cases := []struct {
		url      string
		wantLive *bool
	}{
		// Ended room (status 4) → IsLive=false, nil error.
		{"https://live.douyin.com/745964462470", ptr(false)},
		{"https://www.douyin.com/root/live/898334614374", nil},
		{"https://live.douyin.com/898334614374?anchor_id=1&from_search=true", nil},
		// User pages (sec_uid of the 898334614374 anchor and of the ended room's anchor).
		{"https://www.douyin.com/user/MS4wLjABAAAAvzaICSahsT1tZZEEnFB0DgEa7V0SBoD_Cr9UZAY5NmA", nil},
		{"https://www.douyin.com/user/MS4wLjABAAAAfJdQBvOV3r8BEC7SSnDGmkUIJ_I3mggDO2TeeRUmBSqRVRjhTdzkiGtEIqyLLONV", ptr(false)},
	}
	if s := os.Getenv("STREAMCAP_DOUYIN_SHORT"); s != "" {
		cases = append(cases, struct {
			url      string
			wantLive *bool
		}{s, nil})
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		info, err := r.Resolve(ctx, c.url, platform.Options{})
		cancel()
		if err != nil {
			t.Errorf("%s: %v", c.url, err)
			continue
		}
		t.Logf("%s → room=%s live=%v anchor=%q quality=%s record=%.90s", c.url, info.RoomID, info.IsLive, info.Anchor, info.Quality, info.RecordURL)
		if c.wantLive != nil && info.IsLive != *c.wantLive {
			t.Errorf("%s: IsLive=%v want %v", c.url, info.IsLive, *c.wantLive)
		}
		if info.Anchor == "" {
			t.Errorf("%s: empty anchor", c.url)
		}
	}

	// Reflow (app) API with a known room_id + sec_uid, the path a live-room share link takes.
	c := &client{http: platform.HTTPClient("")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	web, err := c.fetchWeb(ctx, "898334614374")
	if err == nil && web.room.Status == statusLive {
		rd, err := c.fetchApp(ctx, web.room.IDStr, "MS4wLjABAAAAvzaICSahsT1tZZEEnFB0DgEa7V0SBoD_Cr9UZAY5NmA")
		if err != nil {
			t.Errorf("reflow api: %v", err)
		} else {
			info, err := c.toStreamInfo(ctx, "reflow", rd)
			if err != nil || !info.IsLive {
				t.Errorf("reflow api: %+v %v", info, err)
			} else {
				t.Logf("reflow api → web_rid=%s anchor=%q quality=%s record=%.90s", info.RoomID, info.Anchor, info.Quality, info.RecordURL)
			}
		}
	}

	// Error path: an already-expired context must be an error, never IsLive=false.
	dctx, dcancel := context.WithDeadline(context.Background(), time.Now())
	defer dcancel()
	if info, err := r.Resolve(dctx, liveCandidates[0], platform.Options{}); err == nil {
		t.Errorf("expired ctx: want error, got %+v", info)
	} else {
		t.Logf("expired ctx → error: %v", err)
	}
	// Non-existent room → error (state unknown), not offline.
	if info, err := r.Resolve(context.Background(), "https://live.douyin.com/999999999999999", platform.Options{}); err == nil {
		t.Errorf("nonexistent room: want error, got %+v", info)
	} else {
		t.Logf("nonexistent room → error: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
