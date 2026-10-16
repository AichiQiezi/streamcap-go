// Package monitor runs one supervisor goroutine per monitored room: it detects when the room goes
// live, records it, reconnects through drops with a fresh URL, and closes the session when the live
// has really ended.
package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
	"github.com/AichiQiezi/streamcap-go/internal/sysutil"
)

// EventType distinguishes manager events.
type EventType int

const (
	EventRoom    EventType = iota // a room's data or state changed (ID set)
	EventRemoved                  // rooms deleted (IDs set)
	EventToast                    // a message for the user (Text set)
	EventQuitting                 // graceful shutdown started
	EventOrder                    // the room list was reordered (IDs: the new order)
)

// Event is delivered to subscribers. Handlers must not block.
type Event struct {
	Type EventType
	ID   string
	IDs  []string
	Text string
}

// Options configure the manager.
type Options struct {
	DefaultSaveDir string // used when settings.live_save_path is empty
	ThumbDir       string
}

// View is a room plus its live runtime state.
type View struct {
	Room  store.Room
	State State
}

// Manager owns all rooms.
type Manager struct {
	st   *store.Store
	opts Options

	mu       sync.Mutex
	runtimes map[string]*runtime
	subs     []func(Event)
	sems     map[string]chan struct{} // per-platform resolve concurrency
	semSize  int

	// roomsMu serialises Add and Edit end to end. Both look a link up on the network between
	// checking for duplicates and writing, and two adds of one streamer must not both pass the check.
	roomsMu sync.Mutex

	diskMu      sync.Mutex
	diskAt      time.Time
	diskFree    uint64
	diskTotal   uint64
	finalizeWG  sync.WaitGroup
	shutdownCtx context.Context
	shutdown    context.CancelFunc
}

// New creates the manager. Call Start to begin monitoring.
func New(st *store.Store, opts Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{st: st, opts: opts, runtimes: map[string]*runtime{}, sems: map[string]chan struct{}{},
		shutdownCtx: ctx, shutdown: cancel}
	_ = os.MkdirAll(opts.ThumbDir, 0o755)
	for _, room := range m.roomsSnapshot() {
		m.runtimes[room.ID] = newRuntime(m, room.ID)
	}
	return m
}

// Start finishes sessions a previous run left behind, then launches supervisors for every monitored
// room, staggered so startup doesn't burst the sites.
func (m *Manager) Start() {
	go m.recoverSessions()
	i := 0
	for _, room := range m.roomsSnapshot() {
		if room.Monitor {
			delay := time.Duration(i) * 1500 * time.Millisecond
			id := room.ID
			time.AfterFunc(delay, func() { m.startRuntime(id) })
			i++
		}
	}
}

// Shutdown stops every recording gracefully and waits (bounded by ctx) for files to be finalised.
func (m *Manager) Shutdown(ctx context.Context) {
	m.emit(Event{Type: EventQuitting})
	m.mu.Lock()
	rts := make([]*runtime, 0, len(m.runtimes))
	for _, rt := range m.runtimes {
		rts = append(rts, rt)
	}
	m.mu.Unlock()
	for _, rt := range rts {
		rt.stop()
	}
	for _, rt := range rts {
		rt.wait(ctx)
	}
	done := make(chan struct{})
	go func() { m.finalizeWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("shutdown: some sessions were not finalised in time; their part files are kept as-is")
	}
	m.shutdown()
	m.st.FlushAll()
}

