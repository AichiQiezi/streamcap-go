package upload

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/store"
)

// fakeBili imitates the passport, member and UPOS hosts on one TLS server.
type fakeBili struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	chunks map[int][]byte
	parts  int
	added  Archive
	query  url.Values
	polls  int
}

func newFake(t *testing.T) *fakeBili {
	f := &fakeBili{t: t, chunks: map[int][]byte{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBili) client() *Client {
	return &Client{http: f.srv.Client(), passport: f.srv.URL, member: f.srv.URL, api: f.srv.URL}
}

func checkSign(t *testing.T, q url.Values) {
	t.Helper()
	got := q.Get("sign")
	q2 := url.Values{}
	for k, v := range q {
		if k != "sign" {
			q2[k] = v
		}
	}
	sum := md5.Sum([]byte(q2.Encode() + tvAppSec))
	if want := hex.EncodeToString(sum[:]); got != want || q.Get("appkey") != tvAppKey {
		t.Errorf("bad signature: sign=%s want %s appkey=%s", got, want, q.Get("appkey"))
	}
}

func (f *fakeBili) serve(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimPrefix(f.srv.URL, "https:")
	switch {
	case r.URL.Path == "/x/passport-tv-login/qrcode/poll":
		_ = r.ParseForm()
		checkSign(f.t, r.PostForm)
		f.mu.Lock()
		f.polls++
		n := f.polls
		f.mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, `{"code":86039,"message":"二维码尚未确认"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"mid":42,"access_token":"tok","refresh_token":"ref","expires_in":15552000,
			"cookie_info":{"cookies":[{"name":"SESSDATA","value":"s"},{"name":"bili_jct","value":"csrf"}]}}}`)
	case r.URL.Path == "/preupload":
		if !strings.Contains(r.Header.Get("Cookie"), "SESSDATA=s") {
			f.t.Errorf("preupload without cookie")
		}
		if q := r.URL.Query(); q.Get("profile") != "ugcupos/bup" || q.Get("size") != "10" || q.Get("name") != "a.mp4" {
			f.t.Errorf("preupload query: %v", q)
		}
		fmt.Fprintf(w, `{"OK":1,"auth":"AUTH","biz_id":7,"chunk_size":4,"endpoint":%q,"upos_uri":"upos://ugcfx2lf/n123.mp4"}`, host)
	case r.URL.Path == "/ugcfx2lf/n123.mp4":
		if r.Header.Get("X-Upos-Auth") != "AUTH" {
			f.t.Errorf("missing X-Upos-Auth on %s %s", r.Method, r.URL)
		}
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			fmt.Fprint(w, `{"OK":1,"upload_id":"UP"}`)
		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			var chunk int
			fmt.Sscan(q.Get("chunk"), &chunk)
			if q.Get("uploadId") != "UP" || q.Get("chunks") != "3" || q.Get("total") != "10" {
				f.t.Errorf("chunk query: %v", q)
			}
			f.mu.Lock()
			f.chunks[chunk] = body
			f.mu.Unlock()
			fmt.Fprint(w, "MULTIPART_PUT_SUCCESS")
		case r.Method == http.MethodPost:
			var body struct {
				Parts []struct {
					PartNumber int `json:"partNumber"`
				} `json:"parts"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if q.Get("biz_id") != "7" || q.Get("uploadId") != "UP" {
				f.t.Errorf("complete query: %v", q)
			}
			f.mu.Lock()
			f.parts = len(body.Parts)
			f.mu.Unlock()
			fmt.Fprint(w, `{"OK":1}`)
		}
	case r.URL.Path == "/x/vu/web/cover/up":
		fmt.Fprint(w, `{"code":0,"data":{"url":"https://i0.hdslb.com/cover.jpg"}}`)
	case r.URL.Path == "/x/vu/app/add":
		checkSign(f.t, r.URL.Query())
		f.mu.Lock()
		f.query = r.URL.Query()
		_ = json.NewDecoder(r.Body).Decode(&f.added)
		f.mu.Unlock()
		fmt.Fprint(w, `{"code":0,"data":{"aid":1,"bvid":"BV1xx411c7mD"}}`)
	case r.URL.Path == "/x/web-interface/nav":
		fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":42,"uname":"测试账号","face":"https://i0.hdslb.com/face.jpg"}}`)
	default:
		http.NotFound(w, r)
	}
}

// openStore opens a store outside t.TempDir: its background flusher may still be writing when the
// test ends, which makes TempDir's cleanup fail on Windows.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "streamcap-upload-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.RemoveAll(dir)
	})
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPollLogin(t *testing.T) {
	f := newFake(t)
	c := f.client()
	st, acct, err := c.PollLogin(context.Background(), "code")
	if err != nil || st != LoginWaiting || acct != nil {
		t.Fatalf("first poll = %v %v %v, want waiting", st, acct, err)
	}
	st, acct, err = c.PollLogin(context.Background(), "code")
	if err != nil || st != LoginDone {
		t.Fatalf("second poll = %v %v", st, err)
	}
	if acct.Mid != 42 || acct.AccessToken != "tok" || acct.Cookies["bili_jct"] != "csrf" || time.Until(acct.ExpiresAt) < 170*24*time.Hour {
		t.Errorf("account = %+v", acct)
	}
}

