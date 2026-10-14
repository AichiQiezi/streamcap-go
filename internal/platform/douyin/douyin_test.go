package douyin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRegisteredAndMatch(t *testing.T) {
	r := platform.ByKey("douyin")
	if r == nil || r.Name() != "抖音" {
		t.Fatalf("douyin resolver not registered: %v", r)
	}
	for _, u := range []string{
		"https://live.douyin.com/898334614374",
		"https://live.douyin.com/898334614374?anchor_id=1&from=web",
		"https://www.douyin.com/root/live/898334614374",
		"https://v.douyin.com/iAbCdEf/",
		"https://www.douyin.com/user/MS4wLjABAAAAvzaICSahsT1tZZEEnFB0DgEa7V0SBoD_Cr9UZAY5NmA",
	} {
		if !r.Match(u) {
			t.Errorf("Match(%q) = false", u)
		}
	}
	for _, u := range []string{"https://live.bilibili.com/1", "https://notdouyin.com/1", "douyin"} {
		if r.Match(u) {
			t.Errorf("Match(%q) = true", u)
		}
	}
}

func TestWebRidFromPath(t *testing.T) {
	cases := map[string]string{
		"live.douyin.com /898334614374":            "898334614374",
		"www.douyin.com /root/live/898334614374":   "898334614374",
		"www.douyin.com /follow/live/898334614374": "898334614374",
		"www.douyin.com /video/123":                "",
		"live.douyin.com /":                        "",
	}
	for in, want := range cases {
		host, path, _ := strings.Cut(in, " ")
		if got := webRidFromPath(host, pathSegments(path)); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

// Go selection must equal streamget's fetch_stream_url output for the same API response.
// testdata/python_selection.json was produced by running streamget on the fixtures with async_req /
// get_response_status patched (reachable = True → 200, False → probe failure).
func TestSelectionMatchesPython(t *testing.T) {
	var want map[string]struct{ Flv, Hls string }
	if err := json.Unmarshal(readFixture(t, "python_selection.json"), &want); err != nil {
		t.Fatal(err)
	}
	rooms := map[string]roomJSON{}
	{
		var r struct {
			Data struct{ Data []roomJSON } `json:"data"`
		}
		if err := json.Unmarshal(readFixture(t, "enter_live.json"), &r); err != nil {
			t.Fatal(err)
		}
		rooms["enter_live.json"] = r.Data.Data[0]
		var a struct {
			Data struct{ Room roomJSON } `json:"data"`
		}
		if err := json.Unmarshal(readFixture(t, "reflow_live.json"), &a); err != nil {
			t.Fatal(err)
		}
		rooms["reflow_live.json"] = a.Data.Room
	}
	n := 0
	for fx, room := range rooms {
		tr, err := buildTiers(room.StreamURL)
		if err != nil {
			t.Fatal(err)
		}
		for _, reach := range []bool{true, false} {
			for _, q := range platform.Qualities {
				k := fmt.Sprintf("%s|%s|%s", fx, map[bool]string{true: "True", false: "False"}[reach], q)
				w, ok := want[k]
				if !ok {
					t.Fatalf("missing python fixture %s", k)
				}
				sel, err := selectTier(tr, q, func(string) (bool, error) { return reach, nil })
				if err != nil {
					t.Fatal(err)
				}
				if sel.flv != w.Flv || sel.hls != w.Hls {
					t.Errorf("%s:\n got flv=%s\n     hls=%s\nwant flv=%s\n     hls=%s", k, sel.flv, sel.hls, w.Flv, w.Hls)
				}
				n++
			}
		}
	}
	if n != 20 {
		t.Fatalf("compared %d cases, want 20", n)
	}
}

func TestSelectionQualityAndRecordURL(t *testing.T) {
	tr := &tiers{
		flv:         []kv{{"ORIGIN", "http://x/or4.flv?expire=1792508841&codec=h264"}, {"FULL_HD1", "http://x/uhd.flv"}, {"HD1", "http://x/hd.flv"}},
		hls:         []kv{{"ORIGIN", "http://x/or4.m3u8?codec=h264"}, {"FULL_HD1", "http://x/uhd.m3u8"}, {"HD1", "http://x/hd.m3u8"}},
		codecByPath: map[string]string{"/uhd.flv": "h265"},
	}
	ok := func(string) (bool, error) { return true, nil }

	// LD requested, only 3 real tiers → padded, the HD1 tier is used.
	sel, _ := selectTier(tr, platform.QualityLD, ok)
	if sel.quality != platform.QualityHD || sel.flv != "http://x/hd.flv" {
		t.Errorf("LD: got %+v", sel)
	}
	// OD unreachable → falls to FULL_HD1 (UHD), whose SDK data says h265 → HLS record URL.
	sel, _ = selectTier(tr, platform.QualityOD, func(string) (bool, error) { return false, nil })
	if sel.quality != platform.QualityUHD || sel.codec != "h265" {
		t.Errorf("OD fallback: got %+v", sel)
	}
	var info platform.StreamInfo
	fillLive(&info, sel, platform.Options{})
	if info.RecordURL != "http://x/uhd.m3u8" {
		t.Errorf("h265 should record HLS, got %s", info.RecordURL)
	}
	// OD, h264: FLV by default, HLS with PreferHLS; expire parsed.
	sel, _ = selectTier(tr, platform.QualityOD, ok)
	info = platform.StreamInfo{}
	fillLive(&info, sel, platform.Options{})
	if info.RecordURL != sel.flv || info.Codec != "h264" || info.ExpiresAt.Unix() != 1792508841 {
		t.Errorf("OD: got %+v", info)
	}
	info = platform.StreamInfo{}
	fillLive(&info, sel, platform.Options{PreferHLS: true})
	if info.RecordURL != sel.hls || !info.ExpiresAt.IsZero() {
		t.Errorf("OD PreferHLS: got %+v", info)
	}
	// codec=h265 on the FLV URL itself.
	tr.flv[0].Val = "http://x/or4.flv?codec=h265"
	sel, _ = selectTier(tr, platform.QualityOD, ok)
	info = platform.StreamInfo{}
	fillLive(&info, sel, platform.Options{})
	if info.RecordURL != sel.hls || info.Codec != "h265" {
		t.Errorf("codec=h265: got %+v", info)
	}
	// FLV absent → HLS.
	sel, _ = selectTier(&tiers{hls: []kv{{"HD1", "http://x/hd.m3u8"}}}, platform.QualityOD, ok)
	info = platform.StreamInfo{}
	fillLive(&info, sel, platform.Options{})
	if info.RecordURL != "http://x/hd.m3u8" || info.Quality != platform.QualityHD {
		t.Errorf("hls only: got %+v", info)
	}
	// Probe error (ctx cancelled) propagates.
	if _, err := selectTier(tr, platform.QualityOD, func(string) (bool, error) { return false, context.Canceled }); err == nil {
		t.Error("probe error should propagate")
	}
}

func TestExpiry(t *testing.T) {
	cases := map[string]int64{
		"http://x/a.flv?expire=1792508841&sign=s":                     1792508841,
		"http://x/a.flv?expire=6ad78561&sign=s":                       0x6ad78561,
		"http://x/a.flv?k=f77129f544de951c&t=1792508839":              1792508839,
		"http://x/a.flv?keeptime=00093a80&wsSecret=x&wsTime=6ace4ae3": 0x6ace4ae3 + 0x93a80,
		"http://x/a.flv?t=1792508839":                                 0,
		"http://x/a.flv":                                              0,
		"http://x/a.flv?expire=12":                                    0,
	}
	for u, want := range cases {
		got := expiry(u)
		if (want == 0 && !got.IsZero()) || (want != 0 && got.Unix() != want) {
			t.Errorf("%s: got %v want %d", u, got, want)
		}
	}
}

func TestOrderedStrings(t *testing.T) {
	got, err := orderedStrings(json.RawMessage(`{"b":"1","a":"2","c":null}`))
	if err != nil || len(got) != 3 || got[0] != (kv{"b", "1"}) || got[1] != (kv{"a", "2"}) || got[2] != (kv{"c", ""}) {
		t.Fatalf("got %v %v", got, err)
	}
	if got := prependOrigin("o", []kv{{"X", "1"}, {"ORIGIN", "z"}}); len(got) != 2 || got[0] != (kv{"ORIGIN", "z"}) {
		t.Fatalf("prependOrigin: %v", got)
	}
}

// fakeServer serves the web enter API, the reflow API, the profile page and stream HEAD probes.
func fakeServer(t *testing.T, enterBody []byte) (*httptest.Server, *http.Request) {
	return fakeServerFn(t, func(string) []byte { return enterBody })
}

// fakeServerFn builds the enter body from the server's base URL (so stream URLs can point at it).
func fakeServerFn(t *testing.T, enterBody func(base string) []byte) (*httptest.Server, *http.Request) {
	t.Helper()
	var lastEnter http.Request
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/enter", func(w http.ResponseWriter, r *http.Request) {
		lastEnter = *r
		w.Write(enterBody(base))
	})
	mux.HandleFunc("/third/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("probe method %s", r.Method)
		}
	})
	mux.HandleFunc("/reflow", func(w http.ResponseWriter, r *http.Request) {
		w.Write(readFixture(t, "reflow_live.json"))
	})
	mux.HandleFunc("/profile/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `...{"unique_id":"97047583065","verification_type":0}...`)
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	oldEnter, oldReflow, oldProfile := webEnterAPI, appReflowAPI, profilePrefix
	webEnterAPI, appReflowAPI, profilePrefix = srv.URL+"/enter", srv.URL+"/reflow", srv.URL+"/profile/"
	t.Cleanup(func() {
		srv.Close()
		webEnterAPI, appReflowAPI, profilePrefix = oldEnter, oldReflow, oldProfile
	})
	return srv, &lastEnter
}