// Subscribe registers an event handler.
func (m *Manager) Subscribe(fn func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

func (m *Manager) emit(ev Event) {
	m.mu.Lock()
	subs := append([]func(Event){}, m.subs...)
	m.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func (m *Manager) toast(format string, args ...any) {
	m.emit(Event{Type: EventToast, Text: fmt.Sprintf(format, args...)})
}

// ---- queries -------------------------------------------------------------------------------

func (m *Manager) roomsSnapshot() []store.Room {
	var out []store.Room
	m.st.Rooms.Read(func(rs []store.Room) { out = append(out, rs...) })
	return out
}

func (m *Manager) room(id string) (store.Room, bool) {
	var r store.Room
	ok := false
	m.st.Rooms.Read(func(rs []store.Room) {
		for _, x := range rs {
			if x.ID == id {
				r, ok = x, true
				return
			}
		}
	})
	return r, ok
}

// Views returns every room with its state, in insertion order.
func (m *Manager) Views() []View {
	rooms := m.roomsSnapshot()
	out := make([]View, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, View{Room: r, State: m.stateOf(r)})
	}
	return out
}

// View returns one room with its state.
func (m *Manager) View(id string) (View, bool) {
	r, ok := m.room(id)
	if !ok {
		return View{}, false
	}
	return View{Room: r, State: m.stateOf(r)}, true
}

func (m *Manager) stateOf(r store.Room) State {
	m.mu.Lock()
	rt := m.runtimes[r.ID]
	m.mu.Unlock()
	if rt == nil {
		return State{Phase: PhasePaused}
	}
	st := rt.snapshot()
	switch {
	case !r.Monitor && st.Phase != PhaseRecording:
		st.Phase = PhasePaused
	case r.Monitor && st.Phase == PhasePaused:
		st.Phase = PhaseChecking // supervisor just started and hasn't reported yet
	}
	return st
}

// Settings returns a copy of the current settings.
func (m *Manager) Settings() store.Settings {
	var s store.Settings
	m.st.Settings.Read(func(v store.Settings) { s = v.Clone() })
	return s
}

// UpdateSettings merges values into settings.
func (m *Manager) UpdateSettings(values map[string]any) {
	m.st.Settings.Update(func(s *store.Settings) {
		for k, v := range values {
			(*s)[k] = v
		}
	})
	m.diskMu.Lock()
	m.diskAt = time.Time{} // re-measure against the new path/threshold right away
	m.diskMu.Unlock()
	m.mu.Lock()
	m.sems = map[string]chan struct{}{} // pick up a new concurrency limit
	m.mu.Unlock()
	m.wakeAll()
}

// Cookies returns a copy of the per-platform cookies.
func (m *Manager) Cookies() map[string]string {
	out := map[string]string{}
	m.st.Cookies.Read(func(c map[string]string) {
		for k, v := range c {
			out[k] = v
		}
	})
	return out
}

// UpdateCookies sets (or clears, for "") cookies per platform key.
func (m *Manager) UpdateCookies(values map[string]string) {
	m.st.Cookies.Update(func(c *map[string]string) {
		for k, v := range values {
			if strings.TrimSpace(v) == "" {
				delete(*c, k)
			} else {
				(*c)[k] = strings.TrimSpace(v)
			}
		}
	})
}

// SaveRoot is the global recordings folder.
func (m *Manager) SaveRoot() string {
	if p := strings.TrimSpace(m.Settings().String("live_save_path")); p != "" {
		return p
	}
	return m.opts.DefaultSaveDir
}

func (m *Manager) roomRoot(r store.Room) string {
	if strings.TrimSpace(r.RecordDir) != "" {
		return r.RecordDir
	}
	return m.SaveRoot()
}

// Disk reports free/total bytes of the save root (cached for 20s).
func (m *Manager) Disk() (free, total uint64) {
	m.diskMu.Lock()
	defer m.diskMu.Unlock()
	if time.Since(m.diskAt) > 20*time.Second {
		if f, t, err := sysutil.DiskFree(m.SaveRoot()); err == nil {
			m.diskFree, m.diskTotal = f, t
		}
		m.diskAt = time.Now()
	}
	return m.diskFree, m.diskTotal
}

// RecordingEnabled reports whether the save root has more free space than the threshold.
func (m *Manager) RecordingEnabled() bool {
	free, total := m.Disk()
	return total == 0 || float64(free) >= m.thresholdBytes()
}

func (m *Manager) thresholdBytes() float64 {
	return m.Settings().Float("recording_space_threshold", 2) * (1 << 30)
}

// ThumbPath is where a room's cover image lives.
func (m *Manager) ThumbPath(id string) string { return filepath.Join(m.opts.ThumbDir, id+".jpg") }

// ActiveFiles lists the files currently being written.
func (m *Manager) ActiveFiles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, rt := range m.runtimes {
		if f := rt.snapshot().File; f != "" && rt.snapshot().Phase == PhaseRecording {
			out = append(out, f)
		}
	}
	return out
}

// ---- mutations -----------------------------------------------------------------------------

// RoomInput is the user-editable part of a room. Nil pointers mean "leave as is / use default".
type RoomInput struct {
	URL           string
	Name          *string
	Quality       *string
	Format        *string
	Segment       *bool
	SegmentSec    *int
	BitrateK      *int
	RecordDir     *string
	Monitor       *bool
	Scheduled     *bool
	ScheduleStart *string
	ScheduleHours *float64
	Push          *bool
	NotifyOnly    *bool
}

// Skip explains why an added URL was rejected.
type Skip struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// ErrNotFound is returned for unknown room ids.
var ErrNotFound = errors.New("直播间不存在")

