package douyin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// Danmaku follows the room's comment stream (implements platform.DanmakuSource).
//
// Douyin only hands out comments to the web page itself: the websocket URL carries a signature the
// page's own script produces, bound to the room and viewer, so it can't be reused or forged here.
// The page therefore runs in a hidden web view (platform.OpenWatcher) and its comment websocket is
// forwarded frame by frame; this file only decodes those frames. No login, no signature forging.
func (r resolver) Danmaku(ctx context.Context, roomURL string, opt platform.Options, emit func(platform.Danmaku)) error {
	if platform.OpenWatcher == nil {
		return errors.New("douyin danmaku: 这个运行方式没有内置浏览器，无法读取弹幕")
	}
	webRid, err := r.watchRidRetrying(ctx, roomURL, opt)
	if err != nil || ctx.Err() != nil {
		return err
	}
	page := "https://live.douyin.com/" + url.PathEscape(webRid)
	frames := make(chan []byte, 128)
	closePage := platform.OpenWatcher(page, func(b []byte) {
		select {
		case frames <- b:
		default: // the page is faster than the writer; drop the excess rather than stall it
		}
	})
	if closePage == nil {
		return errors.New("douyin danmaku: 无法打开后台网页")
	}
	defer closePage()
	slog.Info("douyin danmaku: watching page", "room", roomURL, "page", page)
	dump := openFrameDump()
	if dump != nil {
		defer dump.Close()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case b := <-frames:
			now := time.Now()
			if dump != nil {
				writeFrame(dump, b)
			}
			if err := decodePushFrame(b, func(d platform.Danmaku) {
				d.At = now
				emit(d)
			}); err != nil {
				slog.Debug("douyin danmaku: bad frame", "err", err)
			}
		}
	}
}

// openFrameDump opens $STREAMCAP_DANMAKU_DUMP for appending raw frames (debugging and refreshing the
// test fixture, see testdata/README.md); nil when unset.
func openFrameDump() *os.File {
	path := os.Getenv("STREAMCAP_DANMAKU_DUMP")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("douyin danmaku: cannot open frame dump", "file", path, "err", err)
		return nil
	}
	slog.Info("douyin danmaku: dumping raw frames", "file", path)
	return f
}

// writeFrame appends one frame as a little-endian uint32 length followed by its bytes.
func writeFrame(w io.Writer, b []byte) {
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(b)))
	_, _ = w.Write(n[:])
	_, _ = w.Write(b)
}

// readFrames splits a dump written by writeFrame.
func readFrames(raw []byte) ([][]byte, error) {
	var out [][]byte
	for len(raw) > 0 {
		if len(raw) < 4 {
			return out, errMalformed
		}
		n := int(binary.LittleEndian.Uint32(raw))
		if n > len(raw)-4 {
			return out, errMalformed
		}
		out = append(out, raw[4:4+n])
		raw = raw[4+n:]
	}
	return out, nil
}

// DanmakuReady reports that the hidden page watcher is available (the macOS/Windows window and
// menu-bar/tray modes; not -headless, which has no browser).
func (resolver) DanmakuReady() bool { return platform.OpenWatcher != nil }

// Retry pacing for finding the room when the session starts (tests shrink it).
var (
	ridRetryBase = 2 * time.Second
	ridRetryMax  = time.Minute
)

// watchRidRetrying keeps trying to find the room: a failure at the start of a session (network,
// risk control) would otherwise lose the comments for the whole live. Only a link that can never be
// a room gives up.
func (r resolver) watchRidRetrying(ctx context.Context, roomURL string, opt platform.Options) (string, error) {
	for attempt := 0; ; attempt++ {
		rid, err := r.watchRid(ctx, roomURL, opt)
		if err == nil || errors.Is(err, platform.ErrUnsupported) || ctx.Err() != nil {
			return rid, err
		}
		wait := min(ridRetryBase<<min(attempt, 6), ridRetryMax)
		slog.Warn("douyin danmaku: room not found yet, retrying", "room", roomURL, "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return "", nil
		case <-time.After(wait):
		}
	}
}

// resolveRoom resolves a link for watchRid (tests replace it).
var resolveRoom = func(ctx context.Context, r resolver, roomURL string, opt platform.Options) (*platform.StreamInfo, error) {
	return r.Resolve(ctx, roomURL, opt)
}

