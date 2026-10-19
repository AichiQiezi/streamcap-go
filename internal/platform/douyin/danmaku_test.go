package douyin

import (
	"bytes"
	"compress/gzip"
	"testing"

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
