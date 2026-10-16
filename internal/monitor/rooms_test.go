package monitor

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/notify"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

// fakeSite maps every link on fake.test to a streamer, after a delay standing in for the network
// lookup that sits between the duplicate check and the write.
type fakeSite struct{}

var fakeIdents = map[string]string{
	"/share/aaa": "alice", "/share/bbb": "alice", "/u/alice": "alice", // three links, one streamer
	"/share/AbC": "carol", "/share/abc": "dave", // short codes differing only in case
	"/u/bob": "bob",
}

func (fakeSite) Key() string           { return "fake" }
func (fakeSite) Name() string          { return "Fake" }
func (fakeSite) Match(raw string) bool { return strings.Contains(raw, "://fake.test/") }

// fakeLive scripts whether a fake.test link is live (path -> *atomic.Bool); absent means offline.
var fakeLive sync.Map

func (fakeSite) Resolve(_ context.Context, raw string, _ platform.Options) (*platform.StreamInfo, error) {
	u, _ := url.Parse(raw)
	live := false
	if v, ok := fakeLive.Load(u.Path); ok {
		live = v.(*atomic.Bool).Load()
	}
	return &platform.StreamInfo{Platform: "fake", Anchor: "Bob", Ident: fakeIdents[u.Path], IsLive: live}, nil
}
func (fakeSite) Canonical(_ context.Context, raw string, _ platform.Options) (platform.Canonical, error) {
	time.Sleep(30 * time.Millisecond)
	u, _ := url.Parse(raw)
	if id, ok := fakeIdents[u.Path]; ok {
		return platform.Canonical{URL: raw, Ident: id}, nil
	}
	return platform.Canonical{}, &platform.UnsupportedError{Reason: "不是直播间"}
}

func init() { platform.Register(fakeSite{}) }

func newTestManager(t *testing.T) *Manager {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, Options{DefaultSaveDir: t.TempDir(), ThumbDir: t.TempDir()})
}

var off = func() *bool { b := false; return &b }()

func add(m *Manager, links ...string) ([]View, []Skip) {
	var in []RoomInput
	for _, l := range links {
		in = append(in, RoomInput{URL: l, Monitor: off})
	}
	return m.Add(in)
}

func TestConcurrentAddsOfOneStreamerKeepOne(t *testing.T) {
	m := newTestManager(t)
	var wg sync.WaitGroup
	for _, l := range []string{"https://fake.test/share/aaa", "https://fake.test/share/aaa", "https://fake.test/share/bbb", "https://fake.test/u/alice"} {
		wg.Add(1)
		go func(l string) { defer wg.Done(); add(m, l) }(l)
	}
	wg.Wait()
	if n := len(m.roomsSnapshot()); n != 1 {
		t.Fatalf("rooms = %d after 4 concurrent adds of the same streamer, want 1", n)
	}
}

func TestAddSkipsSameStreamerThroughAnotherLink(t *testing.T) {
	m := newTestManager(t)
	created, skipped := add(m, "https://fake.test/share/aaa", "https://fake.test/share/bbb", "https://fake.test/u/bob")
	if len(created) != 2 || len(skipped) != 1 || !strings.HasPrefix(skipped[0].Reason, "已在列表中") {
		t.Fatalf("created %d, skipped %+v; want alice and bob, the second alice link skipped", len(created), skipped)
	}
	if _, skipped = add(m, "https://fake.test/u/alice"); len(skipped) != 1 {
		t.Fatalf("a later add of alice was not skipped: %+v", skipped)
	}
	if _, skipped = add(m, "https://fake.test/nope"); len(skipped) != 1 || skipped[0].Reason != "不是直播间" {
		t.Fatalf("unsupported link: %+v", skipped)
	}
}

func TestShortCodesAreCaseSensitive(t *testing.T) {
	m := newTestManager(t)
	if created, skipped := add(m, "https://fake.test/share/AbC", "https://fake.test/share/abc"); len(created) != 2 {
		t.Fatalf("created %d, skipped %+v; codes differing in case are different streamers", len(created), skipped)
	}
	if normalizeURL("HTTPS://Live.Douyin.com/Ab/?x=1") != "https://live.douyin.com/Ab" {
		t.Error(normalizeURL("HTTPS://Live.Douyin.com/Ab/?x=1"))
	}
}

