package bilibili

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// Danmaku follows the room's comment stream (implements platform.DanmakuSource).
//
// Flow, as the web player does it: a buvid3 cookie (from www.bilibili.com) and the WBI keys (from
// /x/web-interface/nav) sign getDanmuInfo, which returns a token and the comment servers. The
// WebSocket then takes an auth packet and a heartbeat every 30s. Anonymous connections receive
// every comment, with user names; a logged-in cookie (SESSDATA) is passed through when set.
func (r *Resolver) Danmaku(ctx context.Context, roomURL string, opt platform.Options, emit func(platform.Danmaku)) error {
	c := r.caller(opt)
	id, err := r.roomID(ctx, c, roomURL)
	if err != nil {
		return err
	}
	data, err := c.get(ctx, "/room/v1/Room/room_init", url.Values{"id": {id}})
	if err != nil {
		return err
	}
	ri, err := parseRoomInit(data)
	if err != nil {
		return err
	}
	for attempt := 0; ctx.Err() == nil; attempt++ {
		start := time.Now()
		err := r.danmakuOnce(ctx, c, ri.RoomID, emit)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			attempt = 0 // it was working: reconnect right away
		}
		wait := min(time.Duration(1<<min(attempt, 6))*time.Second, time.Minute)
		slog.Warn("bilibili danmaku disconnected, reconnecting", "room", ri.RoomID, "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
	return nil
}

func (r *Resolver) danmakuOnce(ctx context.Context, c *caller, roomID int64, emit func(platform.Danmaku)) error {
	cookie, err := r.danmakuCookie(ctx, c)
	if err != nil {
		return err
	}
	info, err := r.danmuInfo(ctx, c, roomID, cookie)
	if err != nil {
		return err
	}
	hosts := info.HostList
	hosts = append(hosts, danmuHost{Host: "broadcastlv.chat.bilibili.com", WssPort: 443})
	var conn *websocket.Conn
	for _, h := range hosts {
		u := fmt.Sprintf("wss://%s:%d/sub", h.Host, h.WssPort)
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		conn, _, err = websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: c.client, HTTPHeader: http.Header{
			"User-Agent": {desktopUA}, "Origin": {liveOrigin},
		}})
		cancel()
		if err == nil {
			break
		}
	}
	if conn == nil {
		return fmt.Errorf("bilibili danmaku: connect: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)

	auth, _ := json.Marshal(map[string]any{
		"uid": cookieUID(c.cookie), "roomid": roomID, "protover": 2, "buvid": cookieValue(cookie, "buvid3"),
		"platform": "web", "type": 2, "key": info.Token,
	})
	if err := conn.Write(ctx, websocket.MessageBinary, packet(opAuth, auth)); err != nil {
		return err
	}
	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			if conn.Write(hbCtx, websocket.MessageBinary, packet(opHeartbeat, []byte("[object Object]"))) != nil {
				return
			}
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
			}
		}
	}()
	for {
		// A live room answers the heartbeat at least every 30s; a silent minute is a dead link.
		rctx, cancel := context.WithTimeout(ctx, 70*time.Second)
		_, msg, err := conn.Read(rctx)
		cancel()
		if err != nil {
			return err
		}
		now := time.Now()
		if err := eachMessage(msg, func(op uint32, body []byte) {
			if op == opMessage {
				if d, ok := parseDanmuMsg(body); ok {
					d.At = now
					emit(d)
				}
			}
		}); err != nil {
			slog.Debug("bilibili danmaku: bad packet", "err", err)
		}
	}
}

// ---- packets ----

const (
	opHeartbeat = 2
	opMessage   = 5
	opAuth      = 7
)

// packet frames body: 16-byte header (length, header length, protocol version, operation, sequence).
func packet(op uint32, body []byte) []byte {
	h := make([]byte, 16, 16+len(body))
	binary.BigEndian.PutUint32(h[0:], uint32(16+len(body)))
	binary.BigEndian.PutUint16(h[4:], 16)
	binary.BigEndian.PutUint16(h[6:], 1)
	binary.BigEndian.PutUint32(h[8:], op)
	binary.BigEndian.PutUint32(h[12:], 1)
	return append(h, body...)
}