// Add creates rooms; monitored ones start right away.
func (m *Manager) Add(inputs []RoomInput) ([]View, []Skip) {
	m.roomsMu.Lock()
	defer m.roomsMu.Unlock()
	s := m.Settings()
	seen := newRoomIndex(m.roomsSnapshot())
	var created []store.Room
	var skipped []Skip
	for _, in := range inputs {
		u := strings.TrimSpace(in.URL)
		res := platform.Find(u)
		if res == nil {
			skipped = append(skipped, Skip{u, "暂不支持该平台（目前支持抖音、B站）"})
			continue
		}
		c, err := m.canonical(res, u)
		if err != nil {
			skipped = append(skipped, Skip{u, err.Error()})
			continue
		}
		if dup := seen.find(res.Key(), c, ""); dup != nil {
			skipped = append(skipped, Skip{u, dupReason(dup)})
			continue
		}
		room := store.Room{
			ID: newID(), URL: c.URL, Platform: res.Key(), Ident: c.Ident, Monitor: true, CreatedAt: time.Now(),
			Quality:    platform.Quality(s.String("record_quality")),
			Format:     strings.ToUpper(s.String("video_format")),
			Segment:    s.Bool("segmented_recording_enabled"),
			SegmentSec: s.Int("video_segment_time", 1800),
			NotifyOnly: s.Bool("only_notify_no_record"),
		}
		applyInput(&room, in)
		seen.add(room)
		created = append(created, room)
	}
	if len(created) == 0 {
		return nil, skipped
	}
	m.st.Rooms.Update(func(rs *[]store.Room) { *rs = append(*rs, created...) })
	views := make([]View, 0, len(created))
	for _, r := range created {
		m.mu.Lock()
		m.runtimes[r.ID] = newRuntime(m, r.ID)
		m.mu.Unlock()
		if r.Monitor {
			m.startRuntime(r.ID)
		}
		views = append(views, View{Room: r, State: m.stateOf(r)})
		m.emit(Event{Type: EventRoom, ID: r.ID})
	}
	return views, skipped
}

// Edit updates a room's settings. Changes to quality/format/paths apply from the next recording part.
func (m *Manager) Edit(id string, in RoomInput) (View, error) {
	m.roomsMu.Lock()
	defer m.roomsMu.Unlock()
	var (
		newURL string
		c      platform.Canonical
		res    platform.Resolver
	)
	if u := strings.TrimSpace(in.URL); u != "" {
		if cur, ok := m.room(id); !ok || normalizeURL(cur.URL) != normalizeURL(u) {
			if res = platform.Find(u); res == nil {
				return View{}, errors.New("不支持的直播间链接")
			}
			var err error
			if c, err = m.canonical(res, u); err != nil {
				return View{}, err
			}
			if dup := newRoomIndex(m.roomsSnapshot()).find(res.Key(), c, id); dup != nil {
				return View{}, errors.New(dupReason(dup))
			}
			newURL = c.URL
		}
	}
	found := false
	m.st.Rooms.Update(func(rs *[]store.Room) {
		for i := range *rs {
			if (*rs)[i].ID != id {
				continue
			}
			found = true
			r := &(*rs)[i]
			if newURL != "" {
				r.URL, r.Platform, r.Ident = newURL, res.Key(), c.Ident
			}
			in.Monitor = nil // monitoring has its own endpoint
			applyInput(r, in)
			return
		}
	})
	if !found {
		return View{}, ErrNotFound
	}
	m.emit(Event{Type: EventRoom, ID: id})
	v, _ := m.View(id)
	return v, nil
}

// Reorder sets the room list's order, which the UI shows by default. ids is the new order; rooms it
// leaves out keep their relative order after the listed ones, and unknown ids are ignored.
func (m *Manager) Reorder(ids []string) {
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		if _, dup := pos[id]; !dup {
			pos[id] = i
		}
	}
	var order []string
	m.st.Rooms.Update(func(rs *[]store.Room) {
		sort.SliceStable(*rs, func(i, j int) bool {
			pi, iok := pos[(*rs)[i].ID]
			pj, jok := pos[(*rs)[j].ID]
			switch {
			case iok && jok:
				return pi < pj
			default:
				return iok && !jok
			}
		})
		for _, r := range *rs {
			order = append(order, r.ID)
		}
	})
	m.emit(Event{Type: EventOrder, IDs: order})
}

// Delete stops and removes rooms (recorded files are kept).
func (m *Manager) Delete(ids []string) {
	for _, id := range ids {
		m.mu.Lock()
		rt := m.runtimes[id]
		delete(m.runtimes, id)
		m.mu.Unlock()
		if rt != nil {
			rt.stop()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			rt.wait(ctx)
			cancel()
		}
		_ = os.Remove(m.ThumbPath(id))
	}
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	m.st.Rooms.Update(func(rs *[]store.Room) {
		kept := (*rs)[:0]
		for _, r := range *rs {
			if !gone[r.ID] {
				kept = append(kept, r)
			}
		}
		*rs = kept
	})
	m.emit(Event{Type: EventRemoved, IDs: ids})
}

