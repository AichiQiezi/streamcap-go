package monitor

import (
	"context"
	"strings"
	"sync"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

// Preview describes a link before it is added: whose room it is, whether they are live, and
// whether the list already has them.
type Preview struct {
	URL          string `json:"url"`      // as given
	Platform     string `json:"platform"` // resolver key; "" when unsupported
	Anchor       string `json:"anchor"`
	Title        string `json:"title"`
	IsLive       bool   `json:"is_live"`
	Existing     string `json:"existing,omitempty"` // id of the room already in the list
	ExistingName string `json:"existing_name,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Previews looks several links up at once (each platform's concurrency limit still applies).
func (m *Manager) Previews(ctx context.Context, urls []string) []Preview {
	out := make([]Preview, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			out[i] = m.preview(ctx, u)
		}(i, u)
	}
	wg.Wait()
	return out
}

func (m *Manager) preview(ctx context.Context, u string) Preview {
	u = strings.TrimSpace(u)
	p := Preview{URL: u}
	res := platform.Find(u)
	if res == nil {
		p.Error = "暂不支持该平台（目前支持抖音、B站）"
		return p
	}
	p.Platform = res.Key()
	c, err := m.canonical(res, u)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	ix := newRoomIndex(m.roomsSnapshot())
	if dup := ix.find(res.Key(), c, ""); dup != nil {
		p.Existing, p.ExistingName = dup.ID, dup.Name
	}
	info, err := m.resolve(ctx, store.Room{URL: c.URL, Platform: res.Key(), Quality: platform.QualityOD})
	if err != nil {
		p.Error = "暂时获取不到直播间信息：" + shortErr(err)
		return p
	}
	p.Anchor, p.Title, p.IsLive = info.Anchor, info.Title, info.IsLive
	if p.Existing == "" && info.Ident != "" { // a share link may only reveal the streamer here
		if dup := ix.find(res.Key(), platform.Canonical{URL: c.URL, Ident: info.Ident}, ""); dup != nil {
			p.Existing, p.ExistingName = dup.ID, dup.Name
		}
	}
	return p
}