// eachMessage walks the packets in buf, unpacking zlib-compressed batches (protocol version 2).
func eachMessage(buf []byte, fn func(op uint32, body []byte)) error {
	for len(buf) >= 16 {
		size := int(binary.BigEndian.Uint32(buf[0:]))
		hlen := int(binary.BigEndian.Uint16(buf[4:]))
		ver := binary.BigEndian.Uint16(buf[6:])
		op := binary.BigEndian.Uint32(buf[8:])
		if size < hlen || size > len(buf) || hlen < 16 {
			return fmt.Errorf("packet length %d / header %d / buffer %d", size, hlen, len(buf))
		}
		body := buf[hlen:size]
		buf = buf[size:]
		if ver == 2 {
			zr, err := zlib.NewReader(bytes.NewReader(body))
			if err != nil {
				return err
			}
			inner, err := io.ReadAll(zr)
			zr.Close()
			if err != nil {
				return err
			}
			if err := eachMessage(inner, fn); err != nil {
				return err
			}
			continue
		}
		fn(op, body)
	}
	return nil
}

// parseDanmuMsg reads a DANMU_MSG notification:
// info[0] = [?, mode, font size, color, timestamp ms, …], info[1] = text, info[2] = [uid, name, …].
func parseDanmuMsg(body []byte) (platform.Danmaku, bool) {
	var m struct {
		Cmd  string            `json:"cmd"`
		Info []json.RawMessage `json:"info"`
	}
	// The cmd can carry a suffix ("DANMU_MSG:4:0:2:2:2:0") on some rooms.
	if json.Unmarshal(body, &m) != nil || !strings.HasPrefix(m.Cmd, "DANMU_MSG") || len(m.Info) < 3 {
		return platform.Danmaku{}, false
	}
	var meta []json.RawMessage
	var text string
	var user []json.RawMessage
	if json.Unmarshal(m.Info[0], &meta) != nil || json.Unmarshal(m.Info[1], &text) != nil || json.Unmarshal(m.Info[2], &user) != nil {
		return platform.Danmaku{}, false
	}
	d := platform.Danmaku{Text: text, Mode: platform.DanmakuScroll, Color: 0xFFFFFF}
	if len(meta) > 3 {
		var mode int
		var color uint32
		if json.Unmarshal(meta[1], &mode) == nil && (mode == platform.DanmakuTop || mode == platform.DanmakuBottom) {
			d.Mode = mode
		}
		if json.Unmarshal(meta[3], &color) == nil {
			d.Color = color & 0xFFFFFF
		}
	}
	if len(user) > 1 {
		_ = json.Unmarshal(user[1], &d.User)
	}
	return d, strings.TrimSpace(d.Text) != ""
}

// ---- token: buvid3 + WBI-signed getDanmuInfo ----

type danmuHost struct {
	Host    string `json:"host"`
	WssPort int    `json:"wss_port"`
}

type danmuInfoData struct {
	Token    string      `json:"token"`
	HostList []danmuHost `json:"host_list"`
}

// danmakuState caches what every room's connection shares.
var danmakuState struct {
	sync.Mutex
	buvid    string // "buvid3=…; b_nut=…" from www.bilibili.com
	mixinKey string
	mixinAt  time.Time
}

// danmakuCookie is the user's cookie when it has a buvid3, else the anonymous one plus whatever
// the user set (SESSDATA for full names).
func (r *Resolver) danmakuCookie(ctx context.Context, c *caller) (string, error) {
	if cookieValue(c.cookie, "buvid3") != "" {
		return c.cookie, nil
	}
	danmakuState.Lock()
	buvid := danmakuState.buvid
	danmakuState.Unlock()
	if buvid == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.bilibili.com/", nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", desktopUA)
		resp, err := c.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("bilibili buvid3: %w", err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var parts []string
		for _, ck := range resp.Cookies() {
			if ck.Name == "buvid3" || ck.Name == "b_nut" {
				parts = append(parts, ck.Name+"="+ck.Value)
			}
		}
		if len(parts) == 0 {
			return "", errors.New("bilibili buvid3: no cookie set")
		}
		buvid = strings.Join(parts, "; ")
		danmakuState.Lock()
		danmakuState.buvid = buvid
		danmakuState.Unlock()
	}
	if strings.TrimSpace(c.cookie) != "" {
		return c.cookie + "; " + buvid, nil
	}
	return buvid, nil
}

