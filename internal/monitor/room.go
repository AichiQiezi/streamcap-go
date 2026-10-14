package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/media"
	"github.com/AichiQiezi/streamcap-go/internal/notify"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/recorder"
	"github.com/AichiQiezi/streamcap-go/internal/store"
	"github.com/AichiQiezi/streamcap-go/internal/sysutil"
)

// Phase is the room's display state.
type Phase string

const (
	PhaseRecording    Phase = "recording"
	PhaseReconnecting Phase = "reconnecting" // stream dropped mid-live; getting a fresh URL
	PhaseLive         Phase = "live"         // live but not recording (notify-only / paused by user / no disk)
	PhaseChecking     Phase = "checking"
	PhaseOffline      Phase = "offline"
	PhaseScheduled    Phase = "scheduled" // outside the daily monitoring window
	PhaseError        Phase = "error"
	PhasePaused       Phase = "paused" // monitoring off
)

// Detail codes refine a phase for the UI.
const (
	DetailCheckError   = "CHECK_ERROR"
	DetailRecordError  = "RECORD_ERROR"
	DetailNoSpace      = "NO_SPACE"
	DetailNotifyOnly   = "NOTIFY_ONLY"
	DetailRecordPaused = "RECORD_PAUSED"
)

// Tunables for the supervisor loop.
var (
	// offlineConfirmations is how many consecutive "not live" answers end a session. A single
	// "not live" right after a drop is common (the site lags behind the CDN), so one is not enough.
	offlineConfirmations = 3
	// sessionGrace: if a session has had no working part for this long, end it even without
	// confirmations (e.g. the site keeps erroring).
	sessionGrace = 5 * time.Minute
	// handoffLead: switch to a freshly resolved URL this long before the current one expires.
	handoffLead = 60 * time.Second
	thumbEvery  = 30 * time.Second
)

// State is the runtime state shown in the UI.
type State struct {
	Phase      Phase
	Detail     string
	LastError  string
	IsLive     bool
	LiveTitle  string
	CheckedAt  time.Time
	Reconnects int // drops recovered in the current session

	SessionStart time.Time
	Recorded     time.Duration // finished parts of the current session
	PartStart    time.Time     // zero unless a part is running
	Dir          string

	File         string
	Bytes        int64 // bytes written in the current session
	SpeedBps     float64
	Width        int
	Height       int
	FPS          float64
	Codec        string
	ThumbVersion int64
}

// Duration is how long the current session has recorded so far.
func (s State) Duration() time.Duration {
	d := s.Recorded
	if !s.PartStart.IsZero() {
		d += time.Since(s.PartStart)
	}
	return d
}

type session struct {
	start     time.Time
	dir       string
	base      string
	format    string
	segmented bool
	parts     int
	files     []string
	bytes     int64 // finished parts
	recorded  time.Duration
	lastAlive time.Time // last time a part was writing data
	anchor    string
	title     string
	notified  bool
	key       string // journal key (crash recovery)
}

type runtime struct {
	m  *Manager
	id string

	mu    sync.Mutex
	st    State
	sess  *session
	part  *recorder.Part
	thumb struct {
		busy   bool
		at     time.Time
		probed string
	}

	ctlMu        sync.Mutex
	cancel       context.CancelFunc
	done         chan struct{}
	wake         chan struct{}
	stopRec      chan struct{}
	recordPaused bool // user stopped this live's recording; cleared when the live ends
	liveNotified bool // notify-only rooms: start pushed for the current live
}

func newRuntime(m *Manager, id string) *runtime {
	rt := &runtime{m: m, id: id, wake: make(chan struct{}, 1), stopRec: make(chan struct{}, 1)}
	rt.st.Phase = PhasePaused
	if _, err := os.Stat(m.ThumbPath(id)); err == nil {
		if fi, err := os.Stat(m.ThumbPath(id)); err == nil {
			rt.st.ThumbVersion = fi.ModTime().Unix()
		}
	}
	return rt
}

func (rt *runtime) start() {
	rt.ctlMu.Lock()
	defer rt.ctlMu.Unlock()
	if rt.done != nil {
		select {
		case <-rt.done:
		default:
			return // already running
		}
	}
	ctx, cancel := context.WithCancel(rt.m.shutdownCtx)
	rt.cancel, rt.done = cancel, make(chan struct{})
	go rt.loop(ctx, rt.done)
}