// SetMonitor turns monitoring on or off.
func (m *Manager) SetMonitor(id string, on bool) (View, error) {
	if _, ok := m.room(id); !ok {
		return View{}, ErrNotFound
	}
	m.st.Rooms.Update(func(rs *[]store.Room) {
		for i := range *rs {
			if (*rs)[i].ID == id {
				(*rs)[i].Monitor = on
			}
		}
	})
	if on {
		m.startRuntime(id)
	} else if rt := m.runtimeOf(id); rt != nil {
		rt.stop()
	}
	m.emit(Event{Type: EventRoom, ID: id})
	v, _ := m.View(id)
	return v, nil
}

// SetRecording stops the current live's recording (monitoring continues; the next live records
// normally), or resumes it.
func (m *Manager) SetRecording(id string, on bool) (View, error) {
	r, ok := m.room(id)
	if !ok {
		return View{}, ErrNotFound
	}
	if on && !r.Monitor {
		if _, err := m.SetMonitor(id, true); err != nil {
			return View{}, err
		}
	}
	rt := m.runtimeOf(id)
	if on {
		if rt != nil {
			rt.resumeRecording() // for a notify-only room: this live only
		}
	} else if rt != nil {
		rt.pauseRecording()
	}
	v, _ := m.View(id)
	return v, nil
}

// CheckNow makes a monitored room re-check immediately.
func (m *Manager) CheckNow(id string) error {
	r, ok := m.room(id)
	if !ok {
		return ErrNotFound
	}
	if !r.Monitor {
		return errors.New("请先开启监控")
	}
	if rt := m.runtimeOf(id); rt != nil {
		rt.poke()
	}
	return nil
}

func (m *Manager) runtimeOf(id string) *runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimes[id]
}

func (m *Manager) startRuntime(id string) {
	m.mu.Lock()
	rt := m.runtimes[id]
	if rt == nil {
		rt = newRuntime(m, id)
		m.runtimes[id] = rt
	}
	m.mu.Unlock()
	if r, ok := m.room(id); ok && r.Monitor {
		rt.start()
	}
}

func (m *Manager) wakeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rt := range m.runtimes {
		rt.poke()
	}
}

// renameIfAuto stores the detected streamer name unless the user set one.
// learn records what a successful check revealed: the streamer's name (unless the user typed one)
// and their identity, for rooms added before identities were stored or while the site was down.
// Runs after every check, so it only writes when something actually changed.
func (m *Manager) learn(id, anchor, ident string) {
	stale := false
	m.st.Rooms.Read(func(rs []store.Room) {
		for _, r := range rs {
			if r.ID == id {
				stale = (anchor != "" && !r.CustomName && r.Name != anchor) || (ident != "" && r.Ident == "")
			}
		}
	})
	if !stale {
		return
	}
	m.st.Rooms.Update(func(rs *[]store.Room) {
		for i := range *rs {
			r := &(*rs)[i]
			if r.ID != id {
				continue
			}
			if anchor != "" && !r.CustomName {
				r.Name = anchor
			}
			if ident != "" && r.Ident == "" {
				r.Ident = ident
			}
		}
	})
	m.emit(Event{Type: EventRoom, ID: id})
}

// resolve calls the site with per-platform concurrency limits and a hard timeout.
func (m *Manager) resolve(ctx context.Context, room store.Room) (*platform.StreamInfo, error) {
	res := platform.ByKey(room.Platform)
	if res == nil {
		return nil, fmt.Errorf("未知平台 %q", room.Platform)
	}
	s := m.Settings()
	sem := m.sem(room.Platform, s.Int("platform_max_concurrent", 2))
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-sem }()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	opt := platform.Options{
		Quality:   room.Quality,
		Cookie:    m.Cookies()[room.Platform],
		PreferHLS: strings.EqualFold(s.String("default_live_source"), "HLS"),
		Proxy:     m.proxyFor(room.Platform, s),
	}
	return res.Resolve(ctx, room.URL, opt)
}

