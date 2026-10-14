package bilibili

// Integration tests against the real Bilibili API. Run with:
//
//	STREAMCAP_LIVE_TEST=1 go test -v -run Live ./internal/platform/bilibili/
//
// Optional: STREAMCAP_BILIBILI_COOKIE (logged-in cookie) and STREAMCAP_BILIBILI_ROOMS (comma
// separated room URLs). ffprobe on PATH enables the pull check.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

func liveGate(t *testing.T) {
	if os.Getenv("STREAMCAP_LIVE_TEST") != "1" {
		t.Skip("set STREAMCAP_LIVE_TEST=1 to run live Bilibili tests")
	}
}

// liveRooms returns a few currently-live rooms from the public area list.
func liveRooms(t *testing.T) []string {
	if env := os.Getenv("STREAMCAP_BILIBILI_ROOMS"); env != "" {
		return strings.Split(env, ",")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.live.bilibili.com/room/v1/Area/getListByAreaID?areaId=0&sort=online&pageSize=4&page=1", nil)
	req.Header.Set("User-Agent", desktopUA)
	resp, err := platform.HTTPClient("").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			RoomID int64 `json:"roomid"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range body.Data {
		out = append(out, fmt.Sprintf("https://live.bilibili.com/%d", r.RoomID))
	}
	return out
}

func ffprobe(t *testing.T, info *platform.StreamInfo, withHeaders bool) string {
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return "(ffprobe not installed)"
	}
	args := []string{"-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "compact", "-rw_timeout", "10000000"}
	if withHeaders {
		var h strings.Builder
		for k, v := range info.Headers {
			h.WriteString(k + ": " + v + "\r\n")
		}
		args = append(args, "-headers", h.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, append(args, info.RecordURL)...).CombinedOutput()
	var keep []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "stream|") || strings.Contains(l, "Server returned") {
			keep = append(keep, strings.TrimPrefix(l, "stream|"))
		}
	}
	return strings.Join(keep, " ; ")
}

func TestLiveResolveQualities(t *testing.T) {
	liveGate(t)
	r := &Resolver{}
	cookie := os.Getenv("STREAMCAP_BILIBILI_COOKIE")
	for _, room := range liveRooms(t) {
		for _, q := range []platform.Quality{platform.QualityOD, platform.QualityLD} {
			for _, hls := range []bool{false, true} {
				info, err := r.Resolve(context.Background(), room, platform.Options{Quality: q, Cookie: cookie, PreferHLS: hls})
				if err != nil {
					t.Errorf("%s %s: %v", room, q, err)
					continue
				}
				if !info.IsLive {
					t.Logf("%s: went offline", room)
					continue
				}
				if info.Anchor == "" || info.RecordURL == "" || info.Quality == "" {
					t.Errorf("%s %s: incomplete %+v", room, q, info)
				}
				kind := "FLV"
				if hls {
					kind = "HLS"
				}
				probe := ffprobe(t, info, true)
				if !strings.Contains(probe, "width=") && !strings.Contains(probe, "not installed") {
					t.Errorf("%s %s %s: ffprobe failed: %s (%s)", room, q, kind, probe, info.RecordURL)
				}
				host := strings.SplitN(strings.TrimPrefix(info.RecordURL, "https://"), "/", 2)[0]
				t.Logf("%s [%s] %q req=%s got=%s codec=%s %s host=%s flv=%v hls=%v expires_in=%s probe: %s",
					room, info.RoomID, info.Anchor, q, info.Quality, info.Codec, kind, host,
					info.FlvURL != "", info.HlsURL != "", time.Until(info.ExpiresAt).Round(time.Minute), probe)
			}
		}
	}
}

func TestLiveURLShapes(t *testing.T) {
	liveGate(t)
	r := &Resolver{}
	for _, u := range []string{
		"https://live.bilibili.com/6", // short id of 7734200
		"https://live.bilibili.com/h5/545068?broadcast_type=0",
		"https://live.bilibili.com/5050?spm_id_from=333.1007.0.0",
	} {
		info, err := r.Resolve(context.Background(), u, platform.Options{})
		if err != nil {
			t.Errorf("%s: %v", u, err)
			continue
		}
		t.Logf("%s -> room=%s live=%v anchor=%q title=%q quality=%s", u, info.RoomID, info.IsLive, info.Anchor, info.Title, info.Quality)
	}
	if _, err := r.Resolve(context.Background(), "https://live.bilibili.com/p/eden/area-tags", platform.Options{}); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("non-room page: %v", err)
	}
}

func TestLiveOfflineAndCancel(t *testing.T) {
	liveGate(t)
	r := &Resolver{}
	// Short id 3 (real 23058) has been offline; short id 1 (5440) is in 轮播 (live_status 2).
	for _, u := range []string{"https://live.bilibili.com/3", "https://live.bilibili.com/1"} {
		info, err := r.Resolve(context.Background(), u, platform.Options{})
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		t.Logf("%s -> room=%s live=%v anchor=%q title=%q", u, info.RoomID, info.IsLive, info.Anchor, info.Title)
		if info.IsLive {
			t.Errorf("%s expected offline (it may have started streaming)", u)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Resolve(ctx, "https://live.bilibili.com/5050", platform.Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled ctx: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel2()
	if _, err := r.Resolve(ctx2, "https://live.bilibili.com/5050", platform.Options{}); err == nil {
		t.Error("1ms deadline: want error")
	} else {
		t.Logf("1ms deadline -> %v", err)
	}
	if _, err := r.Resolve(context.Background(), "https://live.bilibili.com/99999999999", platform.Options{}); err == nil {
		t.Error("nonexistent room: want error")
	} else {
		t.Logf("nonexistent room -> %v", err)
	}
}

// TestLiveHeadersNeeded documents whether ffmpeg can pull RecordURL without the extra headers.
func TestLiveHeadersNeeded(t *testing.T) {
	liveGate(t)
	r := &Resolver{}
	for _, room := range liveRooms(t) {
		info, err := r.Resolve(context.Background(), room, platform.Options{})
		if err != nil || !info.IsLive {
			continue
		}
		t.Logf("%s host=%s\n  without headers: %s\n  with headers:    %s", room,
			strings.SplitN(strings.TrimPrefix(info.RecordURL, "https://"), "/", 2)[0],
			ffprobe(t, info, false), ffprobe(t, info, true))
	}
}