func (rt *runtime) stop() {
	rt.ctlMu.Lock()
	defer rt.ctlMu.Unlock()
	if rt.cancel != nil {
		rt.cancel()
	}
}

func (rt *runtime) wait(ctx context.Context) {
	rt.ctlMu.Lock()
	done := rt.done
	rt.ctlMu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (rt *runtime) poke() {
	select {
	case rt.wake <- struct{}{}:
	default:
	}
}

func (rt *runtime) pauseRecording() {
	rt.ctlMu.Lock()
	rt.recordPaused = true
	rt.ctlMu.Unlock()
	select {
	case rt.stopRec <- struct{}{}:
	default:
	}
}

func (rt *runtime) resumeRecording() {
	rt.ctlMu.Lock()
	rt.recordPaused = false
	rt.ctlMu.Unlock()
	rt.poke()
}

func (rt *runtime) isRecordPaused() bool {
	rt.ctlMu.Lock()
	defer rt.ctlMu.Unlock()
	return rt.recordPaused
}

func (rt *runtime) snapshot() State {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.st
}

func (rt *runtime) update(fn func(*State)) {
	rt.mu.Lock()
	fn(&rt.st)
	rt.mu.Unlock()
	rt.m.emit(Event{Type: EventRoom, ID: rt.id})
}

// sleep waits for d, an early wake-up (check now / settings change), or cancellation.
func (rt *runtime) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-rt.wake:
		return true
	case <-t.C:
		return true
	}
}

func jitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int63n(int64(d/5)+1)) - d/10
}

func backoff(n int, base, max time.Duration) time.Duration {
	d := base << (n - 1)
	if n <= 0 || d > max || d <= 0 {
		return max
	}
	return d
}

// loop is the room's supervisor. It owns rt.sess and rt.part.
func (rt *runtime) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	defer func() {
		rt.endSession("监控已停止")
		rt.update(func(s *State) {
			*s = State{Phase: PhasePaused, ThumbVersion: s.ThumbVersion, Width: s.Width, Height: s.Height, Codec: s.Codec}
		})
	}()

	var checkErrs, offlineHits, quickFails int
	for ctx.Err() == nil {
		room, ok := rt.m.room(rt.id)
		if !ok || !room.Monitor {
			return
		}
		s := rt.m.Settings()
		interval := time.Duration(clampInt(s.Int("loop_time_seconds", 120), 20, 3600)) * time.Second

		if !room.InSchedule(time.Now()) {
			rt.endSession("超出定时监控时段")
			rt.update(func(st *State) { st.Phase, st.IsLive, st.Detail = PhaseScheduled, false, "" })
			rt.sleep(ctx, time.Minute)
			continue
		}

		if rt.sess == nil && rt.snapshot().Phase != PhaseLive {
			rt.update(func(st *State) { st.Phase = PhaseChecking })
		}
		info, err := rt.m.resolve(ctx, room)
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		if err != nil {
			checkErrs++
			msg := "检测失败：" + shortErr(err)
			slog.Warn("resolve failed", "room", room.URL, "attempt", checkErrs, "err", err)
			if rt.sess != nil {
				// Mid-live: an error says nothing about whether the stream ended. Keep retrying fast.
				if now.Sub(rt.sess.lastAlive) > sessionGrace {
					rt.endSession("长时间无法恢复")
				} else {
					rt.update(func(st *State) { st.Phase, st.LastError, st.CheckedAt = PhaseReconnecting, msg, now })
					rt.sleep(ctx, backoff(checkErrs, 3*time.Second, 30*time.Second))
					continue
				}
			}
			rt.update(func(st *State) {
				st.LastError, st.CheckedAt = msg, now
				if checkErrs >= 3 {
					st.Phase, st.Detail = PhaseError, DetailCheckError
				} else if st.Phase == PhaseChecking {
					st.Phase = PhaseOffline
				}
			})
			rt.sleep(ctx, minDur(interval, backoff(checkErrs, 15*time.Second, 2*time.Minute)))
			continue
		}
		checkErrs = 0
		if !room.CustomName {
			rt.m.renameIfAuto(rt.id, info.Anchor)
		}

		if !info.IsLive {
			if rt.sess != nil {
				offlineHits++
				if offlineHits < offlineConfirmations && now.Sub(rt.sess.lastAlive) < sessionGrace {
					rt.update(func(st *State) {
						st.Phase, st.CheckedAt = PhaseReconnecting, now
						st.LastError = fmt.Sprintf("平台返回未开播，确认中（%d/%d）", offlineHits, offlineConfirmations)
					})
					rt.sleep(ctx, time.Duration(5*offlineHits)*time.Second)
					continue
				}
				rt.endSession("直播已结束")
			}
			offlineHits = 0
			rt.ctlMu.Lock()
			rt.recordPaused, rt.liveNotified = false, false
			rt.ctlMu.Unlock()
			rt.update(func(st *State) {
				st.Phase, st.IsLive, st.Detail, st.CheckedAt, st.LiveTitle, st.LastError = PhaseOffline, false, "", now, "", ""
			})
			rt.sleep(ctx, jitter(interval))
			continue
		}

		offlineHits = 0
		rt.update(func(st *State) { st.IsLive, st.LiveTitle, st.CheckedAt = true, info.Title, now })

		// Live, but not to be recorded right now.
		if reason := rt.skipReason(room, info); reason != "" {
			rt.endSession("已停止录制")
			rt.ctlMu.Lock()
			first := !rt.liveNotified
			rt.liveNotified = true
			rt.ctlMu.Unlock()
			if first && room.Push && reason == DetailNotifyOnly {
				go notify.Send(context.Background(), s, notify.Event{Start: true, Anchor: displayName(room, info), Title: info.Title, URL: room.URL})
			}
			phase := PhaseLive
			if reason == DetailNoSpace {
				phase = PhaseError
			}
			rt.update(func(st *State) { st.Phase, st.Detail = phase, reason })
			rt.sleep(ctx, jitter(interval))
			continue
		}

		if rt.sess == nil {
			rt.beginSession(room, info, s)
		}
		res := rt.recordParts(ctx, room, info, s)
		if ctx.Err() != nil {
			return
		}
		switch res.Reason {
		case recorder.ReasonStopped:
			continue // user paused recording; the loop re-checks and shows "live"
		default:
			lasted := res.Ended.Sub(res.Started)
			if lasted < 20*time.Second {
				quickFails++
			} else {
				quickFails = 0
			}
			msg := describe(res)
			slog.Info("part ended, reconnecting", "room", room.URL, "reason", res.Reason, "lasted", lasted.Round(time.Second), "detail", msg)
			rt.update(func(st *State) {
				st.Phase, st.LastError, st.Detail = PhaseReconnecting, msg, ""
				st.Reconnects++
			})
			if quickFails > 0 {
				rt.sleep(ctx, backoff(quickFails, 2*time.Second, 30*time.Second))
			}
		}
	}
}

