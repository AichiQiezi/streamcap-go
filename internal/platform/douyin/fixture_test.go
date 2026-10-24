package douyin

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// testdata/push_frames.bin holds real frames received by the live page (see testdata/README.md), so
// the transport layers — frame headers, compression, Response / Message wrapping — are exercised as
// douyin actually sends them. Refresh it from a dump with:
//
//	scripts/e2e-danmaku.sh https://live.douyin.com/<id> 90 --dump /tmp/frames.bin
//	go test ./internal/platform/douyin -run TestRefreshFixture -fixture-from=/tmp/frames.bin
var fixtureFrom = flag.String("fixture-from", "", "raw frame dump (STREAMCAP_DANMAKU_DUMP) to rebuild testdata/push_frames.bin from")

const fixturePath = "testdata/push_frames.bin"

func TestRealFramesFixture(t *testing.T) {
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := readFrames(raw)
	if err != nil || len(frames) == 0 {
		t.Fatalf("fixture: %d frames, %v", len(frames), err)
	}
	var got []platform.Danmaku
	for i, f := range frames {
		if err := decodePushFrame(f, func(d platform.Danmaku) { got = append(got, d) }); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if len(got) == 0 {
		t.Fatal("no comments decoded from real frames")
	}
	for _, d := range got {
		if !strings.HasPrefix(d.User, "观众") || d.Text == "" {
			t.Fatalf("unexpected comment %+v (fixture not anonymised, or decoding is off)", d)
		}
	}
	t.Logf("%d real frames, %d comments", len(frames), len(got))
}

// TestRefreshFixture turns a raw dump into the fixture: frames with comments only, each comment
// rebuilt from its text and numeric fields under a placeholder viewer — the user record (name, id,
// handle, avatar) and rich-text fields that may quote it are not kept. The frame and its other
// layers are kept byte for byte.
func TestRefreshFixture(t *testing.T) {
	if *fixtureFrom == "" {
		t.Skip("pass -fixture-from=<dump> to rebuild the fixture")
	}
	raw, err := os.ReadFile(*fixtureFrom)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := readFrames(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	kept, viewers := 0, 0
	for _, f := range frames {
		nf, ok, err := anonymizeFrame(f, &viewers)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			writeFrame(&out, nf)
			kept++
		}
	}
	if kept == 0 {
		t.Fatal("the dump has no comments")
	}
	if err := os.WriteFile(fixturePath, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s: %d of %d frames, %d comments", fixturePath, kept, len(frames), viewers)
}

type span struct{ field, wire, start, end, valStart, valEnd int }

// spans lists the fields of b with their byte ranges, so untouched ones can be copied verbatim.
func spans(b []byte) ([]span, error) {
	var out []span
	for i := 0; i < len(b); {
		start := i
		key, n := readVarint(b[i:])
		if n <= 0 {
			return nil, errMalformed
		}
		i += n
		s := span{field: int(key >> 3), wire: int(key & 7), start: start}
		switch s.wire {
		case 0:
			_, n := readVarint(b[i:])
			if n <= 0 {
				return nil, errMalformed
			}
			s.valStart, i = i, i+n
		case 1:
			s.valStart, i = i, i+8
		case 2:
			l, n := readVarint(b[i:])
			if n <= 0 || int(l) > len(b)-i-n {
				return nil, errMalformed
			}
			s.valStart, i = i+n, i+n+int(l)
		case 5:
			s.valStart, i = i, i+4
		default:
			return nil, fmt.Errorf("wire type %d", s.wire)
		}
		if i > len(b) {
			return nil, errMalformed
		}
		s.end, s.valEnd = i, i
		out = append(out, s)
	}
	return out, nil
}

func anonymizeFrame(frame []byte, viewers *int) ([]byte, bool, error) {
	fs, err := spans(frame)
	if err != nil {
		return nil, false, err
	}
	var rebuilt bytes.Buffer
	var payload []byte
	for _, s := range fs {
		if s.field == 8 {
			payload = frame[s.valStart:s.valEnd]
			continue
		}
		rebuilt.Write(frame[s.start:s.end])
	}
	gz := bytes.HasPrefix(payload, []byte{0x1f, 0x8b})
	if gz {
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, false, err
		}
		if payload, err = io.ReadAll(zr); err != nil {
			return nil, false, err
		}
	}
	resp, err := spans(payload)
	if err != nil {
		return nil, false, nil // not a Response (e.g. an ack): leave it out
	}
	var newResp bytes.Buffer
	chats := 0
	for _, s := range resp {
		if s.field != 1 {
			newResp.Write(payload[s.start:s.end])
			continue
		}
		msg := payload[s.valStart:s.valEnd]
		ms, err := spans(msg)
		if err != nil {
			return nil, false, err
		}
		var method string
		for _, m := range ms {
			if m.field == 1 {
				method = string(msg[m.valStart:m.valEnd])
			}
		}
		if method != "WebcastChatMessage" {
			continue // member joins, rankings, gifts… name viewers; they aren't needed
		}
		var newMsg bytes.Buffer
		for _, m := range ms {
			if m.field != 2 {
				newMsg.Write(msg[m.start:m.end])
				continue
			}
			chat := msg[m.valStart:m.valEnd]
			cs, err := spans(chat)
			if err != nil {
				return nil, false, err
			}
			*viewers++
			var newChat bytes.Buffer
			newChat.Write(pbLen(2, pbLen(3, []byte(fmt.Sprintf("观众%d", *viewers)))))
			for _, c := range cs {
				if c.wire == 0 || c.field == 3 { // numbers and the comment's text
					newChat.Write(chat[c.start:c.end])
				}
			}
			newMsg.Write(pbLen(2, newChat.Bytes()))
		}
		newResp.Write(pbLen(1, newMsg.Bytes()))
		chats++
	}
	if chats == 0 {
		return nil, false, nil
	}
	body := newResp.Bytes()
	if gz {
		var z bytes.Buffer
		zw := gzip.NewWriter(&z)
		zw.Write(body)
		zw.Close()
		body = z.Bytes()
	}
	rebuilt.Write(pbLen(8, body))
	return rebuilt.Bytes(), true, nil
}