func TestEditCannotDuplicateAnotherRoom(t *testing.T) {
	m := newTestManager(t)
	created, _ := add(m, "https://fake.test/share/aaa", "https://fake.test/u/bob")
	bob := created[1].Room.ID
	if _, err := m.Edit(bob, RoomInput{URL: "https://fake.test/share/bbb"}); err == nil || !strings.Contains(err.Error(), "已在列表中") {
		t.Fatalf("edit to another alice link: err = %v", err)
	}
	// Re-saving a room with its own link is not a duplicate of itself.
	if _, err := m.Edit(bob, RoomInput{URL: "https://fake.test/u/bob"}); err != nil {
		t.Fatalf("edit with unchanged link: %v", err)
	}
}

func TestLearnWritesOnlyOnChange(t *testing.T) {
	m := newTestManager(t)
	created, _ := add(m, "https://fake.test/u/bob")
	id := created[0].Room.ID
	m.st.Rooms.Update(func(rs *[]store.Room) { (*rs)[0].Ident = "" }) // a room from before idents
	var writes int
	m.Subscribe(func(ev Event) {
		if ev.Type == EventRoom && ev.ID == id {
			writes++ // learn emits exactly when it updates the room
		}
	})
	m.learn(id, "Bob", "bob")
	m.learn(id, "Bob", "bob") // every later check reports the same things
	m.learn(id, "", "")
	r, _ := m.room(id)
	if r.Name != "Bob" || r.Ident != "bob" {
		t.Fatalf("after learn: %+v", r)
	}
	if writes != 1 {
		t.Fatalf("writes = %d, want 1 (only the first learn changed anything)", writes)
	}
}

func TestRecordThisLiveKeepsNotifyOnly(t *testing.T) {
	m := newTestManager(t)
	created, _ := m.Add([]RoomInput{{URL: "https://fake.test/u/bob", Monitor: off, NotifyOnly: func() *bool { b := true; return &b }()}})
	room := created[0].Room
	rt := newRuntime(m, room.ID)
	if got := rt.skipReason(room, nil); got != DetailNotifyOnly {
		t.Fatalf("notify-only room: skip = %q, want %q", got, DetailNotifyOnly)
	}
	rt.resumeRecording() // 录这一场
	if got := rt.skipReason(room, nil); got != "" {
		t.Fatalf("after 录这一场: skip = %q, want recording", got)
	}
	rt.pauseRecording() // 停止本次录制
	if got := rt.skipReason(room, nil); got != DetailNotifyOnly {
		t.Fatalf("after 停止本次录制: skip = %q, want %q", got, DetailNotifyOnly)
	}
	if r, _ := m.room(room.ID); !r.NotifyOnly {
		t.Fatal("the room's mode changed; 录这一场 must only affect this live")
	}
}

// alertLog records what the loop would have pushed.
type alertLog struct {
	mu     sync.Mutex
	events []notify.Event
}

func (a *alertLog) send(_ context.Context, _ store.Settings, ev notify.Event) {
	a.mu.Lock()
	a.events = append(a.events, ev)
	a.mu.Unlock()
}

func (a *alertLog) count() (starts, ends int, last notify.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ev := range a.events {
		if ev.Start {
			starts++
		} else {
			ends++
		}
		last = ev
	}
	return
}

// fastLoop shrinks the supervisor's timings: checks every 5ms, confirmations 1ms apart, and a
// resume wait of resume_wait_minutes * 60ms.
func fastLoop(t *testing.T) *alertLog {
	a := &alertLog{}
	oldFloor, oldUnit, oldStep, oldResume, oldSend := intervalFloor, intervalUnit, confirmStep, resumeUnit, sendAlert
	intervalFloor, intervalUnit, confirmStep, resumeUnit, sendAlert = time.Millisecond, 5*time.Millisecond, time.Millisecond, 60*time.Millisecond, a.send
	t.Cleanup(func() {
		intervalFloor, intervalUnit, confirmStep, resumeUnit, sendAlert = oldFloor, oldUnit, oldStep, oldResume, oldSend
	})
	return a
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func watchNotifyOnly(t *testing.T, m *Manager, live *atomic.Bool) string {
	t.Helper()
	fakeLive.Store("/u/bob", live)
	t.Cleanup(func() { fakeLive.Delete("/u/bob") })
	m.UpdateSettings(map[string]any{"loop_time_seconds": "1", "resume_wait_minutes": "5"}) // wait = 300ms
	on := true
	created, skipped := m.Add([]RoomInput{{URL: "https://fake.test/u/bob", Monitor: &on, NotifyOnly: &on}})
	if len(created) != 1 {
		t.Fatalf("add: %+v", skipped)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})
	return created[0].Room.ID
}