func (rt *runtime) skipReason(room store.Room, _ *platform.StreamInfo) string {
	switch {
	case room.NotifyOnly:
		return DetailNotifyOnly
	case rt.isRecordPaused():
		return DetailRecordPaused
	}
	free, _, err := sysutil.DiskFree(rt.m.roomRoot(room))
	if err == nil && float64(free) < rt.m.thresholdBytes() {
		return DetailNoSpace
	}
	return ""
}

func (rt *runtime) beginSession(room store.Room, info *platform.StreamInfo, s store.Settings) {
	now := time.Now()
	dir, base := sessionPaths(rt.m.roomRoot(room), room, info, s, now)
	rt.sess = &session{
		start: now, dir: dir, base: base, format: room.Format,
		segmented: room.Segment && room.SegmentSec > 0, lastAlive: now,
		anchor: displayName(room, info), title: info.Title,
	}
	j := store.SessionJournal{RoomID: rt.id, Anchor: rt.sess.anchor, Dir: dir, Base: base, Format: room.Format,
		Segmented: rt.sess.segmented, Start: now}
	rt.sess.key = journalKey(rt.id, j)
	rt.m.journalPut(rt.sess.key, j)
	rt.update(func(st *State) {
		st.SessionStart, st.Recorded, st.Bytes, st.Reconnects, st.Dir, st.LastError = now, 0, 0, 0, dir, ""
	})
	if room.Push {
		rt.sess.notified = true
		go notify.Send(context.Background(), s, notify.Event{Start: true, Anchor: rt.sess.anchor, Title: info.Title, URL: room.URL})
	}
}

