package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeAPI serves canned responses per API path and records the requests it saw.
type fakeAPI struct {
	mu       sync.Mutex
	handlers map[string]func(q url.Values) (int, string)
	seen     []*http.Request
}

func (f *fakeAPI) start(t *testing.T) *Resolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Clone(context.Background()))
		h := f.handlers[r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		status, body := h(r.URL.Query())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Resolver{apiBase: srv.URL}
}

func (f *fakeAPI) requests(path string) []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*http.Request
	for _, r := range f.seen {
		if r.URL.Path == path {
			out = append(out, r)
		}
	}
	return out
}

const (
	pInit = "/room/v1/Room/room_init"
	pH5   = "/xlive/web-room/v1/index/getH5InfoByRoom"
	pPlay = "/xlive/web-room/v2/index/getRoomPlayInfo"
)

func static(body string) func(url.Values) (int, string) {
	return func(url.Values) (int, string) { return 200, body }
}

func liveAPI(t *testing.T) *fakeAPI {
	return &fakeAPI{handlers: map[string]func(url.Values) (int, string){
		pInit: static(fixture(t, "room_init_live_short.json")),
		pH5:   static(fixture(t, "h5_live.json")),
		pPlay: static(fixture(t, "play_live_multi.json")),
	}}
}

func TestParseRoomID(t *testing.T) {
	ok := map[string]string{
		"https://live.bilibili.com/7734200":                          "7734200",
		"https://live.bilibili.com/7734200?spm_id_from=333.1007.0.0": "7734200",
		"https://live.bilibili.com/6/":                               "6",
		"http://live.bilibili.com/545068#x":                          "545068",
		"live.bilibili.com/5050":                                     "5050",
		" https://LIVE.bilibili.com/h5/1735947155?a=b ":              "1735947155",
		"https://live.bilibili.com/blanc/3947892":                    "3947892",
	}
	for in, want := range ok {
		got, err := parseRoomID(in)
		if err != nil || got != want {
			t.Errorf("parseRoomID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"https://live.bilibili.com/",
		"https://live.bilibili.com/p/eden/area-tags",
		"https://live.bilibili.com/abc",
		"https://live.bilibili.com/0",
		"https://live.bilibili.com/h5/",
		"https://www.bilibili.com/video/BV1xx411c7mD",
		"https://space.bilibili.com/123",
		"https://example.com/123",
	} {
		if _, err := parseRoomID(in); !errors.Is(err, platform.ErrUnsupported) {
			t.Errorf("parseRoomID(%q) err = %v; want ErrUnsupported", in, err)
		}
	}
}

func TestMatchAndRegister(t *testing.T) {
	r := &Resolver{}
	for in, want := range map[string]bool{
		"https://live.bilibili.com/5050":   true,
		"live.bilibili.com/5050":           true,
		"https://www.bilibili.com/video/x": true,
		"https://live.douyin.com/123":      false,
		"https://notbilibili.com/1":        false,
		"https://bilibili.com.evil.com/1":  false,
	} {
		if got := r.Match(in); got != want {
			t.Errorf("Match(%q) = %v; want %v", in, got, want)
		}
	}
	if platform.ByKey("bilibili") == nil || platform.Find("https://live.bilibili.com/5050") == nil {
		t.Error("resolver not registered")
	}
	if r.Name() != "B站" {
		t.Error(r.Name())
	}
}

func TestResolveUnsupported(t *testing.T) {
	_, err := (&Resolver{}).Resolve(context.Background(), "https://www.bilibili.com/video/BV1", platform.Options{})
	if !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveLive(t *testing.T) {
	api := liveAPI(t)
	r := api.start(t)
	info, err := r.Resolve(context.Background(), "https://live.bilibili.com/6?x=1",
		platform.Options{Quality: platform.QualityOD, Cookie: "SESSDATA=abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsLive || info.RoomID != "7734200" || info.Platform != "bilibili" || info.RoomURL != "https://live.bilibili.com/6?x=1" {
		t.Fatalf("bad info: %+v", info)
	}
	if info.Anchor != "哔哩哔哩英雄联盟赛事" || info.Title != "【预告】14号17点KT vs BFX" {
		t.Errorf("anchor/title = %q / %q", info.Anchor, info.Title)
	}
	// Fixture: anonymous request for 10000 came back as current_qn 250.
	if info.Quality != platform.QualityHD {
		t.Errorf("quality = %s; want HD (qn 250)", info.Quality)
	}
	if info.Codec != "h264" {
		t.Errorf("codec = %q", info.Codec)
	}
	if !strings.Contains(info.FlvURL, ".flv?") || !strings.Contains(info.HlsURL, ".m3u8?") {
		t.Errorf("urls: flv=%q hls=%q", info.FlvURL, info.HlsURL)
	}
	if info.RecordURL != info.FlvURL {
		t.Error("RecordURL should default to FLV")
	}
	if !strings.HasPrefix(info.FlvURL, "https://d1--cn-gotcha04.bilivideo.com/live-bvc/126800/live_50329118_9516950_2500.flv?expires=1791907728&") {
		t.Errorf("flv url = %q", info.FlvURL)
	}
	if !info.ExpiresAt.Equal(time.Unix(1791907728, 0)) {
		t.Errorf("expires = %v", info.ExpiresAt)
	}
	if info.Headers["Referer"] != "https://live.bilibili.com/" || !strings.HasPrefix(info.Headers["User-Agent"], "Mozilla/5.0") {
		t.Errorf("headers = %v", info.Headers)
	}
	// Request shape: real room id is used after room_init, cookie + browser headers are sent.
	if rs := api.requests(pInit); len(rs) != 1 || rs[0].URL.Query().Get("id") != "6" {
		t.Errorf("room_init requests: %v", rs)
	}
	plays := api.requests(pPlay)
	if len(plays) != 1 {
		t.Fatalf("play requests = %d", len(plays))
	}
	q := plays[0].URL.Query()
	if q.Get("room_id") != "7734200" || q.Get("qn") != "10000" || q.Get("protocol") != "0,1" || q.Get("format") != "0,1,2" || q.Get("codec") != "0,1" {
		t.Errorf("play query = %v", q)
	}
	h := plays[0].Header
	if h.Get("Cookie") != "SESSDATA=abc" || h.Get("Referer") != "https://live.bilibili.com/" || !strings.Contains(h.Get("User-Agent"), "Chrome") {
		t.Errorf("play headers = %v", h)
	}
}

func TestResolvePreferHLS(t *testing.T) {
	r := liveAPI(t).start(t)
	info, err := r.Resolve(context.Background(), "https://live.bilibili.com/6", platform.Options{PreferHLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.RecordURL != info.HlsURL || !strings.Contains(info.RecordURL, "/live_50329118_9516950_2500.m3u8?") {
		t.Errorf("record = %q", info.RecordURL)
	}
}

func TestResolveSingleTierOD(t *testing.T) {
	api := liveAPI(t)
	api.handlers[pPlay] = static(fixture(t, "play_live_single.json"))
	info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/1735947155", platform.Options{Quality: platform.QualityLD})
	if err != nil {
		t.Fatal(err)
	}
	if info.Quality != platform.QualityOD || info.Codec != "h264" {
		t.Errorf("quality=%s codec=%s", info.Quality, info.Codec)
	}
	if !strings.HasPrefix(info.RecordURL, "https://d1--cn-gotcha07b.bilivideo.com/") {
		t.Errorf("should prefer the gotcha host over the edge host: %q", info.RecordURL)
	}
	if len(api.requests(pPlay)) != 1 {
		t.Error("no retry expected: closest offered tier is what we got")
	}
}

func TestResolveOffline(t *testing.T) {
	for _, initFix := range []string{"room_init_offline.json", "room_init_rotation.json"} {
		api := &fakeAPI{handlers: map[string]func(url.Values) (int, string){
			pInit: static(fixture(t, initFix)),
			pH5:   static(fixture(t, "h5_offline.json")),
		}}
		info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/3", platform.Options{})
		if err != nil {
			t.Fatalf("%s: %v", initFix, err)
		}
		if info.IsLive || info.RecordURL != "" || info.Anchor != "3号直播间" || info.Title != "哔哩哔哩音悦台" {
			t.Errorf("%s: %+v", initFix, info)
		}
		if len(api.requests(pPlay)) != 0 {
			t.Errorf("%s: play API must not be called for an offline room", initFix)
		}
	}
}

func TestResolveOfflineH5Failure(t *testing.T) {
	api := &fakeAPI{handlers: map[string]func(url.Values) (int, string){
		pInit: static(fixture(t, "room_init_offline.json")),
		pH5:   static(`{"code":-352,"message":"-352","ttl":1}`),
	}}
	info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/3", platform.Options{})
	if err != nil || info.IsLive || info.RoomID != "23058" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestResolveWentOfflineBetweenCalls(t *testing.T) {
	api := liveAPI(t)
	api.handlers[pPlay] = static(fixture(t, "play_offline.json"))
	info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/6", platform.Options{})
	if err != nil || info.IsLive {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestResolveErrors(t *testing.T) {
	riskJSON := `{"code":-352,"message":"-352","ttl":1}`
	cases := []struct {
		name     string
		path     string
		status   int
		body     string
		wantCode int
		wantText string
	}{
		{"init risk control", pInit, 200, riskJSON, -352, ""},
		{"init not found", pInit, 200, fixture(t, "room_init_notfound.json"), 60004, ""},
		{"init 412 html", pInit, 412, "<html>The request was rejected because of the bilibili security control policy.</html>", 0, "HTTP 412"},
		{"init 412 json", pInit, 412, `{"code":-412,"message":"请求被拦截"}`, -412, "HTTP 412"},
		{"init bad json", pInit, 200, "<html>oops</html>", 0, "bad JSON"},
		{"init empty data", pInit, 200, `{"code":0,"message":"ok","data":null}`, 0, "empty data"},
		{"init 502", pInit, 502, "bad gateway", 0, "HTTP 502"},
		{"h5 risk control while live", pH5, 200, riskJSON, -352, ""},
		{"h5 missing uname while live", pH5, 200, `{"code":0,"data":{"room_info":{"title":"t"}}}`, 0, "missing anchor"},
		{"play risk control", pPlay, 200, `{"code":-352,"message":"风控校验失败","ttl":1}`, -352, ""},
		{"play no playurl", pPlay, 200, `{"code":0,"data":{"live_status":1,"playurl_info":null}}`, 0, "no playurl_info"},
		{"play no streams", pPlay, 200, `{"code":0,"data":{"live_status":1,"playurl_info":{"playurl":{"stream":[]}}}}`, 0, "no FLV/HLS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := liveAPI(t)
			api.handlers[tc.path] = func(url.Values) (int, string) { return tc.status, tc.body }
			info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/6", platform.Options{})
			if err == nil {
				t.Fatalf("want error, got %+v", info)
			}
			var ae *APIError
			if tc.wantCode != 0 && (!errors.As(err, &ae) || ae.Code != tc.wantCode) {
				t.Errorf("err = %v; want API code %d", err, tc.wantCode)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("err = %v; want %q", err, tc.wantText)
			}
		})
	}
}

func TestResolveCanceledContext(t *testing.T) {
	r := liveAPI(t).start(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Resolve(ctx, "https://live.bilibili.com/6", platform.Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}

// synthPlay builds a getRoomPlayInfo response with one FLV and one HLS-ts stream.
func synthPlay(cur int, accept []int, codecs ...string) string {
	var cs []map[string]any
	for _, c := range codecs {
		cs = append(cs, map[string]any{
			"codec_name": c, "current_qn": cur, "accept_qn": accept,
			"base_url": fmt.Sprintf("/live-bvc/1/live_x_%s_%d.EXT?", c, cur),
			"url_info": []map[string]any{
				{"host": "https://xy1x2x3x4xy.mcdn.bilivideo.cn:4483", "extra": "expires=1700000000&qn=" + strconv.Itoa(cur)},
				{"host": "https://cn-gddg-ct-01-01.bilivideo.com", "extra": "expires=1700000000&qn=" + strconv.Itoa(cur)},
			},
		})
	}
	flv, _ := json.Marshal(cs)
	hls := strings.ReplaceAll(string(flv), ".EXT", ".m3u8")
	return fmt.Sprintf(`{"code":0,"data":{"live_status":1,"playurl_info":{"playurl":{"stream":[
		{"protocol_name":"http_stream","format":[{"format_name":"flv","codec":%s}]},
		{"protocol_name":"http_hls","format":[{"format_name":"ts","codec":%s}]}]}}}}`,
		strings.ReplaceAll(string(flv), ".EXT", ".flv"), hls)
}

func TestResolveHEVCOnlyAndHostPreference(t *testing.T) {
	api := liveAPI(t)
	api.handlers[pPlay] = static(synthPlay(10000, []int{10000}, "hevc"))
	info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/6", platform.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Codec != "h265" || info.Quality != platform.QualityOD {
		t.Errorf("codec=%q quality=%s", info.Codec, info.Quality)
	}
	if !strings.HasPrefix(info.RecordURL, "https://cn-gddg-ct-01-01.bilivideo.com/") {
		t.Errorf("mcdn host should be avoided: %q", info.RecordURL)
	}
	if !info.ExpiresAt.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("expires = %v", info.ExpiresAt)
	}
}

func TestResolvePrefersAVC(t *testing.T) {
	api := liveAPI(t)
	api.handlers[pPlay] = static(synthPlay(400, []int{10000, 400}, "hevc", "avc"))
	info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/6", platform.Options{Quality: platform.QualityUHD})
	if err != nil {
		t.Fatal(err)
	}
	if info.Codec != "h264" || !strings.Contains(info.RecordURL, "live_x_avc_400.flv") || info.Quality != platform.QualityUHD {
		t.Errorf("codec=%q record=%q quality=%s", info.Codec, info.RecordURL, info.Quality)
	}
}

// When the server ignores an unlisted tier and returns its default, Resolve asks once more for
// the closest offered tier and reports what it actually got.
func TestResolveRetriesClosestTier(t *testing.T) {
	api := liveAPI(t)
	api.handlers[pPlay] = func(q url.Values) (int, string) {
		if q.Get("qn") == "150" {
			return 200, synthPlay(150, []int{10000, 250, 150}, "avc")
		}
		return 200, synthPlay(10000, []int{10000, 250, 150}, "avc")
	}
	info, err := api.start(t).Resolve(context.Background(), "https://live.bilibili.com/6", platform.Options{Quality: platform.QualityLD})
	if err != nil {
		t.Fatal(err)
	}
	plays := api.requests(pPlay)
	if len(plays) != 2 || plays[0].URL.Query().Get("qn") != "80" || plays[1].URL.Query().Get("qn") != "150" {
		t.Fatalf("play requests: %d", len(plays))
	}
	if info.Quality != platform.QualitySD || !strings.Contains(info.RecordURL, "_150.flv") {
		t.Errorf("quality=%s record=%q", info.Quality, info.RecordURL)
	}
}

func TestQnMapping(t *testing.T) {
	for q, want := range map[platform.Quality]int{
		platform.QualityOD: 10000, platform.QualityUHD: 400, platform.QualityHD: 250,
		platform.QualitySD: 150, platform.QualityLD: 80, "": 10000,
	} {
		if got := qnFor(q); got != want {
			t.Errorf("qnFor(%q) = %d; want %d", q, got, want)
		}
	}
	for qn, want := range map[int]platform.Quality{
		30000: platform.QualityOD, 20000: platform.QualityOD, 15000: platform.QualityOD, 10000: platform.QualityOD,
		400: platform.QualityUHD, 250: platform.QualityHD, 150: platform.QualitySD, 80: platform.QualityLD,
	} {
		if got := qualityForQn(qn); got != want {
			t.Errorf("qualityForQn(%d) = %s; want %s", qn, got, want)
		}
	}
	for _, tc := range []struct {
		accept []int
		want   int
		exp    int
	}{
		{[]int{10000, 400, 250}, 80, 250},
		{[]int{10000, 400, 250}, 10000, 10000},
		{[]int{10000, 250}, 400, 10000}, // tie between 原画 and 超清 → the better one
		{[]int{20000, 10000, 150}, 10000, 10000},
		{[]int{20000, 150}, 10000, 20000},
		{[]int{10000}, 80, 10000},
		{nil, 80, 0},
	} {
		if got := closestQn(tc.accept, tc.want); got != tc.exp {
			t.Errorf("closestQn(%v, %d) = %d; want %d", tc.accept, tc.want, got, tc.exp)
		}
	}
}
