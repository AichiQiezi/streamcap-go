// Ported from streamget (MIT), https://github.com/ihmily/streamget
// (streamget/platforms/bilibili/live_stream.py), restructured for Go and the platform.Resolver contract.

// Package bilibili resolves Bilibili (B站) live rooms (https://live.bilibili.com/<room_id>).
//
// Flow: room_init (short id -> real room id, live status) -> getH5InfoByRoom (title, anchor name)
// -> getRoomPlayInfo v2 (FLV + HLS pull URLs, codec, actual qn), only when the room is live.
//
// Observed behaviour (2026-10, anonymous requests): for rooms that offer several tiers the API
// ignores the requested qn and pins anonymous viewers to one tier (usually 250 超清, sometimes 150),
// even though accept_qn lists 10000 原画. Rooms that only offer 原画 return 10000. Options.Cookie
// (a logged-in cookie containing SESSDATA) is passed through to every API call and is what the site
// uses to unlock the other tiers; StreamInfo.Quality always reflects the API's current_qn, never
// the request.
package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

const (
	defaultAPIBase = "https://api.live.bilibili.com"
	liveOrigin     = "https://live.bilibili.com"
	liveReferer    = "https://live.bilibili.com/"
	// desktopUA is sent on API calls and must also be sent by ffmpeg: the edge CDN hosts
	// (cn-xxxx-ct-01-NN.bilivideo.com) answer 403 to ffmpeg's default "Lavf/..." UA or a missing Referer.
	desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	maxBody = 8 << 20
)

func init() { platform.Register(&Resolver{}) }

// Resolver implements platform.Resolver for live.bilibili.com. The zero value is ready to use.
type Resolver struct {
	// apiBase overrides https://api.live.bilibili.com (tests only).
	apiBase string
}

func (r *Resolver) Key() string  { return "bilibili" }
func (r *Resolver) Name() string { return "B站" }