// endSession closes the current session: finalisation (merge/remux) runs in the background.
func (rt *runtime) endSession(why string) {
	sess := rt.sess
	if sess == nil {
		return
	}
	rt.sess = nil
	rt.update(func(st *State) {
		st.SessionStart, st.Recorded, st.PartStart, st.SpeedBps, st.File = time.Time{}, 0, time.Time{}, 0, ""
	})
	room, _ := rt.m.room(rt.id)
	s := rt.m.Settings()
	slog.Info("session ended", "room", room.URL, "why", why, "parts", sess.parts, "recorded", sess.recorded.Round(time.Second))
	if sess.notified {
		go notify.Send(context.Background(), s, notify.Event{Start: false, Anchor: sess.anchor, URL: room.URL, Recorded: sess.recorded})
	}
	if len(sess.files) == 0 {
		rt.m.journalDelete(sess.key)
		return
	}
	opt := recorder.FinalizeOptions{
		Merge:          s.Bool("merge_sessions"),
		ConvertToMP4:   s.Bool("convert_to_mp4"),
		DeleteOriginal: s.Bool("delete_original"),
	}
	rt.m.finalizeWG.Add(1)
	go func() {
		defer rt.m.finalizeWG.Done()
		out := recorder.Finalize(context.Background(), sess.files, sess.format, sess.segmented, opt)
		rt.m.journalDelete(sess.key)
		if len(out) == 1 {
			rt.m.toast("「%s」本场录制已保存（%s，%d 段%s）", sess.anchor, sess.recorded.Round(time.Second), sess.parts, mergedNote(sess.parts, opt.Merge && !sess.segmented))
		} else if len(out) > 1 {
			rt.m.toast("「%s」本场录制已保存为 %d 个文件", sess.anchor, len(out))
		}
	}()
}

func mergedNote(parts int, merged bool) string {
	if parts > 1 && merged {
		return "，已合并"
	}
	return ""
}

// recordParts records from info until the stream ends, the user stops, or ctx is cancelled.
// Before a signed URL expires it starts a part on a fresh URL and only then stops the old one,
// so the hand-over leaves no gap.
func (rt *runtime) recordParts(ctx context.Context, room store.Room, info *platform.StreamInfo, s store.Settings) recorder.Result {
	part, err := rt.startPart(room, info, s)
	if err != nil {
		now := time.Now()
		return recorder.Result{Reason: recorder.ReasonError, Detail: err.Error(), Started: now, Ended: now}
	}
	expiry := expiryTimer(info)
	for {
		select {
		case <-part.Done():
			return rt.finishPart(part)
		case <-ctx.Done():
			part.Stop(recorder.ReasonStopped)
			return rt.finishPart(part)
		case <-rt.stopRec:
			part.Stop(recorder.ReasonStopped)
			return rt.finishPart(part)
		case <-expiry:
			fresh, err := rt.m.resolve(ctx, room)
			if err != nil || !fresh.IsLive || fresh.RecordURL == "" {
				expiry = time.After(20 * time.Second) // try again; the old part keeps going meanwhile
				continue
			}
			next, err := rt.startPart(room, fresh, s)
			if err != nil {
				expiry = time.After(20 * time.Second)
				continue
			}
			waitForData(ctx, next, 15*time.Second)
			part.Stop(recorder.ReasonHandoff)
			rt.finishPart(part)
			part, info = next, fresh
			rt.setPart(part)
			expiry = expiryTimer(info)
			slog.Info("handed over to fresh stream url", "room", room.URL)
		}
	}
}

func expiryTimer(info *platform.StreamInfo) <-chan time.Time {
	if info.ExpiresAt.IsZero() {
		return nil
	}
	d := time.Until(info.ExpiresAt) - handoffLead
	if d < 15*time.Second {
		d = 15 * time.Second
	}
	return time.After(d)
}