func TestBackOnAirWithinWaitIsTheSameLive(t *testing.T) {
	alerts := fastLoop(t)
	m := newTestManager(t)
	live := &atomic.Bool{}
	live.Store(true)
	id := watchNotifyOnly(t, m, live)
	phase := func() (Phase, string) { st := m.runtimeOf(id).snapshot(); return st.Phase, st.Detail }

	eventually(t, "开播 alert", func() bool { s, _, _ := alerts.count(); return s == 1 })
	live.Store(false) // the stream drops…
	eventually(t, "waiting for a resume", func() bool { _, d := phase(); return d == DetailResumeWait })
	live.Store(true) // …and is back well within the 300ms wait
	eventually(t, "live again", func() bool { p, _ := phase(); return p == PhaseLive })
	time.Sleep(50 * time.Millisecond)
	if s, e, _ := alerts.count(); s != 1 || e != 0 {
		t.Fatalf("after a resume within the wait: %d 开播 / %d 下播 alerts, want 1 / 0", s, e)
	}

	live.Store(false) // now it really ends
	eventually(t, "下播 alert", func() bool { _, e, _ := alerts.count(); return e == 1 })
	if s, _, last := alerts.count(); s != 1 || last.Live <= 0 {
		t.Fatalf("starts = %d, end event %+v; want one 开播 and a live duration", s, last)
	}
	if p, d := phase(); p != PhaseOffline || d != "" {
		t.Fatalf("after the wait: phase %v / %q, want offline", p, d)
	}
}

func TestBackOnAirAfterWaitIsANewLive(t *testing.T) {
	alerts := fastLoop(t)
	m := newTestManager(t)
	live := &atomic.Bool{}
	live.Store(true)
	watchNotifyOnly(t, m, live)

	eventually(t, "开播 alert", func() bool { s, _, _ := alerts.count(); return s == 1 })
	live.Store(false)
	eventually(t, "下播 alert after the wait", func() bool { _, e, _ := alerts.count(); return e == 1 })
	live.Store(true)
	eventually(t, "a new 开播 alert", func() bool { s, _, _ := alerts.count(); return s == 2 })
}

func TestRecordThisLiveSurvivesAResume(t *testing.T) {
	m := newTestManager(t)
	created, _ := m.Add([]RoomInput{{URL: "https://fake.test/u/bob", Monitor: off, NotifyOnly: func() *bool { b := true; return &b }()}})
	room := created[0].Room
	rt := newRuntime(m, room.ID)
	rt.watch = &liveWatch{since: time.Now()}
	rt.resumeRecording()                            // 录这一场
	rt.endingAt = time.Now().Add(time.Minute)       // off air, within the wait
	if got := rt.skipReason(room, nil); got != "" { // back on air: still recording this 场
		t.Fatalf("录这一场 lost during the wait: skip = %q", got)
	}
	rt.finishLive(room, m.Settings()) // the wait ran out
	if got := rt.skipReason(room, nil); got != DetailNotifyOnly {
		t.Fatalf("录这一场 outlived its 场: skip = %q", got)
	}
}

func TestReorder(t *testing.T) {
	m := newTestManager(t)
	created, _ := add(m, "https://fake.test/u/alice", "https://fake.test/u/bob", "https://fake.test/share/AbC")
	a, b, c := created[0].Room.ID, created[1].Room.ID, created[2].Room.ID
	var broadcast []string
	m.Subscribe(func(ev Event) {
		if ev.Type == EventOrder {
			broadcast = ev.IDs
		}
	})
	// c first, then a; b isn't listed so it goes after them; duplicates and unknown ids are ignored.
	m.Reorder([]string{c, "nope", a, c})
	var got []string
	for _, r := range m.roomsSnapshot() {
		got = append(got, r.ID)
	}
	if want := []string{c, a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if strings.Join(broadcast, ",") != strings.Join(got, ",") {
		t.Fatalf("broadcast %v, want the new order %v", broadcast, got)
	}
}
