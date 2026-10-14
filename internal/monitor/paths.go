package monitor

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/mozillazg/go-pinyin"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

var illegal = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

// safeName makes s usable as one path component on Windows, macOS and Linux.
func safeName(s, fallback string) string {
	s = illegal.ReplaceAllString(strings.TrimSpace(s), "_")
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.So, r) || unicode.Is(unicode.Cs, r) || r == '️' || r == '‍' {
			return -1 // emoji & pictographs: legal but trouble for many tools
		}
		return r
	}, s)
	s = strings.Trim(s, " ._")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	if s == "" {
		return fallback
	}
	return s
}

var pinyinArgs = func() pinyin.Args {
	a := pinyin.NewArgs()
	a.Fallback = func(r rune, _ pinyin.Args) []string {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return []string{string(r)}
		}
		return []string{"_"}
	}
	return a
}()

// asciiName turns a display name into an ASCII-only slug (张三 → zhangsan). Never returns "".
func asciiName(s, fallback string) string {
	var b strings.Builder
	for _, part := range pinyin.LazyPinyin(s, pinyinArgs) {
		b.WriteString(part)
	}
	out := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(b.String(), "_")
	out = strings.Trim(regexp.MustCompile(`_+`).ReplaceAllString(out, "_"), "_.")
	if out == "" {
		return fallback
	}
	return out
}

// sessionPaths decides the folder and base file name for a new live session.
func sessionPaths(root string, room store.Room, info *platform.StreamInfo, s store.Settings, now time.Time) (dir, base string) {
	ascii := s.Bool("ascii_save_path")
	fallback := "room_" + shortID(room.ID)
	name := func(v string) string {
		if ascii {
			return asciiName(v, fallback)
		}
		return safeName(v, fallback)
	}
	anchor := room.Name
	if anchor == "" {
		anchor = info.Anchor
	}

	dir = root
	if s.Bool("folder_name_platform") {
		if r := platform.ByKey(room.Platform); r != nil {
			if ascii {
				dir = filepath.Join(dir, r.Key())
			} else {
				dir = filepath.Join(dir, r.Name())
			}
		}
	}
	if s.Bool("folder_name_author") {
		dir = filepath.Join(dir, name(anchor))
	}
	if s.Bool("folder_name_time") {
		dir = filepath.Join(dir, now.Format("2006-01-02"))
	}
	if s.Bool("folder_name_title") && info.Title != "" {
		dir = filepath.Join(dir, name(info.Title))
	}

	base = name(anchor) + "_" + now.Format("2006-01-02_15-04-05")
	if s.Bool("filename_includes_title") && info.Title != "" {
		base += "_" + name(info.Title)
	}
	return dir, base
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
