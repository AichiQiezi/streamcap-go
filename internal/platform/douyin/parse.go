// Ported from streamget (MIT), https://github.com/ihmily/streamget
// Source: streamget/platforms/douyin/live_stream.py (_get_web_stream_data, fetch_app_stream_data,
// fetch_stream_url) — response parsing and quality-tier selection.

package douyin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// statusLive is the room status douyin reports while broadcasting (streamget: status == 2 → live;
// anything else, normally 4, is offline).
const statusLive = 2

// roomJSON is the room object shared by the web enter API (data.data[0]) and the app reflow API
// (data.room).
type roomJSON struct {
	IDStr     string         `json:"id_str"`
	Status    int            `json:"status"`
	Title     string         `json:"title"`
	Owner     *ownerJSON     `json:"owner"`
	StreamURL *streamURLJSON `json:"stream_url"`
}

type ownerJSON struct {
	Nickname string `json:"nickname"`
	WebRid   string `json:"web_rid"`
}

type streamURLJSON struct {
	// Both are JSON objects whose key order is the tier order (FULL_HD1, HD1, SD1, SD2), so they are
	// decoded with orderedStrings rather than into a Go map.
	FlvPullURL      json.RawMessage `json:"flv_pull_url"`
	HlsPullURLMap   json.RawMessage `json:"hls_pull_url_map"`
	LiveCoreSDKData struct {
		PullData struct {
			StreamData string `json:"stream_data"`
		} `json:"pull_data"`
	} `json:"live_core_sdk_data"`
}

type kv struct{ Key, Val string }

// orderedStrings decodes a JSON object of string values preserving key order. null/empty → nil.
func orderedStrings(raw json.RawMessage) ([]kv, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected JSON object, got %v", tok)
	}
	var out []kv
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		s, _ := v.(string)
		out = append(out, kv{k, s})
	}
	return out, nil
}

// prependOrigin mimics Python's {**{'ORIGIN': origin}, **m}: ORIGIN first, and an ORIGIN key already in
// m overwrites the value but keeps the first position.
func prependOrigin(origin string, m []kv) []kv {
	out := []kv{{"ORIGIN", origin}}
	for _, e := range m {
		if e.Key == "ORIGIN" {
			out[0].Val = e.Val
			continue
		}
		out = append(out, e)
	}
	return out
}

// streamDataJSON is live_core_sdk_data.pull_data.stream_data (itself a JSON string).
type streamDataJSON struct {
	Data map[string]struct {
		Main struct {
			Flv       string          `json:"flv"`
			Hls       string          `json:"hls"`
			SDKParams json.RawMessage `json:"sdk_params"`
		} `json:"main"`
	} `json:"data"`
}