func TestResolveWithFakeAPI(t *testing.T) {
	r := resolver{}
	ctx := context.Background()

	t.Run("offline", func(t *testing.T) {
		fakeServer(t, readFixture(t, "enter_offline.json"))
		info, err := r.Resolve(ctx, "https://live.douyin.com/745964462470?from=x", platform.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if info.IsLive || info.Anchor != "喜剧电影笑不停" || info.RoomID != "745964462470" || info.RecordURL != "" {
			t.Fatalf("got %+v", info)
		}
	})

	t.Run("live-web", func(t *testing.T) {
		fakeServerFn(t, func(base string) []byte {
			b := strings.ReplaceAll(string(readFixture(t, "enter_live.json")), "http://pull-flv-q95.douyincdn.com", base)
			return []byte(strings.ReplaceAll(b, "http://pull-hls-q95.douyincdn.com", base))
		})
		info, err := r.Resolve(ctx, "https://live.douyin.com/648819501004", platform.Options{Quality: platform.QualitySD})
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsLive || info.Quality != platform.QualitySD || info.Codec != "h264" || info.Title == "" ||
			!strings.HasSuffix(strings.Split(info.RecordURL, "?")[0], "_ld.flv") || info.ExpiresAt.Unix() != 1792508841 {
			t.Fatalf("got %+v", info)
		}
	})

	t.Run("cookie", func(t *testing.T) {
		_, last := fakeServer(t, readFixture(t, "enter_live.json"))
		c := &client{http: http.DefaultClient, opt: platform.Options{Cookie: "sessionid=abc"}}
		rd, err := c.fetchWeb(ctx, "648819501004")
		if err != nil {
			t.Fatal(err)
		}
		if rd.anchor != "魏牌" || rd.room.Status != statusLive {
			t.Fatalf("got %+v", rd)
		}
		ck := last.Header.Get("Cookie")
		if !strings.HasPrefix(ck, "sessionid=abc; ttwid=") {
			t.Errorf("cookie = %q", ck)
		}
		if !strings.Contains(last.URL.RawQuery, "web_rid=648819501004") || !strings.Contains(last.URL.RawQuery, "&a_bogus=") {
			t.Errorf("query = %q", last.URL.RawQuery)
		}
	})

	t.Run("user page via unique_id", func(t *testing.T) {
		_, last := fakeServer(t, readFixture(t, "enter_offline.json"))
		info, err := r.Resolve(ctx, "https://www.douyin.com/user/MS4wLjABAAAAvzaICSahsT1tZZEEnFB0DgEa7V0SBoD_Cr9UZAY5NmA", platform.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if info.RoomID != "97047583065" || !strings.Contains(last.URL.RawQuery, "web_rid=97047583065") {
			t.Fatalf("got %+v query %s", info, last.URL.RawQuery)
		}
	})

	t.Run("reflow", func(t *testing.T) {
		fakeServer(t, nil)
		c := &client{http: http.DefaultClient}
		rd, err := c.fetchApp(ctx, "7696161576210664219", "MS4w")
		if err != nil {
			t.Fatal(err)
		}
		if rd.webRid != "898334614374" || rd.anchor == "" || rd.room.Status != statusLive {
			t.Fatalf("got %+v", rd)
		}
	})

	errCases := map[string][]byte{
		"empty body (risk control)": {},
		"not found / prompts":       readFixture(t, "enter_notfound.json"),
		"garbage":                   []byte("<html>blocked</html>"),
	}
	for name, body := range errCases {
		t.Run(name, func(t *testing.T) {
			fakeServer(t, body)
			info, err := r.Resolve(ctx, "https://live.douyin.com/1", platform.Options{})
			if err == nil {
				t.Fatalf("want error, got %+v", info)
			}
		})
	}

	t.Run("deadline", func(t *testing.T) {
		fakeServer(t, readFixture(t, "enter_offline.json"))
		dctx, cancel := context.WithDeadline(ctx, time.Now())
		defer cancel()
		info, err := r.Resolve(dctx, "https://live.douyin.com/745964462470", platform.Options{})
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline error, got %+v, %v", info, err)
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		_, err := r.Resolve(ctx, "https://www.douyin.com/video/123", platform.Options{})
		if !errors.Is(err, platform.ErrUnsupported) {
			t.Fatalf("got %v", err)
		}
	})
}
