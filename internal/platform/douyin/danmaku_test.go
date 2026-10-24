package douyin

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// pb builds one length-delimited protobuf field.
func pbLen(field int, val []byte) []byte {
	out := pbVarint(uint64(field)<<3 | 2)
	out = append(out, pbVarint(uint64(len(val)))...)
	return append(out, val...)
}

func pbVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

// chatMessage mirrors the real shape: Response(1: messages) → Message(1: method, 2: payload) →
// ChatMessage(2: user → 3: nickName, 3: content).
func chatMessage(user, text string) []byte {
	u := pbLen(3, []byte(user))
	msg := append(pbLen(2, u), pbLen(3, []byte(text))...)
	message := append(pbLen(1, []byte("WebcastChatMessage")), pbLen(2, msg)...)
	return pbLen(1, message)
}

func pushFrame(encoding string, payload []byte) []byte {
	f := pbLen(6, []byte(encoding))
	return append(f, pbLen(8, payload)...)
}

func collect(t *testing.T, frame []byte) []platform.Danmaku {
	t.Helper()
	var got []platform.Danmaku
	if err := decodePushFrame(frame, func(d platform.Danmaku) { got = append(got, d) }); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDecodePushFrameGzip(t *testing.T) {
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(chatMessage("小明", " 你好 "))
	zw.Close()

	got := collect(t, pushFrame("gzip", z.Bytes()))
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if d := got[0]; d.User != "小明" || d.Text != "你好" || d.Mode != platform.DanmakuScroll || d.Color != 0xFFFFFF {
		t.Fatalf("parsed %+v", d)
	}
}

func TestDecodePushFramePlain(t *testing.T) {
	if got := collect(t, pushFrame("", chatMessage("a", "b"))); len(got) != 1 {
		t.Fatalf("plain payload: got %d comments, want 1", len(got))
	}
}

func TestDecodePushFrameIgnoresOtherMessages(t *testing.T) {
	other := pbLen(1, append(pbLen(1, []byte("WebcastGiftMessage")), pbLen(2, []byte{0x01})...))
	if got := collect(t, pushFrame("", other)); len(got) != 0 {
		t.Fatalf("gift message produced %+v", got)
	}
	// Heartbeats carry no payload.
	if got := collect(t, pbLen(6, []byte("gzip"))); len(got) != 0 {
		t.Fatalf("empty frame produced %+v", got)
	}
}

func TestDecodePushFrameRejectsTruncated(t *testing.T) {
	frame := pushFrame("", chatMessage("a", "b"))
	if err := decodePushFrame(frame[:len(frame)-3], func(platform.Danmaku) {}); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

// As captured from the live page (2026-10-19): payloadEncoding is "pb", the gzip compression is
// only announced in a compress_type header.
func TestDecodePushFrameAsSentByThePage(t *testing.T) {
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(chatMessage("观众", "来了"))
	zw.Close()
	header := append(pbLen(1, []byte("compress_type")), pbLen(2, []byte("gzip"))...)
	frame := append(pbLen(5, header), pbLen(6, []byte("pb"))...)
	frame = append(frame, pbLen(7, []byte("msg"))...)
	frame = append(frame, pbLen(8, z.Bytes())...)
	if got := collect(t, frame); len(got) != 1 || got[0].Text != "来了" {
		t.Fatalf("got %+v", got)
	}
}

func TestEmojiText(t *testing.T) {
	for in, want := range map[string]string{
		"晚上好[玫瑰][呲牙]": "晚上好🌹😁",
		"[赞][赞]":      "👍👍",
		"[问]什么时候上":    "[问]什么时候上", // not an emoticon: left alone
		"无表情":         "无表情",
	} {
		if got := emojiText(in); got != want {
			t.Errorf("emojiText(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeWatcher stands in for the hidden page: it records what was opened and lets the test push
// frames as the page's websocket would.
type fakeWatcher struct {
	mu      sync.Mutex
	opened  []string
	onFrame func([]byte)
	closed  chan struct{}
}

func installFakeWatcher(t *testing.T) *fakeWatcher {
	f := &fakeWatcher{closed: make(chan struct{})}
	old := platform.OpenWatcher
	platform.OpenWatcher = func(url string, onFrame func([]byte)) func() {
		f.mu.Lock()
		f.opened, f.onFrame = append(f.opened, url), onFrame
		f.mu.Unlock()
		var once sync.Once
		return func() { once.Do(func() { close(f.closed) }) }
	}
	t.Cleanup(func() { platform.OpenWatcher = old })
	return f
}

func (f *fakeWatcher) push(b []byte) {
	f.mu.Lock()
	fn := f.onFrame
	f.mu.Unlock()
	fn(b)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDanmakuWatchesThePageAndClosesIt(t *testing.T) {
	f := installFakeWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var got []platform.Danmaku
	done := make(chan error, 1)
	go func() {
		done <- resolver{}.Danmaku(ctx, "https://live.douyin.com/646454278948", platform.Options{}, func(d platform.Danmaku) {
			mu.Lock()
			got = append(got, d)
			mu.Unlock()
		})
	}()
	waitFor(t, "the page to open", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.opened) == 1 })
	if f.opened[0] != "https://live.douyin.com/646454278948" {
		t.Fatalf("opened %q", f.opened[0])
	}

	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(chatMessage("观众", "[玫瑰]晚上好"))
	zw.Close()
	header := append(pbLen(1, []byte("compress_type")), pbLen(2, []byte("gzip"))...)
	frame := append(append(pbLen(5, header), pbLen(6, []byte("pb"))...), pbLen(8, z.Bytes())...)
	before := time.Now()
	f.push(frame)
	f.push([]byte{0xff}) // garbage from the page must not stop the stream
	f.push(frame)
	waitFor(t, "two comments", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 2 })
	if d := got[0]; d.Text != "🌹晚上好" || d.User != "观众" || d.At.Before(before) {
		t.Fatalf("comment %+v", d)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Danmaku returned %v after cancel", err)
	}
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("the page was not closed when the session ended")
	}
}

func TestDanmakuRetriesFindingTheRoom(t *testing.T) {
	f := installFakeWatcher(t)
	oldResolve, oldBase := resolveRoom, ridRetryBase
	ridRetryBase = time.Millisecond
	calls := 0
	resolveRoom = func(context.Context, resolver, string, platform.Options) (*platform.StreamInfo, error) {
		if calls++; calls < 3 {
			return nil, errors.New("risk control")
		}
		return &platform.StreamInfo{Ident: "97031554167"}, nil
	}
	t.Cleanup(func() { resolveRoom, ridRetryBase = oldResolve, oldBase })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = resolver{}.Danmaku(ctx, "https://v.douyin.com/y9gIe0Pibig/", platform.Options{}, func(platform.Danmaku) {})
	}()
	waitFor(t, "the page to open after retries", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.opened) == 1 })
	if calls != 3 || f.opened[0] != "https://live.douyin.com/97031554167" {
		t.Fatalf("calls %d, opened %v", calls, f.opened)
	}
}

func TestDanmakuGivesUpOnALinkThatIsNoRoom(t *testing.T) {
	installFakeWatcher(t)
	old := resolveRoom
	resolveRoom = func(context.Context, resolver, string, platform.Options) (*platform.StreamInfo, error) {
		return nil, fmt.Errorf("douyin: %w", platform.ErrUnsupported)
	}
	t.Cleanup(func() { resolveRoom = old })
	err := resolver{}.Danmaku(context.Background(), "https://www.douyin.com/video/1", platform.Options{}, func(platform.Danmaku) {})
	if !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported without retrying", err)
	}
}

func TestDanmakuWithoutABrowser(t *testing.T) {
	old := platform.OpenWatcher
	platform.OpenWatcher = nil
	t.Cleanup(func() { platform.OpenWatcher = old })
	if (resolver{}).DanmakuReady() {
		t.Fatal("ready without a page watcher")
	}
	if err := (resolver{}).Danmaku(context.Background(), "https://live.douyin.com/1", platform.Options{}, func(platform.Danmaku) {}); err == nil {
		t.Fatal("no error without a page watcher")
	}
}