func TestUploaderEndToEnd(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	st := openStore(t)
	file := filepath.Join(dir, "a.mp4")
	if err := os.WriteFile(file, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := New(st, func() store.Settings { return store.DefaultSettings() })
	u.c = f.client()
	if _, err := u.PollLogin(context.Background(), "x"); err != nil { // waiting
		t.Fatal(err)
	}
	if s, err := u.PollLogin(context.Background(), "x"); err != nil || s != LoginDone {
		t.Fatalf("login: %v %v", s, err)
	}
	if a := u.Account(); a == nil || a.Name != "测试账号" {
		t.Fatalf("account = %+v", a)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)
	r := u.Draft([]string{file}, Vars{Anchor: "主播", Title: "聊天", URL: "https://live.douyin.com/1", Platform: "抖音",
		Start: time.Date(2026, 10, 24, 20, 5, 0, 0, time.Local)})
	r.OnlySelf = true
	task, err := u.Enqueue(r)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, _ := u.Task(task.ID)
		if v.State == store.UploadDone {
			if v.Bvid != "BV1xx411c7mD" || v.Sent != 10 {
				t.Errorf("done task = %+v", v.UploadTask)
			}
			break
		}
		if v.State == store.UploadFailed || time.Now().After(deadline) {
			t.Fatalf("task = %s %s", v.State, v.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	var got bytes.Buffer
	for i := 0; i < 3; i++ {
		got.Write(f.chunks[i])
	}
	if got.String() != "0123456789" || f.parts != 3 {
		t.Errorf("uploaded %q in %d parts", got.String(), f.parts)
	}
	a := f.added
	if a.Title != "【直播录像】主播 2026-10-24 聊天" || a.Tag != "直播录像,主播" || a.Copyright != 2 ||
		a.Source != "https://live.douyin.com/1" || a.Tid != 21 || len(a.Videos) != 1 || a.Videos[0].Filename != "n123" {
		t.Errorf("submitted %+v", a)
	}
	if a.IsOnlySelf != 1 {
		t.Errorf("is_only_self = %d, want 1", a.IsOnlySelf)
	}
	if !strings.Contains(a.Desc, "开播时间：2026-10-24 20:05") {
		t.Errorf("desc = %q", a.Desc)
	}
	if f.query.Get("access_key") != "tok" {
		t.Errorf("submit query = %v", f.query)
	}
}

func TestNormalize(t *testing.T) {
	base := Request{Files: []string{"x"}, Title: "  t  ", Tags: "a，b,a, ,c", Tid: 21, Copyright: 2, Source: "s"}
	r, err := normalize(base)
	if err != nil || r.Title != "t" || r.Tags != "a,b,c" {
		t.Fatalf("normalize = %+v %v", r, err)
	}
	bad := base
	bad.Source = ""
	if _, err := normalize(bad); err == nil {
		t.Error("转载 without a source accepted")
	}
	own := base
	own.Copyright = 1
	if r, _ := normalize(own); r.Source != "" {
		t.Error("自制 kept a source")
	}
	long := base
	long.Title = strings.Repeat("长", 100)
	if r, _ := normalize(long); len([]rune(r.Title)) != maxTitle {
		t.Errorf("title not clipped: %d", len([]rune(r.Title)))
	}
}

func TestVarsForFile(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t)
	st.Rooms.Update(func(rs *[]store.Room) {
		*rs = append(*rs, store.Room{ID: "r1", Name: "小酱不萌", URL: "https://live.douyin.com/796028189961", Platform: "douyin"})
	})
	u := New(st, store.DefaultSettings)
	v := u.VarsForFile(filepath.Join(dir, "抖音", "小酱不萌", "小酱不萌_2026-10-25_00-19-09_淡淡的_p2.mp4"))
	if v.Anchor != "小酱不萌" || v.Title != "淡淡的" || v.URL != "https://live.douyin.com/796028189961" ||
		v.Start.Format("2006-01-02 15:04:05") != "2026-10-25 00:19:09" {
		t.Errorf("vars = %+v", v)
	}
	for name, title := range map[string]string{
		"小酱不萌_2026-10-13_22-55-13_000.mp4":    "",
		"小酱不萌_2026-10-13_22-55-13_p3_002.mp4": "",
		"小酱不萌_2026-10-13_22-55-13_聊天_001.ts":  "聊天",
		"小酱不萌_2026-10-13_22-55-13_第_2_场.mp4":  "第_2_场",
	} {
		if v := u.VarsForFile(filepath.Join(dir, name)); v.Title != title || v.Anchor != "小酱不萌" {
			t.Errorf("%s: anchor %q title %q, want title %q", name, v.Anchor, v.Title, title)
		}
	}
	v = u.VarsForFile(filepath.Join(dir, "other.mp4"))
	if v.Anchor != "other" || v.URL != "" {
		t.Errorf("unknown file vars = %+v", v)
	}
}

func TestSessionSavedQueuesOnlyAutoUploadRooms(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t)
	file := filepath.Join(dir, "主播_2026-10-25_20-00-00.mp4")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := store.DefaultSettings()
	settings["bili_upload_min_minutes"] = "0" // the fake file has no duration
	u := New(st, func() store.Settings { return settings })
	room := store.Room{ID: "r1", URL: "https://live.douyin.com/1", Platform: "douyin"}
	start := time.Date(2026, 10, 25, 20, 0, 0, 0, time.Local)

	u.SessionSaved(room, "主播", "聊天", start, []string{file, filepath.Join(dir, "a.m4a")})
	if n := len(u.Tasks()); n != 0 {
		t.Fatalf("room without auto upload queued %d tasks", n)
	}
	room.AutoUpload = true
	u.SessionSaved(room, "主播", "聊天", start, []string{file, filepath.Join(dir, "a.m4a")})
	ts := u.Tasks()
	if len(ts) != 1 {
		t.Fatalf("queued %d tasks, want 1", len(ts))
	}
	if got := ts[0]; len(got.Files) != 1 || got.RoomID != "r1" || got.Source != room.URL || got.Title != "【直播录像】主播 2026-10-25 聊天" || !got.OnlySelf {
		t.Errorf("task = %+v", got.UploadTask)
	}

	settings["bili_upload_min_minutes"] = "10"
	u.SessionSaved(room, "主播", "聊天", start, []string{file})
	if n := len(u.Tasks()); n != 1 {
		t.Errorf("a recording shorter than the minimum was queued (%d tasks)", n)
	}
}