// canonical asks the site for the link's stored form and the streamer's identity. Only a definite
// "this link can't be a room" rejects the link; a network failure keeps it as typed (identity
// unknown until the first successful check fills it in).
func (m *Manager) canonical(res platform.Resolver, u string) (platform.Canonical, error) {
	cz, ok := res.(platform.Canonicalizer)
	if !ok {
		return platform.Canonical{URL: u}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s := m.Settings()
	c, err := cz.Canonical(ctx, u, platform.Options{Cookie: m.Cookies()[res.Key()], Proxy: m.proxyFor(res.Key(), s)})
	var ue *platform.UnsupportedError
	switch {
	case err == nil:
		return c, nil
	case errors.As(err, &ue):
		return platform.Canonical{}, ue
	default:
		slog.Warn("link not verified, keeping it as typed", "url", u, "err", err)
		return platform.Canonical{URL: u}, nil
	}
}

// roomIndex finds an existing room by link or by streamer.
type roomIndex struct {
	byURL   map[string]store.Room
	byIdent map[string]store.Room
}

func newRoomIndex(rooms []store.Room) *roomIndex {
	ix := &roomIndex{byURL: map[string]store.Room{}, byIdent: map[string]store.Room{}}
	for _, r := range rooms {
		ix.add(r)
	}
	return ix
}

func (ix *roomIndex) add(r store.Room) {
	ix.byURL[normalizeURL(r.URL)] = r
	if r.Ident != "" {
		ix.byIdent[r.Platform+":"+r.Ident] = r
	}
}

// find returns another room (not self) with the same link or the same streamer.
func (ix *roomIndex) find(platformKey string, c platform.Canonical, self string) *store.Room {
	if r, ok := ix.byURL[normalizeURL(c.URL)]; ok && r.ID != self {
		return &r
	}
	if c.Ident != "" {
		if r, ok := ix.byIdent[platformKey+":"+c.Ident]; ok && r.ID != self {
			return &r
		}
	}
	return nil
}

func dupReason(r *store.Room) string {
	if r.Name != "" {
		return fmt.Sprintf("已在列表中（「%s」）", r.Name)
	}
	return "已在列表中"
}

func (m *Manager) proxyFor(platformKey string, s store.Settings) string {
	if !s.Bool("enable_proxy") {
		return ""
	}
	list := strings.TrimSpace(s.String("default_platform_with_proxy"))
	if list != "" {
		hit := false
		for _, p := range strings.Split(strings.ReplaceAll(list, "，", ","), ",") {
			if strings.EqualFold(strings.TrimSpace(p), platformKey) {
				hit = true
			}
		}
		if !hit {
			return ""
		}
	}
	return platform.ProxyURL(s.String("proxy_address"))
}

func (m *Manager) sem(key string, size int) chan struct{} {
	if size < 1 {
		size = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sems[key]
	if !ok || cap(s) != size {
		s = make(chan struct{}, size)
		m.sems[key] = s
	}
	return s
}

func applyInput(r *store.Room, in RoomInput) {
	if in.Name != nil {
		r.Name = strings.TrimSpace(*in.Name)
		r.CustomName = r.Name != ""
	}
	if in.Quality != nil && *in.Quality != "" {
		r.Quality = platform.Quality(strings.ToUpper(*in.Quality))
	}
	if in.Format != nil && *in.Format != "" {
		r.Format = strings.ToUpper(*in.Format)
	}
	if in.Segment != nil {
		r.Segment = *in.Segment
	}
	if in.SegmentSec != nil && *in.SegmentSec > 0 {
		r.SegmentSec = *in.SegmentSec
	}
	if in.BitrateK != nil {
		r.BitrateK = *in.BitrateK
	}
	if in.RecordDir != nil {
		r.RecordDir = strings.TrimSpace(*in.RecordDir)
	}
	if in.Monitor != nil {
		r.Monitor = *in.Monitor
	}
	if in.Scheduled != nil {
		r.Scheduled = *in.Scheduled
	}
	if in.ScheduleStart != nil {
		r.ScheduleStart = *in.ScheduleStart
	}
	if in.ScheduleHours != nil {
		r.ScheduleHours = *in.ScheduleHours
	}
	if in.Push != nil {
		r.Push = *in.Push
	}
	if in.NotifyOnly != nil {
		r.NotifyOnly = *in.NotifyOnly
	}
	if r.Quality == "" {
		r.Quality = platform.QualityOD
	}
	if r.Format == "" {
		r.Format = "TS"
	}
	if r.SegmentSec <= 0 {
		r.SegmentSec = 1800
	}
}

// normalizeURL is the duplicate-check form of a link. Only scheme and host are case-insensitive:
// short-link codes (v.douyin.com/qwpVijrUc3E, b23.tv/5piv15g) are not.
func normalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimRight(u, "/")
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		scheme, rest = "https", u
	}
	host, path, _ := strings.Cut(rest, "/")
	return strings.ToLower(scheme) + "://" + strings.ToLower(host) + "/" + path
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
