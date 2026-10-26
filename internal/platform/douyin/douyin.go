// Ported from streamget (MIT), https://github.com/ihmily/streamget
// Source: streamget/platforms/douyin/live_stream.py and platforms/douyin/utils.py.

// Package douyin resolves 抖音 (Douyin) live rooms.
//
// Supported URLs:
//   - https://live.douyin.com/<web_rid>[?...]                 web "room enter" API (main path)
//   - https://www.douyin.com/root/live/<web_rid>, /follow/live/<web_rid>, /live/<web_rid>
//   - https://v.douyin.com/<code>/  short share links         followed; live-room shares go through the
//     app reflow API, user shares through the user's unique_id (streamget's fetch_app_stream_data)
//   - https://www.douyin.com/user/<sec_uid>                   via the user's unique_id
package douyin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

const (
	key  = "douyin"
	name = "抖音"

	// Web room-enter API headers (streamget _get_web_stream_data).
	webUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"
	webReferer = "https://live.douyin.com/335354047186"
	// Built-in anonymous ttwid. The enter API answers 200 with an empty body when no ttwid is sent.
	defaultTTWID = "ttwid=1%7CmDcInbJ7AJ-2PGtsgrG4xj7SOiNMzePqQBF1LMO2Qkg%7C1761107324%7Cbbf97c2cd9f8eae8e8c36db4ef50c323deaa4b161179170aaf659590867c162d"

	// PC headers used for the reachability probe (streamget _get_pc_headers).
	pcUA            = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	pcDefaultCookie = "__ac_nonce=064caded4009deafd8b89"
	acceptLanguage  = "zh-CN,zh;q=0.8,zh-TW;q=0.7,zh-HK;q=0.5,en-US;q=0.3,en;q=0.2"

	// Mobile headers (streamget BaseLiveStream._get_mobile_headers / DouyinUtils.HEADERS).
	mobileUA = "Mozilla/5.0 (Linux; Android 11; SAMSUNG SM-G973U) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/14.2 Chrome/87.0.4280.141 Mobile Safari/537.36"
	// Cookie DouyinUtils sends when following share links.
	shareLinkCookie = "s_v_web_id=verify_lk07kv74_QZYCUApD_xhiB_405x_Ax51_GYO9bUIyZQVf"
	// Cookie DouyinUtils.get_unique_id sends to the iesdouyin profile page.
	profileCookie = "ttwid=1%7C4ejCkU2bKY76IySQENJwvGhg1IQZrgGEupSyTKKfuyk%7C1740470403%7Cbc9ad2ee341f1a162f9e27f4641778030d1ae91e31f9df6553a8f2efa3bdb7b4; __ac_nonce=0683e59f3009cc48fbab0; __ac_signature=_02B4Z6wo00f01mG6waQAAIDB9JUCzFb6.TZhmsUAAPBf34; __ac_referer=__ac_blank"

	probeTimeout = 10 * time.Second
	maxBody      = 8 << 20

	// noRoomData explains an empty room list. The site returns one for live types its web player
	// can't show (voice / radio-style and VR lives; the page says "该直播类型或玩法电脑端暂不支持"),
	// so there is no stream to record.
	noRoomData = "该直播类型网页端不支持（如语音、电台类或 VR 直播），只能在手机上观看，无法录制"
)

// Endpoints; tests may point them elsewhere.
var (
	webEnterAPI   = "https://live.douyin.com/webcast/room/web/enter/"
	appReflowAPI  = "https://webcast.amemv.com/webcast/room/reflow/info/"
	profilePrefix = "https://www.iesdouyin.com/share/user/"
)

type resolver struct{}

func init() { platform.Register(resolver{}) }

func (resolver) Key() string  { return key }
func (resolver) Name() string { return name }

func (resolver) Match(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "douyin.com" || strings.HasSuffix(h, ".douyin.com")
}