// wbiMixin is the WBI signing key: derived from the nav API's image names, rotated daily.
func (r *Resolver) wbiMixin(ctx context.Context, c *caller, cookie string) (string, error) {
	danmakuState.Lock()
	key, at := danmakuState.mixinKey, danmakuState.mixinAt
	danmakuState.Unlock()
	if key != "" && time.Since(at) < time.Hour {
		return key, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.bilibili.com/x/web-interface/nav", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Cookie", cookie)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("bilibili nav: %w", err)
	}
	defer resp.Body.Close()
	var nav struct {
		Data struct {
			WbiImg struct {
				ImgURL string `json:"img_url"`
				SubURL string `json:"sub_url"`
			} `json:"wbi_img"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&nav); err != nil {
		return "", fmt.Errorf("bilibili nav: %w", err)
	}
	name := func(u string) string {
		u = u[strings.LastIndex(u, "/")+1:]
		if i := strings.IndexByte(u, '.'); i >= 0 {
			u = u[:i]
		}
		return u
	}
	key = mixinKey(name(nav.Data.WbiImg.ImgURL) + name(nav.Data.WbiImg.SubURL))
	if key == "" {
		return "", errors.New("bilibili nav: no wbi keys")
	}
	danmakuState.Lock()
	danmakuState.mixinKey, danmakuState.mixinAt = key, time.Now()
	danmakuState.Unlock()
	return key, nil
}

var mixinTable = []int{46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19,
	29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21,
	56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52}

func mixinKey(raw string) string {
	if len(raw) < 64 {
		return ""
	}
	var b strings.Builder
	for _, i := range mixinTable {
		b.WriteByte(raw[i])
	}
	return b.String()[:32]
}

// wbiSign adds wts and w_rid to q: md5(sorted query + mixin key).
func wbiSign(q url.Values, mixin string, now time.Time) string {
	q.Set("wts", strconv.FormatInt(now.Unix(), 10))
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"="+strings.ReplaceAll(url.QueryEscape(q.Get(k)), "+", "%20"))
	}
	query := strings.Join(parts, "&")
	sum := md5.Sum([]byte(query + mixin))
	return query + "&w_rid=" + hex.EncodeToString(sum[:])
}

func (r *Resolver) danmuInfo(ctx context.Context, c *caller, roomID int64, cookie string) (danmuInfoData, error) {
	mixin, err := r.wbiMixin(ctx, c, cookie)
	if err != nil {
		return danmuInfoData{}, err
	}
	q := url.Values{"id": {strconv.FormatInt(roomID, 10)}, "type": {"0"}, "web_location": {"444.8"}}
	withCookie := &caller{base: c.base, client: c.client, cookie: cookie}
	data, err := withCookie.getRaw(ctx, "/xlive/web-room/v1/index/getDanmuInfo", wbiSign(q, mixin, time.Now()))
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == -352 {
			danmakuState.Lock()
			danmakuState.mixinKey = "" // signature rejected: refresh the keys next time
			danmakuState.Unlock()
		}
		return danmuInfoData{}, err
	}
	var info danmuInfoData
	if err := json.Unmarshal(data, &info); err != nil || info.Token == "" {
		return danmuInfoData{}, fmt.Errorf("bilibili getDanmuInfo: no token")
	}
	return info, nil
}

// getRaw is get with a pre-encoded query (signed queries must not be re-encoded).
func (c *caller) getRaw(ctx context.Context, path, query string) (json.RawMessage, error) {
	api := path[strings.LastIndex(path, "/")+1:]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+query, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", liveReferer)
	req.Header.Set("Origin", liveOrigin)
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bilibili %s: %w", api, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("bilibili %s: read body: %w", api, err)
	}
	return decodeEnvelope(api, resp.StatusCode, body)
}

func cookieValue(cookie, name string) string {
	for _, kv := range strings.Split(cookie, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok && k == name {
			return v
		}
	}
	return ""
}

// cookieUID is the logged-in user's id (DedeUserID), 0 when anonymous.
func cookieUID(cookie string) int64 {
	n, _ := strconv.ParseInt(cookieValue(cookie, "DedeUserID"), 10, 64)
	return n
}
