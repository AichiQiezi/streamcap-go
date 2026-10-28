package platform_test

import (
	"reflect"
	"testing"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
	_ "github.com/AichiQiezi/streamcap-go/internal/platform/bilibili"
	_ "github.com/AichiQiezi/streamcap-go/internal/platform/douyin"
)

func TestExtractLinks(t *testing.T) {
	cases := map[string][]string{
		"7- 长按复制此条消息，打开抖音搜索，查看TA的更多作品。 https://v.douyin.com/_aaXw7U1SeA/ 1@4.com :5pm": {
			"https://v.douyin.com/_aaXw7U1SeA/"},
		"【LofiGirl的小号的直播间】 https://b23.tv/5piv15g，快来看": {"https://b23.tv/5piv15g"},
		"两个：https://live.douyin.com/646916955331 和 https://live.bilibili.com/1775716505。": {
			"https://live.douyin.com/646916955331", "https://live.bilibili.com/1775716505"},
		"重复 https://live.douyin.com/1 https://live.douyin.com/1": {"https://live.douyin.com/1"},
		"别的网站 https://www.baidu.com/s?wd=直播 不算":                  nil,
		"没有链接的普通文字":                                              nil,
	}
	for text, want := range cases {
		if got := platform.ExtractLinks(text); !reflect.DeepEqual(got, want) {
			t.Errorf("ExtractLinks(%q) = %q, want %q", text, got, want)
		}
	}
}
