package platform

import (
	"regexp"
	"strings"
)

// urlRE finds links in prose. App share messages wrap them in text — 抖音: "7- 长按复制此条消息，打开
// 抖音搜索，查看TA的更多作品。 https://v.douyin.com/xxx/ 1@4.com :5pm", B站: "【xx的直播间】 https://b23.tv/xxx"
// — so a link ends at whitespace or CJK punctuation (mirrors web/src/lib/platforms.ts).
var urlRE = regexp.MustCompile(`https?://[^\s"'<>，。！？、；【】（）()「」]+`)

// ExtractLinks returns the supported live-room links in text, deduplicated, in order.
func ExtractLinks(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range urlRE.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:!?)")
		if seen[u] || Find(u) == nil {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}