func (r resolver) Resolve(ctx context.Context, roomURL string, opt platform.Options) (*platform.StreamInfo, error) {
	roomURL = strings.TrimSpace(roomURL)
	u, err := url.Parse(roomURL)
	if err != nil || !r.Match(roomURL) {
		return nil, fmt.Errorf("douyin: %w: %s", platform.ErrUnsupported, roomURL)
	}
	c := &client{http: platform.HTTPClient(opt.Proxy), opt: opt}
	host := strings.ToLower(u.Hostname())
	segs := pathSegments(u.Path)

	var room *roomData
	switch {
	case host == "v.douyin.com":
		room, err = c.cached(ctx, roomURL, func() (*roomData, error) { return c.fromShareLink(ctx, roomURL) })
	case len(segs) == 2 && segs[0] == "user":
		// www.douyin.com/user/<sec_uid>: streamget fetches the page only to read the sec_uid back out of
		// the final URL; it is already in the path, so go straight to the profile lookup.
		room, err = c.cached(ctx, "sec:"+segs[1], func() (*roomData, error) { return c.fromSecUID(ctx, segs[1]) })
	default:
		webRid := webRidFromPath(host, segs)
		if webRid == "" {
			return nil, fmt.Errorf("douyin: %w: %s", platform.ErrUnsupported, roomURL)
		}
		room, err = c.fetchWeb(ctx, webRid)
	}
	if err != nil {
		return nil, err
	}
	return c.toStreamInfo(ctx, roomURL, room)
}

// Canonical implements platform.Canonicalizer. Douyin links are stored as given: a profile share
// link keeps working after the streamer changes their 抖音号, the live.douyin.com/<抖音号> it maps
// to wouldn't. Resolving once yields the identity, and warms the cache for the first check.
func (r resolver) Canonical(ctx context.Context, roomURL string, opt platform.Options) (platform.Canonical, error) {
	info, err := r.Resolve(ctx, roomURL, opt)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupported) {
			return platform.Canonical{}, &platform.UnsupportedError{Reason: "不是抖音直播间或主播主页的链接"}
		}
		return platform.Canonical{}, err
	}
	return platform.Canonical{URL: strings.TrimSpace(roomURL), Ident: info.Ident}, nil
}

func pathSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// webRidFromPath extracts the web_rid for the web API, or "" for an unsupported shape.
func webRidFromPath(host string, segs []string) string {
	if len(segs) == 0 {
		return ""
	}
	last := segs[len(segs)-1]
	if host == "live.douyin.com" {
		return last // streamget: url.split('?')[0].rsplit('/', 1)[-1]
	}
	// www.douyin.com/root/live/<id>, /follow/live/<id>, /live/<id>
	if len(segs) >= 2 && segs[len(segs)-2] == "live" {
		return last
	}
	return ""
}

// webRids remembers the web_rid (the user's unique_id) a profile link led to. Getting there costs
// following the short link and loading the profile page; afterwards a check is one API call, which
// is what makes short check intervals affordable. Live-room shares aren't stored: they name one
// broadcast, not the user.
var webRids sync.Map // share link or "sec:"+sec_uid -> web_rid

// cached resolves key through its remembered web_rid when there is one, and otherwise (or when that
// fails: the user may have changed their 抖音号) through the full lookup, remembering the result.
func (c *client) cached(ctx context.Context, key string, full func() (*roomData, error)) (*roomData, error) {
	if v, ok := webRids.Load(key); ok {
		if rd, err := c.fetchWeb(ctx, v.(string)); err == nil {
			return rd, nil
		} else if ctx.Err() != nil {
			return nil, err
		}
		webRids.Delete(key)
	}
	rd, err := full()
	if err == nil && rd.viaProfile {
		webRids.Store(key, rd.webRid)
	}
	return rd, err
}

// roomData is the normalised result of either API.
type roomData struct {
	webRid     string
	viaProfile bool // found through the user's profile: webRid is their stable unique_id
	stable     bool // webRid identifies the streamer (not just this broadcast)
	anchor     string
	room       roomJSON
}

type client struct {
	http *http.Client
	opt  platform.Options
}

// webCookie: the user's cookie when set. The enter API needs a ttwid, so one without it gets the
// built-in anonymous ttwid appended (streamget instead ignores a user cookie lacking ttwid).
func (c *client) webCookie() string {
	ck := strings.TrimSpace(c.opt.Cookie)
	switch {
	case ck == "":
		return defaultTTWID
	case strings.Contains(ck, "ttwid="):
		return ck
	default:
		return strings.TrimRight(ck, "; ") + "; " + defaultTTWID
	}
}

