package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/recorder"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

func journalKey(roomID string, j store.SessionJournal) string {
	return roomID + "@" + j.Start.Format("20060102T150405")
}

func (m *Manager) journalPut(key string, j store.SessionJournal) {
	m.st.Sessions.Update(func(s *map[string]store.SessionJournal) { (*s)[key] = j })
}

func (m *Manager) journalDelete(key string) {
	m.st.Sessions.Update(func(s *map[string]store.SessionJournal) { delete(*s, key) })
}

// recoverSessions finalises sessions a previous run didn't finish (killed, crashed, shut down
// before merging completed). Runs in the background at startup.
func (m *Manager) recoverSessions() {
	var pending map[string]store.SessionJournal
	m.st.Sessions.Read(func(s map[string]store.SessionJournal) {
		pending = make(map[string]store.SessionJournal, len(s))
		for k, v := range s {
			pending[k] = v
		}
	})
	if len(pending) == 0 {
		return
	}
	s := m.Settings()
	opt := recorder.FinalizeOptions{
		Merge: s.Bool("merge_sessions"), ConvertToMP4: s.Bool("convert_to_mp4"), DeleteOriginal: s.Bool("delete_original"),
	}
	for key, j := range pending {
		files := journalFiles(j)
		if !settled(files) {
			// Something (e.g. an ffmpeg we couldn't stop) is still writing: leave it for next start.
			slog.Warn("session files still growing, recovery postponed", "anchor", j.Anchor, "dir", j.Dir)
			continue
		}
		slog.Info("recovering unfinished session", "anchor", j.Anchor, "dir", j.Dir, "files", len(files))
		m.finalizeWG.Add(1)
		go func(key string, j store.SessionJournal, files []string) {
			defer m.finalizeWG.Done()
			if len(files) > 0 {
				out := recorder.Finalize(context.Background(), files, j.Format, j.Segmented, opt)
				if len(out) > 0 {
					m.toast("已整理上次未完成的录制：「%s」%s", j.Anchor, j.Start.Format("01-02 15:04"))
				}
			}
			m.journalDelete(key)
		}(key, j, files)
	}
}

// settled reports whether none of files changed size over a few seconds.
func settled(files []string) bool {
	size := func() (n int64) {
		for _, f := range files {
			if st, err := os.Stat(f); err == nil {
				n += st.Size()
			}
		}
		return n
	}
	before := size()
	time.Sleep(3 * time.Second)
	return size() == before
}

// journalFiles lists the files a journaled session produced, in recording order.
func journalFiles(j store.SessionJournal) []string {
	ext := "." + recorder.Ext(j.Format)
	var out []string
	for n := 1; n <= j.Parts; n++ {
		base := j.Base
		if n > 1 {
			base = fmt.Sprintf("%s_p%d", j.Base, n)
		}
		if j.Segmented {
			matches, _ := filepath.Glob(filepath.Join(j.Dir, globEscape(base)+"_*"+ext))
			seg := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `_\d{3}` + regexp.QuoteMeta(ext) + `$`)
			sort.Strings(matches)
			for _, f := range matches {
				if seg.MatchString(filepath.Base(f)) {
					out = append(out, f)
				}
			}
			continue
		}
		f := filepath.Join(j.Dir, base+ext)
		if st, err := os.Stat(f); err == nil && st.Size() > 0 {
			out = append(out, f)
		}
	}
	return out
}

func globEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)
	return r.Replace(s)
}
