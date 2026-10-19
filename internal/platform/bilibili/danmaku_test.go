package bilibili

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

func rawPacket(ver uint16, op uint32, body []byte) []byte {
	p := packet(op, body)
	binary.BigEndian.PutUint16(p[6:], ver)
	return p
}

func TestEachMessageUnpacksZlibBatches(t *testing.T) {
	danmu := []byte(`{"cmd":"DANMU_MSG:4:0:2:2:2:0","info":[[0,4,25,14893055,1792390990000,0],"弹幕内容",[123,"某用户"]]}`)
	other := []byte(`{"cmd":"WATCHED_CHANGE","data":{}}`)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(append(rawPacket(0, opMessage, danmu), rawPacket(0, opMessage, other)...))
	zw.Close()
	frame := append(rawPacket(2, opMessage, z.Bytes()), rawPacket(1, 3, []byte{0, 0, 0, 1})...) // + a heartbeat reply

	var got []platform.Danmaku
	var ops []uint32
	if err := eachMessage(frame, func(op uint32, body []byte) {
		ops = append(ops, op)
		if d, ok := parseDanmuMsg(body); ok && op == opMessage {
			got = append(got, d)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 || len(got) != 1 {
		t.Fatalf("ops %v, danmaku %+v; want 3 packets, 1 comment", ops, got)
	}
	d := got[0]
	if d.Text != "弹幕内容" || d.User != "某用户" || d.Mode != platform.DanmakuBottom || d.Color != 14893055 {
		t.Fatalf("parsed %+v", d)
	}
}

func TestEachMessageRejectsTruncated(t *testing.T) {
	p := rawPacket(0, opMessage, []byte(`{"cmd":"x"}`))
	if err := eachMessage(p[:len(p)-3], func(uint32, []byte) {}); err == nil {
		t.Fatal("truncated packet accepted")
	}
}

func TestWbiSign(t *testing.T) {
	// Reference values from the public WBI description (bilibili-API-collect).
	mixin := mixinKey("7cd084941338484aae1ad9425b84077c" + "4932caff0ff746eab6f01bf08b70ac45")
	if mixin != "ea1db124af3c7062474693fa704f4ff8" {
		t.Fatalf("mixin = %s", mixin)
	}
	q := url.Values{"foo": {"114"}, "bar": {"514"}, "zab": {"1919810"}}
	got := wbiSign(q, mixin, time.Unix(1702204169, 0))
	if !strings.HasSuffix(got, "&w_rid=8f6f2b5b3d485fe1886cec6a0be8c5d4") || !strings.HasPrefix(got, "bar=514&foo=114&wts=1702204169&zab=1919810") {
		t.Fatalf("signed = %s", got)
	}
}
