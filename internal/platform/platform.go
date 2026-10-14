// Package platform resolves a live-room URL into the room's live status and pullable stream URLs.
//
// Each supported site (douyin, bilibili) implements Resolver. Resolvers are stateless and safe for
// concurrent use; per-call inputs (quality, cookie, proxy) come in through Options.
package platform

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Quality is the requested recording quality, coarsest to finest mapped onto each site's own tiers.
type Quality string

const (
	QualityOD  Quality = "OD"  // 原画 (source)
	QualityUHD Quality = "UHD" // 超清
	QualityHD  Quality = "HD"  // 高清
	QualitySD  Quality = "SD"  // 标清
	QualityLD  Quality = "LD"  // 流畅
)

// Qualities lists every quality from best to worst.
var Qualities = []Quality{QualityOD, QualityUHD, QualityHD, QualitySD, QualityLD}

// Label is the Chinese display name of a quality.
func (q Quality) Label() string {
	switch q {
	case QualityUHD:
		return "超清"
	case QualityHD:
		return "高清"
	case QualitySD:
		return "标清"
	case QualityLD:
		return "流畅"
	default:
		return "原画"
	}
}

// Index is the quality's position in Qualities (OD=0 … LD=4); unknown values map to OD.
func (q Quality) Index() int {
	for i, v := range Qualities {
		if v == q {
			return i
		}
	}
	return 0
}

// Options are the per-call inputs to Resolve.
type Options struct {
	Quality Quality
	// Cookie is the raw Cookie header the user configured for this site ("" = anonymous).
	Cookie string
	// Proxy is an optional http(s) proxy URL used for API calls (the recorder passes it to ffmpeg too).
	Proxy string
	// PreferHLS asks for the HLS URL as RecordURL when both are available (default is FLV).
	PreferHLS bool
}

// StreamInfo is the result of resolving a room.
type StreamInfo struct {
	Platform string // resolver key, e.g. "douyin"
	RoomURL  string // the URL that was resolved
	RoomID   string // site room id, when known
	Anchor   string // streamer display name
	Title    string // current live title
	IsLive   bool
	// The fields below are set only when IsLive is true.
	Quality   Quality           // the tier actually selected (may differ from the request when unavailable)
	FlvURL    string            // FLV pull URL, if offered
	HlsURL    string            // HLS (m3u8) pull URL, if offered
	RecordURL string            // the URL the recorder should pull: FlvURL or HlsURL per Options.PreferHLS / codec
	Headers   map[string]string // extra HTTP headers ffmpeg must send when pulling RecordURL (may be nil)
	Codec     string            // "h264" / "h265" when the site reports it, else ""
	// ExpiresAt is when the CDN will cut RecordURL (signed-URL expiry), zero when unknown. The recorder
	// re-resolves before it instead of letting the stream drop.
	ExpiresAt time.Time
}

// Resolver is implemented by each supported site.
type Resolver interface {
	Key() string  // stable id: "douyin", "bilibili"
	Name() string // display name: "抖音", "B站"
	// Match reports whether url belongs to this site.
	Match(url string) bool
	// Resolve fetches the room's current state. A room that is simply offline returns
	// (&StreamInfo{IsLive: false, ...}, nil). A non-nil error means the state is UNKNOWN (network
	// failure, rate limit, anti-bot, parse failure) — callers must not treat it as "offline".
	Resolve(ctx context.Context, roomURL string, opt Options) (*StreamInfo, error)
}

// ErrUnsupported is returned when a URL shape is recognised as the site but cannot be handled.
var ErrUnsupported = errors.New("unsupported room url")

var registry []Resolver

// Register adds a resolver; called from each site package's init.
func Register(r Resolver) { registry = append(registry, r) }

// All returns the registered resolvers.
func All() []Resolver { return registry }

// Find returns the resolver for url, or nil.
func Find(url string) Resolver {
	u := strings.TrimSpace(url)
	for _, r := range registry {
		if r.Match(u) {
			return r
		}
	}
	return nil
}

// ByKey returns the resolver with the given key, or nil.
func ByKey(key string) Resolver {
	for _, r := range registry {
		if r.Key() == key {
			return r
		}
	}
	return nil
}

// HTTPClient builds the client resolvers should use: bounded timeouts, optional proxy.
func HTTPClient(proxy string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		if u, err := parseProxy(proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}
