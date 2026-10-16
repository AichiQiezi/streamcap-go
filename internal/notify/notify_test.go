package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/store"
)

// fakeQQ mimics the two bot endpoints. The first token it hands out is rejected once, to exercise
// the refresh-and-retry path.
type fakeQQ struct {
	mu       sync.Mutex
	tokens   int
	sent     map[string][]string // openid -> contents
	rejected bool
}

func (f *fakeQQ) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/app/getAppAccessToken":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["clientSecret"] != "good" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 100016, "message": "invalid appid or secret"})
			return
		}
		f.tokens++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t" + string(rune('0'+f.tokens)), "expires_in": "7200"})
	case strings.HasPrefix(r.URL.Path, "/v2/users/"):
		if r.Header.Get("Authorization") == "QQBot t1" && !f.rejected {
			f.rejected = true
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/users/"), "/messages")
		f.sent[id] = append(f.sent[id], in["content"].(string))
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "m1"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func withFakeQQ(t *testing.T) *fakeQQ {
	f := &fakeQQ{sent: map[string][]string{}}
	srv := httptest.NewServer(f)
	old := qqAPI
	qqAPI = srv.URL
	qqTokens.Lock()
	qqTokens.m = map[string]qqToken{}
	qqTokens.Unlock()
	t.Cleanup(func() { srv.Close(); qqAPI = old })
	return f
}

func qqSettings(secret string) store.Settings {
	s := store.DefaultSettings()
	s["system_notify_enabled"] = false
	s["qqbot_enabled"] = true
	s["qqbot_app_id"] = "app"
	s["qqbot_secret"] = secret
	s["qqbot_openid"] = "U1, U2"
	return s
}

func TestQQRetriesStaleTokenAndSendsToEveryOpenID(t *testing.T) {
	f := withFakeQQ(t)
	if err := Test(context.Background(), qqSettings("good"), "qqbot"); err != nil {
		t.Fatal(err)
	}
	if len(f.sent["U1"]) != 1 || len(f.sent["U2"]) != 1 {
		t.Fatalf("sent = %v, want one message to each openid", f.sent)
	}
	if f.tokens != 2 {
		t.Fatalf("tokens fetched = %d, want 2 (first rejected, then refreshed)", f.tokens)
	}
	// The refreshed token is cached.
	if err := Test(context.Background(), qqSettings("good"), "qqbot"); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 {
		t.Fatalf("tokens fetched = %d after a second send, want the cached one reused", f.tokens)
	}
}

func TestQQBadSecretIsReported(t *testing.T) {
	withFakeQQ(t)
	err := Test(context.Background(), qqSettings("bad"), "qqbot")
	if err == nil || !strings.Contains(err.Error(), "invalid appid or secret") {
		t.Fatalf("err = %v, want the platform's message", err)
	}
}

func TestSendGating(t *testing.T) {
	f := withFakeQQ(t)
	s := qqSettings("good")
	s["stream_start_notification_enabled"] = false // no longer a gate: the room's own switch decides
	s["stream_end_notification_enabled"] = false

	Send(context.Background(), s, Event{Start: true, Anchor: "主播A", Title: "标题", URL: "https://live.douyin.com/1"})
	if len(f.sent["U1"]) != 1 || !strings.Contains(f.sent["U1"][0], "主播A 开播了") {
		t.Fatalf("start not sent: %v", f.sent)
	}
	Send(context.Background(), s, Event{Start: false, Anchor: "主播A", Live: 90 * time.Minute})
	if len(f.sent["U1"]) != 1 {
		t.Fatalf("end sent although 下播提醒 is off: %v", f.sent["U1"])
	}
	s["stream_end_notification_enabled"] = true
	Send(context.Background(), s, Event{Start: false, Anchor: "主播A", Live: 90 * time.Minute})
	if got := f.sent["U1"]; len(got) != 2 || !strings.Contains(got[1], "直播时长约：1h30m") {
		t.Fatalf("end message = %v", got)
	}
}