// watchRid finds the 抖音号 (web_rid) to load in the page. A live.douyin.com link already carries it;
// other shapes (short shares, profile links) go through the normal resolver.
func (r resolver) watchRid(ctx context.Context, roomURL string, opt platform.Options) (string, error) {
	if u, err := url.Parse(strings.TrimSpace(roomURL)); err == nil && strings.EqualFold(u.Hostname(), "live.douyin.com") {
		if segs := pathSegments(u.Path); len(segs) == 1 {
			return segs[0], nil
		}
	}
	info, err := resolveRoom(ctx, r, roomURL, opt)
	if err != nil {
		return "", err
	}
	if info.Ident != "" {
		return info.Ident, nil
	}
	if info.RoomID != "" {
		return info.RoomID, nil
	}
	return "", errors.New("douyin danmaku: 链接里没有抖音号")
}

// decodePushFrame unpacks a PushFrame: payloadEncoding (6) and payload (8), gunzipped when needed.
func decodePushFrame(frame []byte, emit func(platform.Danmaku)) error {
	var encoding string
	var payload []byte
	err := eachField(frame, func(field, _ int, val []byte, _ uint64) error {
		switch field {
		case 6:
			encoding = string(val)
		case 8:
			payload = val
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil // a heartbeat / control frame without a Response
	}
	// Real frames say payloadEncoding "pb" and put the compression in a compress_type header; the
	// gzip magic bytes are the reliable signal.
	if strings.EqualFold(encoding, "gzip") || bytes.HasPrefix(payload, []byte{0x1f, 0x8b}) {
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		inner, err := io.ReadAll(io.LimitReader(zr, 8<<20))
		zr.Close()
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		payload = inner
	}
	return decodeResponse(payload, emit)
}

// decodeResponse walks Response.messages (1); each Message has method (1) and payload (2).
func decodeResponse(buf []byte, emit func(platform.Danmaku)) error {
	return eachField(buf, func(field, _ int, val []byte, _ uint64) error {
		if field != 1 {
			return nil
		}
		var method string
		var payload []byte
		if err := eachField(val, func(f, _ int, v []byte, _ uint64) error {
			switch f {
			case 1:
				method = string(v)
			case 2:
				payload = v
			}
			return nil
		}); err != nil {
			return err
		}
		if method == "WebcastChatMessage" && len(payload) > 0 {
			if d, ok := decodeChatMessage(payload); ok {
				emit(d)
			}
		}
		return nil
	})
}

// decodeChatMessage reads ChatMessage.user (2) → User.nickName (3) and ChatMessage.content (3).
func decodeChatMessage(buf []byte) (platform.Danmaku, bool) {
	d := platform.Danmaku{Mode: platform.DanmakuScroll, Color: 0xFFFFFF}
	err := eachField(buf, func(field, _ int, val []byte, _ uint64) error {
		switch field {
		case 2:
			return eachField(val, func(f, _ int, v []byte, _ uint64) error {
				if f == 3 {
					d.User = string(v)
				}
				return nil
			})
		case 3:
			d.Text = string(val)
		}
		return nil
	})
	if err != nil {
		return platform.Danmaku{}, false
	}
	d.Text = emojiText(strings.TrimSpace(d.Text))
	return d, d.Text != ""
}

var errMalformed = errors.New("malformed protobuf")

// eachField walks protobuf fields, handing length-delimited values and varints to fn.
func eachField(b []byte, fn func(field, wire int, val []byte, num uint64) error) error {
	for len(b) > 0 {
		key, n := readVarint(b)
		if n <= 0 {
			return errMalformed
		}
		b = b[n:]
		field, wire := int(key>>3), int(key&7)
		switch wire {
		case 0:
			v, n := readVarint(b)
			if n <= 0 {
				return errMalformed
			}
			b = b[n:]
			if err := fn(field, wire, nil, v); err != nil {
				return err
			}
		case 1:
			if len(b) < 8 {
				return errMalformed
			}
			b = b[8:]
		case 2:
			l, n := readVarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return errMalformed
			}
			val := b[n : n+int(l)]
			b = b[n+int(l):]
			if err := fn(field, wire, val, 0); err != nil {
				return err
			}
		case 5:
			if len(b) < 4 {
				return errMalformed
			}
			b = b[4:]
		default:
			return fmt.Errorf("wire type %d", wire)
		}
	}
	return nil
}

func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		v |= uint64(c&0x7f) << (7 * uint(i))
		if c < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}
