package douyin

import "regexp"

// Douyin's chat emoticons arrive as [name] codes the page swaps for images. Players can't show
// those, so the common ones become the closest emoji; unknown codes stay as they are (a viewer may
// have typed brackets on purpose).
var emoticons = map[string]string{
	"微笑": "🙂", "大笑": "😄", "呲牙": "😁", "偷笑": "🤭", "憨笑": "😊", "笑哭": "😂", "捂脸": "🤦",
	"害羞": "😳", "可爱": "🥰", "色": "😍", "爱慕": "😍", "得意": "😏", "奸笑": "😏", "酷拽": "😎",
	"惊呆": "😮", "震惊": "😱", "恐惧": "😨", "石化": "😶", "思考": "🤔", "疑问": "❓", "what": "❓",
	"流泪": "😢", "快哭了": "🥺", "可怜": "🥺", "泪奔": "😭", "泣不成声": "😭", "大哭": "😭",
	"发怒": "😠", "抓狂": "😫", "翻白眼": "🙄", "撇嘴": "😒", "黑线": "😓", "晕": "😵", "衰": "😩",
	"睡": "😴", "困": "😪", "哈欠": "🥱", "闭嘴": "🤐", "嘘": "🤫", "吐血": "🤮", "抠鼻": "🤭",
	"机智": "🤓", "耶": "✌️", "胜利": "✌️", "赞": "👍", "强": "👍", "鼓掌": "👏", "小鼓掌": "👏",
	"握手": "🤝", "抱拳": "🙏", "祈祷": "🙏", "比心": "🫶", "送心": "💗", "爱心": "❤️", "心碎": "💔",
	"玫瑰": "🌹", "礼物": "🎁", "红包": "🧧", "钱": "💰", "666": "6️⃣6️⃣6️⃣", "太阳": "☀️", "月亮": "🌙",
	"吻": "😘", "kiss": "😘", "打脸": "🤕", "加油": "💪", "灵光一闪": "💡", "来看我": "👀",
}

var emoticonCode = regexp.MustCompile(`\[([^\[\]]{1,6})\]`)

func emojiText(s string) string {
	return emoticonCode.ReplaceAllStringFunc(s, func(code string) string {
		if e, ok := emoticons[code[1:len(code)-1]]; ok {
			return e
		}
		return code
	})
}
