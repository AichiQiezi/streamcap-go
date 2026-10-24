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

	"github.com/AichiQiezi/streamcap-go/internal/danmaku"
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
	DetailResumeWait   = "RESUME_WAIT" // off air, waiting to see if the streamer comes back
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
	// confirmStep: the n-th confirmation of "not live" waits n*confirmStep (5s, 10s).
	confirmStep = 5 * time.Second
	// intervalFloor is the shortest check interval settings may ask for; intervalUnit is the unit
	// of loop_time_seconds (tests shrink both).
	intervalFloor = 20 * time.Second
	intervalUnit  = time.Second
	// resumeUnit is the unit of the resume_wait_minutes setting (tests shrink it).
	resumeUnit = time.Minute
	// sendAlert pushes 开播/下播 messages (tests count them instead).
	sendAlert = notify.Send
	// handoffLead: switch to a freshly resolved URL this long before the current one expires.
	handoffLead = 60 * time.Second
	thumbEvery  = 30 * time.Second
	// liveThumbEvery: how often a live room that isn't recording gets a fresh cover. With no file
	// to take it from, each one is a short pull of the stream at the lowest quality.
	liveThumbEvery = 60 * time.Second
	// grabStreamThumb takes that cover (tests fake it: no ffmpeg).
	grabStreamThumb = func(ctx context.Context, info *platform.StreamInfo, proxy, dest string) error {
		return media.StreamThumbnail(ctx, info.RecordURL, info.Headers, proxy, dest)
	}
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

	// DanmakuOn: this session records comments; Danmaku is how many it has so far.
	DanmakuOn bool
	Danmaku   int
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
	key       string // journal key (crash recovery)

	dm      *dmRecorder // comments, when recorded (see danmaku.go)
	dmParts []partFiles // each part's files and data start, to line the comments up
}

// dataTimes records when a part's file first and last grew, on the local clock.
type dataTimes struct {
	mu          sync.Mutex
	first, last time.Time
	bytes       int64
}

func (f *dataTimes) mark(p recorder.Progress) {
	f.mu.Lock()
	if p.Bytes > f.bytes {
		now := time.Now()
		if f.first.IsZero() {
			f.first = now
		}
		f.last, f.bytes = now, p.Bytes
	}
	f.mu.Unlock()
}

func (f *dataTimes) get() (first, last time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.first, f.last
}

type runtime struct {
	m  *Manager
	id string

	mu     sync.Mutex
	st     State
	sess   *session
	part   *recorder.Part
	firsts map[*recorder.Part]*dataTimes // per part: a URL hand-over overlaps two parts
	thumb  struct {
		busy   bool
		at     time.Time
		probed string
		cancel context.CancelFunc // a cover being pulled from the stream (liveThumb)
		failed bool               // liveThumb's last pull failed (and that was logged)
	}

	ctlMu        sync.Mutex
	cancel       context.CancelFunc
	done         chan struct{}
	wake         chan struct{}
	stopRec      chan struct{}
	recordPaused bool // user stopped this live's recording; cleared when the live ends
	recordOnce   bool // user asked to record this live of a notify-only room; cleared when it ends

	// watch is the broadcast the room is in, from the first check that saw it live until it is over
	// (only touched by the loop).
	watch *liveWatch
	// endingAt is set once the platform has confirmed "off air": the 场 is closed at that time unless
	// the streamer comes back first (only touched by the loop).
	endingAt time.Time
}

// liveWatch is one broadcast. Alerts hang off it rather than off recording sessions: within one
// live, recording can start and stop (录这一场, 停止本次录制, a mode switch) without announcing the
// start again or the end early.
type liveWatch struct {
	since     time.Time
	lastLive  time.Time // last check that saw it live: the end, for the 下播 alert's duration
	anchor    string
	announced bool          // the 开播 alert went out; the 下播 alert pairs with it
	recorded  time.Duration // recorded across this live's sessions
}

// resumeWait is how long a confirmed "off air" may last and still count as the same 场.
func resumeWait(s store.Settings) time.Duration {
	return time.Duration(clampInt(s.Int("resume_wait_minutes", 10), 0, 120)) * resumeUnit
}

