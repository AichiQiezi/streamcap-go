package bilibili

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// shareServer plays both b23.tv and api.live.bilibili.com, with the redirect shapes observed on
// 2026-10-15: a room share 302s to the room with share params, a profile share to m.bilibili.com,
// an unknown code answers 200.
func shareServer(t *testing.T) (*Resolver, *int32) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/room/v1/Room/room_init" { // the per-add check; short links and uids are cached
			atomic.AddInt32(&calls, 1)
		}
		switch r.URL.Path {
		case "/room/v1/Room/room_init":
			switch r.URL.Query().Get("id") {
			case "6", "7734200": // short id and real id of the same room
				_, _ = w.Write([]byte(`{"code":0,"data":{"room_id":7734200,"short_id":6,"uid":50329118,"live_status":1}}`))
			default:
				_, _ = w.Write([]byte(`{"code":60004,"message":"直播间不存在","data":{}}`))
			}
		case "/5piv15g":
			http.Redirect(w, r, "https://live.bilibili.com/7734200?plat_id=124&share_from=h5&share_source=COPY&unique_k=5piv15g", http.StatusFound)
		case "/Wj4DjOq":
			http.Redirect(w, r, "https://m.bilibili.com/space/50329118?plat_id=1&share_from=space&unique_k=Wj4DjOq", http.StatusFound)
		case "/BVvideo":
			http.Redirect(w, r, "https://www.bilibili.com/video/BV1xx411c7mD", http.StatusFound)
		case "/room/v1/Room/getRoomInfoOld":
			switch r.URL.Query().Get("mid") {
			case "50329118":
				_, _ = w.Write([]byte(`{"code":0,"message":"OK","data":{"roomStatus":1,"roomid":7734200,"url":"https://live.bilibili.com/7734200"}}`))
			default: // a user without a room
				_, _ = w.Write([]byte(`{"code":0,"message":"OK","data":{"roomStatus":0,"roomid":0,"url":""}}`))
			}
		default:
			_, _ = w.Write([]byte("<html>not found</html>"))
		}
	}))
	t.Cleanup(srv.Close)
	return &Resolver{apiBase: srv.URL, shortBase: srv.URL}, &calls
}

func TestCanonicalShareLinks(t *testing.T) {
	r, calls := shareServer(t)
	for in, want := range map[string]string{
		"https://b23.tv/5piv15g":                  "https://live.bilibili.com/7734200",
		"b23.tv/5piv15g":                          "https://live.bilibili.com/7734200",
		"https://b23.tv/Wj4DjOq":                  "https://live.bilibili.com/7734200", // profile share
		"https://space.bilibili.com/50329118":     "https://live.bilibili.com/7734200",
		"https://m.bilibili.com/space/50329118?x": "https://live.bilibili.com/7734200",
		"https://live.bilibili.com/6?spm=1":       "https://live.bilibili.com/7734200", // short id -> real id
	} {
		got, err := r.Canonical(context.Background(), in, platform.Options{})
		if err != nil || got.URL != want || got.Ident != "7734200" {
			t.Errorf("Canonical(%q) = %+v, %v; want %q, ident 7734200", in, got, err, want)
		}
	}
	// Short links and uids are looked up once.
	before := atomic.LoadInt32(calls)
	if _, err := r.Canonical(context.Background(), "https://b23.tv/Wj4DjOq", platform.Options{}); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(calls) != before {
		t.Error("second lookup of the same share link hit the network")
	}
}

func TestCanonicalRejectsWithReason(t *testing.T) {
	r, _ := shareServer(t)
	for in, reason := range map[string]string{
		"https://b23.tv/nope000":           "B站短链接无效或已失效",
		"https://space.bilibili.com/2":     "这个 B站用户还没有开通直播间",
		"https://b23.tv/BVvideo":           "不是 B站直播间或个人主页的链接",
		"https://www.bilibili.com/video/x": "不是 B站直播间或个人主页的链接",
		"https://live.bilibili.com/999":    "这个 B站直播间不存在",
	} {
		_, err := r.Canonical(context.Background(), in, platform.Options{})
		var ue *platform.UnsupportedError
		if !errors.As(err, &ue) || ue.Reason != reason || !errors.Is(err, platform.ErrUnsupported) {
			t.Errorf("Canonical(%q) err = %v; want UnsupportedError %q", in, err, reason)
		}
	}
}

func TestMatchShareHosts(t *testing.T) {
	r := &Resolver{}
	for in, want := range map[string]bool{
		"https://b23.tv/5piv15g":         true,
		"https://bili2233.cn/5piv15g":    true,
		"https://space.bilibili.com/123": true,
		"https://notb23.tv/x":            false,
		"https://b23.tv.evil.com/x":      false,
	} {
		if got := r.Match(in); got != want {
			t.Errorf("Match(%q) = %v; want %v", in, got, want)
		}
	}
}