func waitForData(ctx context.Context, p *recorder.Part, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if p.Progress().Bytes > 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-p.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (rt *runtime) startPart(room store.Room, info *platform.StreamInfo, s store.Settings) (*recorder.Part, error) {
	sess := rt.sess
	sess.parts++
	rt.m.journalPut(sess.key, store.SessionJournal{RoomID: rt.id, Anchor: sess.anchor, Dir: sess.dir, Base: sess.base,
		Format: sess.format, Segmented: sess.segmented, Parts: sess.parts, Start: sess.start})
	base := sess.base
	if sess.parts > 1 {
		base = fmt.Sprintf("%s_p%d", sess.base, sess.parts)
	}
	proxy := rt.m.proxyFor(room.Platform, s)
	job := &recorder.Job{
		URL: info.RecordURL, Headers: info.Headers, Proxy: proxy, Format: room.Format,
		Segment: sess.segmented, SegmentSec: room.SegmentSec, BitrateK: room.BitrateK,
		Dir: sess.dir, BaseName: base,
	}
	part, err := recorder.Start(job, func(p recorder.Progress) { rt.onProgress(p) })
	if err != nil {
		return nil, err
	}
	rt.setPart(part)
	slog.Info("recording", "room", room.URL, "quality", info.Quality, "part", sess.parts, "dir", sess.dir)
	return part, nil
}

func (rt *runtime) setPart(p *recorder.Part) {
	rt.mu.Lock()
	rt.part = p
	rt.mu.Unlock()
	rt.update(func(st *State) {
		st.Phase, st.Detail, st.PartStart = PhaseRecording, "", p.Started()
	})
}

// finishPart waits for p and folds its result into the session.
func (rt *runtime) finishPart(p *recorder.Part) recorder.Result {
	res := p.Wait()
	sess := rt.sess
	if sess != nil {
		sess.files = append(sess.files, res.Files...)
		sess.bytes += res.Bytes
		if res.Bytes > 0 {
			sess.recorded += res.Ended.Sub(res.Started)
			sess.lastAlive = res.Ended
		}
	}
	rt.mu.Lock()
	if rt.part == p {
		rt.part = nil
	}
	rt.mu.Unlock()
	rt.update(func(st *State) {
		if sess != nil {
			st.Recorded, st.Bytes = sess.recorded, sess.bytes
		}
		st.PartStart, st.SpeedBps = time.Time{}, 0
		if res.Reason == recorder.ReasonError && res.Bytes == 0 {
			st.Detail = DetailRecordError
		}
	})
	return res
}

func (rt *runtime) onProgress(p recorder.Progress) {
	rt.mu.Lock()
	var sessBytes int64
	if rt.sess != nil {
		sessBytes = rt.sess.bytes
		if p.Bytes > 0 {
			rt.sess.lastAlive = time.Now()
		}
	}
	needProbe := p.File != "" && p.File != rt.thumb.probed && p.Bytes > 512<<10
	if needProbe {
		rt.thumb.probed = p.File
	}
	needThumb := p.File != "" && !rt.thumb.busy && p.Bytes > 256<<10 &&
		(time.Since(rt.thumb.at) > thumbEvery || rt.st.ThumbVersion == 0)
	if needThumb {
		rt.thumb.busy, rt.thumb.at = true, time.Now()
	}
	rt.mu.Unlock()

	rt.update(func(st *State) {
		st.File, st.Bytes, st.SpeedBps = p.File, sessBytes+p.Bytes, p.SpeedBps
	})
	if needProbe {
		go func(file string) {
			info, err := media.Probe(context.Background(), file)
			if err != nil {
				rt.mu.Lock()
				rt.thumb.probed = "" // retry on the next sample
				rt.mu.Unlock()
				return
			}
			rt.update(func(st *State) { st.Width, st.Height, st.FPS, st.Codec = info.Width, info.Height, info.FPS, info.Codec })
		}(p.File)
	}
	if needThumb {
		go func(file string) {
			err := media.Thumbnail(context.Background(), file, rt.m.ThumbPath(rt.id))
			rt.mu.Lock()
			rt.thumb.busy = false
			rt.mu.Unlock()
			if err == nil {
				rt.update(func(st *State) { st.ThumbVersion = time.Now().UnixMilli() })
			}
		}(p.File)
	}
}

func describe(res recorder.Result) string {
	switch res.Reason {
	case recorder.ReasonEOF:
		return "直播流被平台断开（链接过期或 CDN 切换），已自动重连"
	case recorder.ReasonStalled:
		return "长时间没有收到数据，已自动重连"
	case recorder.ReasonError:
		if res.Detail != "" {
			return "录制出错：" + lastLine(res.Detail)
		}
		return fmt.Sprintf("录制出错（ffmpeg 退出码 %d）", res.ExitCode)
	default:
		return string(res.Reason)
	}
}

func displayName(room store.Room, info *platform.StreamInfo) string {
	if room.Name != "" {
		return room.Name
	}
	if info != nil && info.Anchor != "" {
		return info.Anchor
	}
	return "直播间"
}

func shortErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时"
	}
	return lastLine(err.Error())
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := lines[len(lines)-1]
	if r := []rune(l); len(r) > 140 {
		l = string(r[:140]) + "…"
	}
	return l
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