// alerts reports whether the room wants 开播/下播 messages: its own switch, or notify-only mode,
// which exists for nothing else.
func alerts(room store.Room) bool { return room.Push || room.NotifyOnly }

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
	rt.recordPaused, rt.recordOnce = true, false
	rt.ctlMu.Unlock()
	select {
	case rt.stopRec <- struct{}{}:
	default:
	}
}

// resumeRecording (re)starts recording this live. For a notify-only room that means this live only:
// the room stays notify-only, and recordOnce is dropped when the live ends.
func (rt *runtime) resumeRecording() {
	rt.ctlMu.Lock()
	rt.recordPaused, rt.recordOnce = false, true
	rt.ctlMu.Unlock()
	rt.poke()
}

func (rt *runtime) isRecordOnce() bool {
	rt.ctlMu.Lock()
	defer rt.ctlMu.Unlock()
	return rt.recordOnce
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
		interval := max(time.Duration(clampInt(s.Int("loop_time_seconds", 120), 1, 3600))*intervalUnit, intervalFloor)

		if !room.InSchedule(time.Now()) {
			rt.closeSession("超出定时监控时段")
			rt.update(func(st *State) { st.Phase, st.IsLive, st.Detail = PhaseScheduled, false, "" })
			rt.sleep(ctx, time.Minute)
			continue
		}

		// Only the first check shows 检测中. Re-checks keep the last answer on screen: flipping every
		// room to 检测中 and back each interval made the cards blink and re-sort.
		if rt.sess == nil && rt.snapshot().CheckedAt.IsZero() {
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
			if !rt.endingAt.IsZero() && now.After(rt.endingAt) {
				rt.finishLive(room, s) // the wait ran out; errors don't extend it
			}
			if rt.sess != nil && rt.endingAt.IsZero() {
				// Mid-live: an error says nothing about whether the stream ended. Keep retrying fast.
				if now.Sub(rt.sess.lastAlive) > sessionGrace {
					rt.closeSession("长时间无法恢复")
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
		rt.m.learn(rt.id, info.Anchor, info.Ident)

		if !info.IsLive {
			if rt.watch != nil || rt.sess != nil {
				if rt.endingAt.IsZero() {
					// One "not live" right after a drop is common (the site lags behind the CDN), and
					// believing it would end the recording, or announce the end and then a second start.
					offlineHits++
					inGrace := rt.sess == nil || now.Sub(rt.sess.lastAlive) < sessionGrace
					if offlineHits < offlineConfirmations && inGrace {
						rt.update(func(st *State) {
							st.CheckedAt = now
							if rt.sess != nil {
								st.Phase = PhaseReconnecting
								st.LastError = fmt.Sprintf("平台返回未开播，确认中（%d/%d）", offlineHits, offlineConfirmations)
							}
						})
						rt.sleep(ctx, time.Duration(offlineHits)*confirmStep)
						continue
					}
					// Off air for sure. Streamers often come back within minutes (app restart, network
					// switch): until the wait is over that continues this 场 — same recording session,
					// no second 开播 alert, 录这一场 still on.
					if wait := resumeWait(s); wait > 0 {
						rt.endingAt = now.Add(wait)
						slog.Info("off air, waiting for a resume", "room", room.URL, "until", rt.endingAt.Format("15:04:05"))
						rt.update(func(st *State) {
							st.Phase, st.IsLive, st.Detail, st.CheckedAt, st.LiveTitle, st.LastError = PhaseOffline, false, DetailResumeWait, now, "", ""
							st.SpeedBps = 0
						})
						rt.sleep(ctx, jitter(interval))
						continue
					}
				} else if now.Before(rt.endingAt) {
					rt.update(func(st *State) { st.CheckedAt = now })
					rt.sleep(ctx, jitter(interval))
					continue
				}
				rt.finishLive(room, s)
			}
			offlineHits = 0
			rt.update(func(st *State) {
				st.Phase, st.IsLive, st.Detail, st.CheckedAt, st.LiveTitle, st.LastError = PhaseOffline, false, "", now, "", ""
			})
			rt.sleep(ctx, jitter(interval))
			continue
		}

		offlineHits = 0
		if !rt.endingAt.IsZero() {
			slog.Info("back on air within the wait: same 场", "room", room.URL)
			rt.endingAt = time.Time{}
			if rt.sess != nil {
				rt.sess.lastAlive = now // the wait isn't a stall
			}
		}
		rt.update(func(st *State) { st.IsLive, st.LiveTitle, st.CheckedAt, st.Detail = true, info.Title, now, "" })
		if rt.watch == nil {
			rt.watch = &liveWatch{since: now, anchor: displayName(room, info)}
			// The cover on file is from an earlier live: don't show it as this one's picture.
			rt.mu.Lock()
			rt.thumb.at = time.Time{}
			rt.mu.Unlock()
			rt.update(func(st *State) { st.ThumbVersion = 0 })
		}
		rt.watch.lastLive = now
		if w := rt.watch; !w.announced && alerts(room) {
			w.announced = true
			go sendAlert(context.Background(), s, notify.Event{Start: true, Anchor: w.anchor, Title: info.Title, URL: room.URL})
		}

		// Live, but not to be recorded right now.
		if reason := rt.skipReason(room, info); reason != "" {
			rt.closeSession("已停止录制")
			phase := PhaseLive
			if reason == DetailNoSpace {
				phase = PhaseError
			}
			rt.update(func(st *State) { st.Phase, st.Detail = phase, reason })
			rt.liveThumb(ctx, room, s)
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
	case room.NotifyOnly && !rt.isRecordOnce():
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
	rt.mu.Lock()
	if rt.thumb.cancel != nil {
		rt.thumb.cancel() // the recorder must not share the stream with a cover pull
	}
	rt.mu.Unlock()
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
	rt.sess.dm = rt.startDanmaku(room, s, dir, base)
}

// closeSession ends the recording session, if any, counting its time towards the live.
func (rt *runtime) closeSession(why string) {
	if d := rt.endSession(why); rt.watch != nil {
		rt.watch.recorded += d
	}
}

// finishLive closes the 场: its last recording session, the 下播 alert, and the switches that last
// one live (录这一场, 停止本次录制).
func (rt *runtime) finishLive(room store.Room, s store.Settings) {
	rt.closeSession("直播已结束")
	if w := rt.watch; w != nil && w.announced {
		go sendAlert(context.Background(), s, notify.Event{Start: false, Anchor: w.anchor, URL: room.URL,
			Live: w.lastLive.Sub(w.since), Recorded: w.recorded})
	}
	rt.watch, rt.endingAt = nil, time.Time{}
	rt.ctlMu.Lock()
	rt.recordPaused, rt.recordOnce = false, false
	rt.ctlMu.Unlock()
}

// endSession closes the current session and returns how long it recorded. Alerts are the loop's
// business (see liveWatch): a session also ends while the live goes on.
func (rt *runtime) endSession(why string) time.Duration {
	sess := rt.sess
	if sess == nil {
		return 0
	}
	rt.sess = nil
	rt.update(func(st *State) {
		st.SessionStart, st.Recorded, st.PartStart, st.SpeedBps, st.File = time.Time{}, 0, time.Time{}, 0, ""
		st.DanmakuOn, st.Danmaku = false, 0
	})
	room, _ := rt.m.room(rt.id)
	s := rt.m.Settings()
	slog.Info("session ended", "room", room.URL, "why", why, "parts", sess.parts, "recorded", sess.recorded.Round(time.Second))
	if sess.dm != nil {
		sess.dm.stop()
	}
	if len(sess.files) == 0 {
		rt.m.journalDelete(sess.key)
		return sess.recorded
	}
	opt := recorder.FinalizeOptions{
		Merge:          s.Bool("merge_sessions"),
		ConvertToMP4:   s.Bool("convert_to_mp4"),
		DeleteOriginal: s.Bool("delete_original"),
	}
	rt.m.finalizeWG.Add(1)
	snap := rt.snapshot()
	go func() {
		defer rt.m.finalizeWG.Done()
		ctx := context.Background()
		// Measure the parts before finalising: merging can delete them.
		var windows [][]danmaku.Window
		if sess.dm != nil && sess.dm.w.Count() > 0 {
			windows = fileWindows(ctx, sess.dmParts)
		}
		out := recorder.Finalize(ctx, sess.files, sess.format, sess.segmented, opt)
		rt.m.journalDelete(sess.key)
		note := ""
		if windows != nil {
			if n := writeDanmakuASS(sess.dm.path, windows, out, snap.Width, snap.Height); n > 0 {
				note = fmt.Sprintf("，弹幕 %d 条", n)
			}
		}
		if rt.m.onSaved != nil && len(out) > 0 {
			rt.m.onSaved(SavedSession{Room: room, Anchor: sess.anchor, Title: sess.title, Start: sess.start, Files: out})
		}
		if len(out) == 1 {
			rt.m.toast("「%s」本场录制已保存（%s，%d 段%s%s）", sess.anchor, sess.recorded.Round(time.Second), sess.parts, mergedNote(sess.parts, opt.Merge && !sess.segmented), note)
		} else if len(out) > 1 {
			rt.m.toast("「%s」本场录制已保存为 %d 个文件%s", sess.anchor, len(out), note)
		}
	}()
	return sess.recorded
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
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
	first := &dataTimes{}
	part, err := recorder.Start(job, func(p recorder.Progress) {
		first.mark(p)
		rt.onProgress(p)
	})
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	if rt.firsts == nil {
		rt.firsts = map[*recorder.Part]*dataTimes{}
	}
	rt.firsts[part] = first
	rt.mu.Unlock()
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
	rt.mu.Lock()
	first := rt.firsts[p]
	delete(rt.firsts, p)
	rt.mu.Unlock()
	sess := rt.sess
	if sess != nil {
		if len(res.Files) > 0 {
			pf := partFiles{files: res.Files, dataAt: res.Started, endAt: res.Ended}
			if first != nil {
				if f, l := first.get(); !f.IsZero() {
					// Progress is sampled every couple of seconds; a part that stalled before ending
					// stopped getting data at its last growth, not when ffmpeg was finally stopped.
					pf.dataAt, pf.endAt = f, minTime(res.Ended, l.Add(recorder.SampleEvery))
				}
			}
			sess.dmParts = append(sess.dmParts, pf)
		}
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

// liveThumb refreshes, at most every liveThumbEvery, the cover of a room that is live but not
// recording: from a lowest-quality resolve of its own, so the check's URL (which 录这一场 records)
// is never pulled by anyone else. Starting a recording cancels it.
func (rt *runtime) liveThumb(ctx context.Context, room store.Room, s store.Settings) {
	rt.mu.Lock()
	if rt.thumb.busy || time.Since(rt.thumb.at) < liveThumbEvery {
		rt.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	rt.thumb.busy, rt.thumb.at, rt.thumb.cancel = true, time.Now(), cancel
	rt.mu.Unlock()

	go func() {
		defer cancel()
		low := room
		low.Quality = platform.QualityLD
		info, err := rt.m.resolve(ctx, low)
		if err == nil && !info.IsLive {
			err = fmt.Errorf("not live")
		}
		if err == nil {
			err = grabStreamThumb(ctx, info, rt.m.proxyFor(room.Platform, s), rt.m.ThumbPath(rt.id))
		}
		rt.mu.Lock()
		rt.thumb.busy, rt.thumb.cancel = false, nil
		logIt := err != nil && ctx.Err() == nil && !rt.thumb.failed // once per run of failures
		rt.thumb.failed = err != nil
		rt.mu.Unlock()
		if logIt {
			slog.Warn("live cover failed", "room", room.URL, "err", err)
		}
		if err != nil {
			return
		}
		rt.update(func(st *State) { st.ThumbVersion = time.Now().UnixMilli() })
	}()
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