func (c *client) get(ctx context.Context, rawURL string, hdr map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

// fetchWeb calls the web room-enter API (streamget _get_web_stream_data).
func (c *client) fetchWeb(ctx context.Context, webRid string) (*roomData, error) {
	params := url.Values{}
	for _, p := range [][2]string{
		{"aid", "6383"}, {"app_name", "douyin_web"}, {"live_id", "1"}, {"device_platform", "web"},
		{"language", "zh-CN"}, {"browser_language", "zh-CN"}, {"browser_platform", "Win32"},
		{"browser_name", "Chrome"}, {"browser_version", "116.0.0.0"}, {"web_rid", webRid},
		{"is_need_double_stream", "false"}, {"msToken", ""},
	} {
		params.Add(p[0], p[1])
	}
	query := encodeOrdered(params, []string{"aid", "app_name", "live_id", "device_platform", "language",
		"browser_language", "browser_platform", "browser_name", "browser_version", "web_rid",
		"is_need_double_stream", "msToken"})
	api := webEnterAPI + "?" + query + "&a_bogus=" + abSign(query, webUA)

	resp, body, err := c.get(ctx, api, map[string]string{
		"User-Agent": webUA,
		"Referer":    webReferer,
		"Cookie":     c.webCookie(),
	})
	if err != nil {
		return nil, fmt.Errorf("douyin: room enter api (web_rid %s): %w", webRid, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("douyin: room enter api (web_rid %s): http %d", webRid, resp.StatusCode)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("douyin: room enter api (web_rid %s): empty response (risk control / cookie rejected)", webRid)
	}
	var r struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			Data    []roomJSON `json:"data"`
			User    *ownerJSON `json:"user"`
			Prompts string     `json:"prompts"`
			Message string     `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("douyin: room enter api (web_rid %s): decode: %w (body %q)", webRid, err, snippet(body))
	}
	if len(r.Data.Data) == 0 {
		msg := firstNonEmpty(r.Data.Prompts, r.Data.Message, noRoomData)
		return nil, fmt.Errorf("douyin: room enter api (web_rid %s): status_code %d: %s", webRid, r.StatusCode, msg)
	}
	rd := &roomData{webRid: webRid, stable: true, room: r.Data.Data[0]}
	if r.Data.User != nil {
		rd.anchor = r.Data.User.Nickname
	}
	if rd.anchor == "" && rd.room.Owner != nil {
		rd.anchor = rd.room.Owner.Nickname
	}
	return rd, nil
}

// fromShareLink follows a v.douyin.com link (streamget DouyinUtils.get_sec_user_id / get_unique_id).
func (c *client) fromShareLink(ctx context.Context, link string) (*roomData, error) {
	resp, _, err := c.get(ctx, link, map[string]string{
		"User-Agent":      mobileUA,
		"Accept-Language": acceptLanguage,
		"Cookie":          shareLinkCookie,
	})
	if err != nil {
		return nil, fmt.Errorf("douyin: follow share link %s: %w", link, err)
	}
	final := resp.Request.URL
	if strings.Contains(final.String(), "reflow/") {
		// Live-room share: .../webcast/reflow/<room_id>?...&sec_user_id=...
		secUID := final.Query().Get("sec_user_id")
		segs := pathSegments(final.Path)
		if secUID == "" || len(segs) == 0 {
			return nil, fmt.Errorf("douyin: share link %s: no room_id/sec_user_id in %s", link, final)
		}
		return c.fetchApp(ctx, segs[len(segs)-1], secUID)
	}
	// User share: the final URL ends with the sec_uid.
	segs := pathSegments(final.Path)
	if len(segs) == 0 {
		return nil, fmt.Errorf("douyin: share link %s: unexpected redirect target %s", link, final)
	}
	return c.fromSecUID(ctx, segs[len(segs)-1])
}

var uniqueIDRe = regexp.MustCompile(`unique_id":"(.*?)","verification_type`)

// fromSecUID looks up the user's unique_id on the iesdouyin profile page and resolves
// live.douyin.com/<unique_id> (streamget get_unique_id + fetch_web_stream_data).
func (c *client) fromSecUID(ctx context.Context, secUID string) (*roomData, error) {
	resp, body, err := c.get(ctx, profilePrefix+url.PathEscape(secUID), map[string]string{
		"User-Agent":      mobileUA,
		"Accept-Language": acceptLanguage,
		"Cookie":          profileCookie,
	})
	if err != nil {
		return nil, fmt.Errorf("douyin: user profile %s: %w", secUID, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("douyin: user profile %s: http %d", secUID, resp.StatusCode)
	}
	m := uniqueIDRe.FindAllSubmatch(body, -1)
	if len(m) == 0 || len(m[len(m)-1][1]) == 0 {
		return nil, fmt.Errorf("douyin: user profile %s: unique_id not found", secUID)
	}
	rd, err := c.fetchWeb(ctx, string(m[len(m)-1][1]))
	if rd != nil {
		rd.viaProfile = true
	}
	return rd, err
}

// fetchApp calls the app reflow API for a room_id + sec_user_id (streamget fetch_app_stream_data).
func (c *client) fetchApp(ctx context.Context, roomID, secUID string) (*roomData, error) {
	keys := []string{"verifyFp", "type_id", "live_id", "room_id", "sec_user_id", "version_code", "app_id", "is_need_double_stream"}
	params := url.Values{
		"verifyFp":              {"verify_lxj5zv70_7szNlAB7_pxNY_48Vh_ALKF_GA1Uf3yteoOY"},
		"type_id":               {"0"},
		"live_id":               {"1"},
		"room_id":               {roomID},
		"sec_user_id":           {secUID},
		"version_code":          {"99.99.99"},
		"app_id":                {"1128"},
		"is_need_double_stream": {"True"}, // Python urlencode of True
	}
	query := encodeOrdered(params, keys)
	api := appReflowAPI + "?" + query + "&a_bogus=" + abSign(query, mobileUA)
	resp, body, err := c.get(ctx, api, map[string]string{
		"User-Agent":      mobileUA,
		"Accept-Language": acceptLanguage,
		"Cookie":          c.opt.Cookie,
	})
	if err != nil {
		return nil, fmt.Errorf("douyin: reflow api (room %s): %w", roomID, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("douyin: reflow api (room %s): http %d", roomID, resp.StatusCode)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("douyin: reflow api (room %s): empty response (risk control)", roomID)
	}
	var r struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			Room    *roomJSON `json:"room"`
			Prompts string    `json:"prompts"`
			Message string    `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("douyin: reflow api (room %s): decode: %w (body %q)", roomID, err, snippet(body))
	}
	if r.Data.Room == nil {
		msg := firstNonEmpty(r.Data.Prompts, r.Data.Message, noRoomData)
		return nil, fmt.Errorf("douyin: reflow api (room %s): status_code %d: %s", roomID, r.StatusCode, msg)
	}
	rd := &roomData{room: *r.Data.Room}
	if o := r.Data.Room.Owner; o != nil {
		rd.anchor, rd.webRid = o.Nickname, o.WebRid
	}
	rd.stable = rd.webRid != ""
	if rd.webRid == "" {
		rd.webRid = roomID // this broadcast's id: fine to show, useless as an identity
	}
	return rd, nil
}

func (c *client) toStreamInfo(ctx context.Context, roomURL string, rd *roomData) (*platform.StreamInfo, error) {
	info := &platform.StreamInfo{
		Platform: key,
		RoomURL:  roomURL,
		RoomID:   rd.webRid,
		Anchor:   rd.anchor,
	}
	if rd.stable {
		info.Ident = rd.webRid
	}
	if rd.room.Status != statusLive {
		return info, nil
	}
	info.Title = rd.room.Title
	t, err := buildTiers(rd.room.StreamURL)
	if err != nil {
		return nil, fmt.Errorf("douyin: %s: %w", roomURL, err)
	}
	sel, err := selectTier(t, c.opt.Quality, func(u string) (bool, error) { return c.reachable(ctx, u) })
	if err != nil {
		return nil, fmt.Errorf("douyin: %s: %w", roomURL, err)
	}
	fillLive(info, sel, c.opt)
	return info, nil
}

// reachable is streamget's get_response_status check: a HEAD request that counts as reachable whenever
// any HTTP response comes back (streamget treats every status code as truthy; only a transport error
// triggers the fallback). A cancelled ctx is returned as an error rather than as "unreachable".
func (c *client) reachable(ctx context.Context, u string) (bool, error) {
	if u == "" {
		return false, nil
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodHead, u, nil)
	if err != nil {
		return false, nil
	}
	cookie := c.opt.Cookie
	if cookie == "" {
		cookie = pcDefaultCookie
	}
	req.Header.Set("User-Agent", pcUA)
	req.Header.Set("Accept-Language", acceptLanguage)
	req.Header.Set("Referer", "https://live.douyin.com/")
	req.Header.Set("Cookie", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	resp.Body.Close()
	return true, nil
}

// encodeOrdered url-encodes params in the given key order (like Python's urlencode of a dict), which
// matters because a_bogus signs the exact query string.
func encodeOrdered(v url.Values, keys []string) string {
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(v.Get(k)))
	}
	return b.String()
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func snippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

var _ platform.Resolver = resolver{}