func parseSDKParams(raw json.RawMessage) (vcodec string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		raw = []byte(s)
	}
	var p struct {
		VCodec string `json:"VCodec"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	return p.VCodec
}

// tiers is the parsed set of pull URLs for a live room.
type tiers struct {
	flv, hls []kv
	// codecByPath maps a pull URL path (no host/query) to the codec the SDK data reports for it.
	codecByPath map[string]string
}

// buildTiers ports the stream_orientation == 1 branch of _get_web_stream_data / fetch_app_stream_data:
// take the ORIGIN tier from stream_data and put it in front of flv_pull_url / hls_pull_url_map.
//
// Deviation: streamget raises when stream_data has no origin entry; here the room is still recorded
// from the remaining tiers.
func buildTiers(su *streamURLJSON) (*tiers, error) {
	if su == nil {
		return nil, errors.New("live room has no stream_url")
	}
	flv, err := orderedStrings(su.FlvPullURL)
	if err != nil {
		return nil, fmt.Errorf("parse flv_pull_url: %w", err)
	}
	hls, err := orderedStrings(su.HlsPullURLMap)
	if err != nil {
		return nil, fmt.Errorf("parse hls_pull_url_map: %w", err)
	}
	t := &tiers{codecByPath: map[string]string{}}
	if sdStr := su.LiveCoreSDKData.PullData.StreamData; sdStr != "" && !strings.HasPrefix(sdStr, "$") {
		var sd streamDataJSON
		if err := json.Unmarshal([]byte(sdStr), &sd); err == nil {
			for _, v := range sd.Data {
				c := strings.ToLower(parseSDKParams(v.Main.SDKParams))
				if c == "" {
					continue
				}
				for _, u := range []string{v.Main.Flv, v.Main.Hls} {
					if p := urlPath(u); p != "" {
						t.codecByPath[p] = c
					}
				}
			}
			if o, ok := sd.Data["origin"]; ok && (o.Main.Flv != "" || o.Main.Hls != "") {
				codec := parseSDKParams(o.Main.SDKParams)
				// Exactly as streamget: url + '&codec=' + VCodec (even when VCodec is empty).
				flv = prependOrigin(o.Main.Flv+"&codec="+codec, flv)
				hls = prependOrigin(o.Main.Hls+"&codec="+codec, hls)
			}
		}
	}
	t.flv, t.hls = dropEmpty(flv), dropEmpty(hls)
	if len(t.flv) == 0 && len(t.hls) == 0 {
		return nil, errors.New("live room reports no pull urls")
	}
	return t, nil
}

func dropEmpty(in []kv) []kv {
	out := in[:0:0]
	for _, e := range in {
		if strings.TrimSpace(e.Val) != "" && !strings.HasPrefix(e.Val, "&codec=") {
			out = append(out, e)
		}
	}
	return out
}

func urlPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

// pad5 repeats the last element until there are five tiers (streamget's padding).
func pad5(in []kv) []kv {
	if len(in) == 0 {
		return nil
	}
	out := append([]kv(nil), in...)
	for len(out) < 5 {
		out = append(out, out[len(out)-1])
	}
	return out
}

// tierQuality maps douyin's tier names onto platform qualities.
var tierQuality = map[string]platform.Quality{
	"ORIGIN":   platform.QualityOD,
	"FULL_HD1": platform.QualityUHD,
	"HD1":      platform.QualityHD,
	"SD1":      platform.QualitySD,
	"SD2":      platform.QualityLD,
}

// fallbackIndex is streamget's "next tier" rule when the chosen URL is unreachable.
func fallbackIndex(i int) int {
	if i < 4 {
		return i + 1
	}
	return i - 1
}

// selection is the outcome of picking a tier.
type selection struct {
	flv, hls string
	quality  platform.Quality
	codec    string
}

// selectTier ports fetch_stream_url: pad both lists to 5, index by the requested quality, probe the HLS
// URL (FLV when there is no HLS) and on failure move to fallbackIndex. probe returns false only when
// the URL could not be reached; a non-nil error aborts (context cancelled).
func selectTier(t *tiers, q platform.Quality, probe func(string) (bool, error)) (*selection, error) {
	flv, hls := pad5(t.flv), pad5(t.hls)
	pick := func(list []kv, i int) kv {
		if len(list) == 0 {
			return kv{}
		}
		return list[i]
	}
	idx := q.Index()
	f, h := pick(flv, idx), pick(hls, idx)
	probeURL := h.Val
	if probeURL == "" {
		probeURL = f.Val
	}
	ok, err := probe(probeURL)
	if err != nil {
		return nil, err
	}
	if !ok {
		idx = fallbackIndex(idx)
		f, h = pick(flv, idx), pick(hls, idx)
	}

	name := f.Key
	if name == "" {
		name = h.Key
	}
	quality, known := tierQuality[name]
	if !known {
		// Unknown tier name: use the position among the real (unpadded) tiers.
		n := len(t.flv)
		if n == 0 {
			n = len(t.hls)
		}
		pos := idx
		if pos > n-1 {
			pos = n - 1
		}
		quality = platform.Qualities[pos]
	}
	return &selection{flv: f.Val, hls: h.Val, quality: quality, codec: codecOf(t, f.Val, h.Val)}, nil
}

// codecOf prefers the codec= query param (set on ORIGIN), then the SDK data for the URL's path.
func codecOf(t *tiers, urls ...string) string {
	for _, u := range urls {
		if c := queryParam(u, "codec"); c != "" {
			return strings.ToLower(c)
		}
	}
	for _, u := range urls {
		if c := t.codecByPath[urlPath(u)]; c != "" {
			return c
		}
	}
	return ""
}

func queryParam(raw, key string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}

// fillLive sets the stream fields of info from a selection.
func fillLive(info *platform.StreamInfo, sel *selection, opt platform.Options) {
	info.IsLive = true
	info.Quality = sel.quality
	info.FlvURL = sel.flv
	info.HlsURL = sel.hls
	info.Codec = sel.codec
	switch {
	case sel.flv == "":
		info.RecordURL = sel.hls
	case sel.hls != "" && opt.PreferHLS:
		info.RecordURL = sel.hls
	case sel.hls != "" && sel.codec == "h265":
		// ffmpeg's FLV demuxer can't take douyin's h265-in-FLV; StreamCap switches to HLS too.
		info.RecordURL = sel.hls
	default:
		info.RecordURL = sel.flv
	}
	info.ExpiresAt = expiry(info.RecordURL)
}

// expiry reads the signed-URL expiry from a douyin pull URL. The CDNs in use encode it differently:
//   - expire=1792508841 (decimal unix seconds) or expire=6ad78561 (8 hex digits, douyinliving.com)
//   - k=<sig>&t=1792508839 (decimal unix seconds)
//   - wsTime=6ace4ae3&keeptime=00093a80 (hex start + hex lifetime)
//
// Zero when none is present or the value is implausible.
func expiry(raw string) time.Time {
	u, err := url.Parse(raw)
	if err != nil {
		return time.Time{}
	}
	q := u.Query()
	var sec int64
	switch {
	case q.Get("expire") != "":
		sec = parseUnix(q.Get("expire"))
	case q.Get("k") != "" && q.Get("t") != "":
		sec, _ = strconv.ParseInt(q.Get("t"), 10, 64)
	case q.Get("wsTime") != "" && q.Get("keeptime") != "":
		start, err1 := strconv.ParseInt(q.Get("wsTime"), 16, 64)
		keep, err2 := strconv.ParseInt(q.Get("keeptime"), 16, 64)
		if err1 == nil && err2 == nil {
			sec = start + keep
		}
	}
	// Plausibility: after 2020-01-01 and within ~10 years.
	if sec < 1577836800 || sec > time.Now().Unix()+10*365*24*3600 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// parseUnix accepts decimal seconds, or exactly 8 hex digits (a decimal 8-digit timestamp would be 1973).
func parseUnix(v string) int64 {
	if len(v) == 8 {
		if n, err := strconv.ParseInt(v, 16, 64); err == nil {
			return n
		}
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}