// Match reports whether raw is a bilibili.com URL. Non-room pages (videos, the live home page, …)
// match too, so the user gets ErrUnsupported from Resolve instead of "no platform".
func (r *Resolver) Match(raw string) bool {
	u, err := parseURL(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "bilibili.com" || strings.HasSuffix(h, ".bilibili.com")
}

// APIError is a non-zero "code" in a Bilibili API response, e.g. -352 / -412 (risk control) or
// 60004 (room does not exist).
type APIError struct {
	API     string
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bilibili %s: code %d: %s", e.API, e.Code, e.Message)
}

// Resolve implements platform.Resolver.
func (r *Resolver) Resolve(ctx context.Context, roomURL string, opt platform.Options) (*platform.StreamInfo, error) {
	id, err := parseRoomID(roomURL)
	if err != nil {
		return nil, err
	}
	c := &caller{base: r.apiBase, client: platform.HTTPClient(opt.Proxy), cookie: opt.Cookie}
	if c.base == "" {
		c.base = defaultAPIBase
	}

	initData, err := c.get(ctx, "/room/v1/Room/room_init", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	ri, err := parseRoomInit(initData)
	if err != nil {
		return nil, err
	}
	info := &platform.StreamInfo{
		Platform: r.Key(),
		RoomURL:  roomURL,
		RoomID:   strconv.FormatInt(ri.RoomID, 10),
	}
	live := ri.LiveStatus == 1 // 0 = offline, 2 = 轮播 (replaying VODs) — not a live broadcast

	h5Data, err := c.get(ctx, "/xlive/web-room/v1/index/getH5InfoByRoom", url.Values{"room_id": {info.RoomID}})
	if err == nil {
		var h h5Info
		if h, err = parseH5Info(h5Data); err == nil {
			info.Anchor, info.Title = h.Anchor, h.Title
		}
	}
	if err != nil {
		if !live {
			// room_init already established the room is offline; the title/name are cosmetic.
			return info, nil
		}
		return nil, err
	}
	if !live {
		return info, nil
	}

	want := qnFor(opt.Quality)
	pi, err := c.playInfo(ctx, info.RoomID, want)
	if err != nil {
		return nil, err
	}
	if pi.LiveStatus != 1 {
		return info, nil // went offline between the two calls
	}
	sel, err := selectStreams(pi, opt.PreferHLS)
	if err != nil {
		return nil, err
	}
	// The server may hand back a tier other than the one asked for. If a tier closer to the request
	// is on offer, ask for it explicitly once (helps logged-in users asking for an unlisted tier).
	if cur := sel.record.CurrentQn; cur != want {
		if best := closestQn(pi.acceptQns(), want); best != 0 && best != cur && best != want {
			if pi2, err2 := c.playInfo(ctx, info.RoomID, best); err2 == nil && pi2.LiveStatus == 1 {
				if sel2, err3 := selectStreams(pi2, opt.PreferHLS); err3 == nil {
					sel = sel2
				}
			} else if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	}

	info.IsLive = true
	info.FlvURL, info.HlsURL, info.RecordURL = sel.flvURL, sel.hlsURL, sel.recordURL
	info.Quality = qualityForQn(sel.record.CurrentQn)
	info.Codec = codecName(sel.record.CodecName)
	info.ExpiresAt = expiresAt(info.RecordURL)
	info.Headers = map[string]string{"User-Agent": desktopUA, "Referer": liveReferer}
	return info, nil
}

// ---- URL parsing ----

func parseURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	u.Host = strings.ToLower(u.Host)
	return u, nil
}

// parseRoomID extracts the (possibly short) room id from https://live.bilibili.com/<id>,
// .../h5/<id> or .../blanc/<id>, ignoring query string and fragment.
func parseRoomID(raw string) (string, error) {
	u, err := parseURL(raw)
	if err != nil || u.Hostname() != "live.bilibili.com" {
		return "", fmt.Errorf("%w: %s", platform.ErrUnsupported, raw)
	}
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	var id string
	switch {
	case len(segs) == 1:
		id = segs[0]
	case len(segs) == 2 && (segs[0] == "h5" || segs[0] == "blanc"):
		id = segs[1]
	}
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
		return "", fmt.Errorf("%w: not a live room: %s", platform.ErrUnsupported, raw)
	}
	return id, nil
}

// ---- HTTP ----

type caller struct {
	base   string
	client *http.Client
	cookie string
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

// get calls a Bilibili API and returns its "data", or an error for HTTP failures, non-JSON bodies
// and non-zero codes.
func (c *caller) get(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	api := path[strings.LastIndex(path, "/")+1:]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", liveReferer)
	req.Header.Set("Origin", liveOrigin)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
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

func decodeEnvelope(api string, status int, body []byte) (json.RawMessage, error) {
	var env envelope
	jerr := json.Unmarshal(body, &env)
	if status != http.StatusOK {
		if jerr == nil && env.Code != 0 {
			return nil, fmt.Errorf("bilibili %s: HTTP %d: %w", api, status, apiErr(api, env))
		}
		return nil, fmt.Errorf("bilibili %s: HTTP %d: %s", api, status, snippet(body))
	}
	if jerr != nil {
		return nil, fmt.Errorf("bilibili %s: bad JSON: %v: %s", api, jerr, snippet(body))
	}
	if env.Code != 0 {
		return nil, apiErr(api, env)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, fmt.Errorf("bilibili %s: empty data", api)
	}
	return env.Data, nil
}

func apiErr(api string, env envelope) *APIError {
	msg := env.Message
	if msg == "" {
		msg = env.Msg
	}
	return &APIError{API: api, Code: env.Code, Message: msg}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---- API payloads ----

type roomInit struct {
	RoomID     int64 `json:"room_id"`
	ShortID    int64 `json:"short_id"`
	UID        int64 `json:"uid"`
	LiveStatus int   `json:"live_status"`
}

func parseRoomInit(data json.RawMessage) (roomInit, error) {
	var ri roomInit
	if err := json.Unmarshal(data, &ri); err != nil {
		return ri, fmt.Errorf("bilibili room_init: %w", err)
	}
	if ri.RoomID <= 0 {
		return ri, errors.New("bilibili room_init: missing room_id")
	}
	return ri, nil
}

type h5Info struct {
	Title, Anchor string
	LiveStatus    int
}

func parseH5Info(data json.RawMessage) (h5Info, error) {
	var raw struct {
		RoomInfo struct {
			Title      string `json:"title"`
			LiveStatus int    `json:"live_status"`
		} `json:"room_info"`
		AnchorInfo struct {
			BaseInfo struct {
				Uname string `json:"uname"`
			} `json:"base_info"`
		} `json:"anchor_info"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return h5Info{}, fmt.Errorf("bilibili getH5InfoByRoom: %w", err)
	}
	if raw.AnchorInfo.BaseInfo.Uname == "" {
		return h5Info{}, errors.New("bilibili getH5InfoByRoom: missing anchor name")
	}
	return h5Info{Title: raw.RoomInfo.Title, Anchor: raw.AnchorInfo.BaseInfo.Uname, LiveStatus: raw.RoomInfo.LiveStatus}, nil
}

type codecEntry struct {
	CodecName string `json:"codec_name"`
	CurrentQn int    `json:"current_qn"`
	AcceptQn  []int  `json:"accept_qn"`
	BaseURL   string `json:"base_url"`
	URLInfo   []struct {
		Host  string `json:"host"`
		Extra string `json:"extra"`
	} `json:"url_info"`
}

type playInfo struct {
	LiveStatus int
	Streams    []struct {
		ProtocolName string `json:"protocol_name"`
		Format       []struct {
			FormatName string       `json:"format_name"`
			Codec      []codecEntry `json:"codec"`
		} `json:"format"`
	}
}

func (c *caller) playInfo(ctx context.Context, roomID string, qn int) (*playInfo, error) {
	data, err := c.get(ctx, "/xlive/web-room/v2/index/getRoomPlayInfo", url.Values{
		"room_id":  {roomID},
		"protocol": {"0,1"},   // 0 = http_stream (FLV), 1 = http_hls
		"format":   {"0,1,2"}, // flv, ts, fmp4
		"codec":    {"0,1"},   // avc, hevc
		"qn":       {strconv.Itoa(qn)},
		"platform": {"web"},
		"ptype":    {"8"},
	})
	if err != nil {
		return nil, err
	}
	return parsePlayInfo(data)
}

func parsePlayInfo(data json.RawMessage) (*playInfo, error) {
	var raw struct {
		LiveStatus  int `json:"live_status"`
		PlayurlInfo *struct {
			Playurl struct {
				Stream json.RawMessage `json:"stream"`
			} `json:"playurl"`
		} `json:"playurl_info"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("bilibili getRoomPlayInfo: %w", err)
	}
	pi := &playInfo{LiveStatus: raw.LiveStatus}
	if raw.LiveStatus != 1 {
		return pi, nil
	}
	if raw.PlayurlInfo == nil {
		return nil, errors.New("bilibili getRoomPlayInfo: room is live but no playurl_info (encrypted, paid or region-restricted room?)")
	}
	if err := json.Unmarshal(raw.PlayurlInfo.Playurl.Stream, &pi.Streams); err != nil {
		return nil, fmt.Errorf("bilibili getRoomPlayInfo: stream: %w", err)
	}
	return pi, nil
}

func (p *playInfo) acceptQns() []int {
	seen := map[int]bool{}
	var out []int
	for _, s := range p.Streams {
		for _, f := range s.Format {
			for _, c := range f.Codec {
				for _, q := range c.AcceptQn {
					if !seen[q] {
						seen[q] = true
						out = append(out, q)
					}
				}
			}
		}
	}
	return out
}

// ---- stream selection ----

type selection struct {
	flvURL, hlsURL, recordURL string
	record                    *codecEntry
}

// pickCodec prefers avc (h264) and falls back to hevc, then anything with a URL.
func pickCodec(codecs []codecEntry) *codecEntry {
	var fallback *codecEntry
	for i := range codecs {
		c := &codecs[i]
		if c.BaseURL == "" || len(c.URLInfo) == 0 {
			continue
		}
		if c.CodecName == "avc" {
			return c
		}
		if fallback == nil || (c.CodecName == "hevc" && fallback.CodecName != "hevc") {
			fallback = c
		}
	}
	return fallback
}

// find returns the preferred codec entry for protocol + one of formats (tried in order).
func (p *playInfo) find(protocol string, formats ...string) *codecEntry {
	for _, want := range formats {
		for _, s := range p.Streams {
			if s.ProtocolName != protocol {
				continue
			}
			for _, f := range s.Format {
				if f.FormatName == want {
					if c := pickCodec(f.Codec); c != nil {
						return c
					}
				}
			}
		}
	}
	return nil
}

func selectStreams(p *playInfo, preferHLS bool) (selection, error) {
	var sel selection
	flv := p.find("http_stream", "flv")
	hls := p.find("http_hls", "ts", "fmp4")
	if flv != nil {
		sel.flvURL = buildURL(flv)
	}
	if hls != nil {
		sel.hlsURL = buildURL(hls)
	}
	switch {
	case flv != nil && !(preferHLS && hls != nil):
		sel.recordURL, sel.record = sel.flvURL, flv
	case hls != nil:
		sel.recordURL, sel.record = sel.hlsURL, hls
	default:
		return sel, errors.New("bilibili getRoomPlayInfo: room is live but offers no FLV/HLS stream")
	}
	return sel, nil
}

// buildURL joins host + base_url + extra, picking the most reliable host: Bilibili's own
// "d1--cn-gotchaNN" CDN first, then edge hosts, and PCDN ("mcdn") hosts last.
func buildURL(c *codecEntry) string {
	best, bestScore := 0, -1
	for i, u := range c.URLInfo {
		s := 1
		switch {
		case strings.Contains(u.Host, "mcdn"):
			s = 0
		case strings.Contains(u.Host, "cn-gotcha"):
			s = 2
		}
		if s > bestScore {
			best, bestScore = i, s
		}
	}
	u := c.URLInfo[best]
	return u.Host + c.BaseURL + u.Extra
}

// ---- quality ----

// qnTiers are Bilibili's qn values aligned with platform.Qualities (OD … LD):
// 10000 原画, 400 蓝光, 250 超清, 150 高清, 80 流畅.
var qnTiers = []int{10000, 400, 250, 150, 80}

func qnFor(q platform.Quality) int { return qnTiers[q.Index()] }

// tierIndex maps any qn onto a platform.Qualities index (2K/4K/杜比 count as OD).
func tierIndex(qn int) int {
	for i, t := range qnTiers {
		if qn >= t {
			return i
		}
	}
	return len(qnTiers) - 1
}

func qualityForQn(qn int) platform.Quality { return platform.Qualities[tierIndex(qn)] }

// closestQn picks from accept the qn nearest to want by tier (ties go to the better tier); an exact
// match wins outright. Returns 0 when accept is empty.
func closestQn(accept []int, want int) int {
	best, bestDist := 0, 1<<30
	wi := tierIndex(want)
	for _, a := range accept {
		if a == want {
			return a
		}
		ai := tierIndex(a)
		d := ai - wi
		if d < 0 {
			d = -d
		}
		if d < bestDist || (d == bestDist && (ai < tierIndex(best) || (ai == tierIndex(best) && a == qnTiers[ai]))) {
			best, bestDist = a, d
		}
	}
	return best
}

func codecName(c string) string {
	switch c {
	case "avc":
		return "h264"
	case "hevc":
		return "h265"
	}
	return ""
}

// expiresAt reads the signed URL's expires= (unix seconds); zero when absent.
func expiresAt(raw string) time.Time {
	u, err := url.Parse(raw)
	if err != nil {
		return time.Time{}
	}
	n, err := strconv.ParseInt(u.Query().Get("expires"), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
