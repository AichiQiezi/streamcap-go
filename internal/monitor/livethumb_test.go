package monitor

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// fakeGrab stands in for the stream pull: it reports what it was asked for, then waits for done
// (or its context) before writing the cover.
type fakeGrab struct {
	asked chan *platform.StreamInfo
	ended chan error
	done  chan struct{}
}

func newFakeGrab(t *testing.T) *fakeGrab {
	g := &fakeGrab{asked: make(chan *platform.StreamInfo, 8), ended: make(chan error, 8), done: make(chan struct{})}
	old := grabStreamThumb
	grabStreamThumb = func(ctx context.Context, info *platform.StreamInfo, _, dest string) error {
		g.asked <- info
		select {
		case <-g.done:
		case <-ctx.Done():
			g.ended <- ctx.Err()
			return ctx.Err()
		}
		g.ended <- nil
		return os.WriteFile(dest, []byte("jpg"), 0o644)
	}
	t.Cleanup(func() { grabStreamThumb = old })
	return g
}

func TestNotifyOnlyLiveGetsAFreshCover(t *testing.T) {
	fastLoop(t)
	g := newFakeGrab(t)
	m := newTestManager(t)
	live := &atomic.Bool{}
	id := watchNotifyOnly(t, m, live)
	rt := m.runtimeOf(id)
	eventually(t, "offline", func() bool { return rt.snapshot().Phase == PhaseOffline })
	rt.update(func(st *State) { st.ThumbVersion = 123 }) // a cover left by an earlier recording

	live.Store(true)
	info := <-g.asked
	if info.Quality != platform.QualityLD {
		t.Fatalf("cover pulled at quality %q, want the lowest (%q)", info.Quality, platform.QualityLD)
	}
	if st := rt.snapshot(); st.Phase != PhaseLive || st.ThumbVersion != 0 {
		t.Fatalf("live, cover pending: phase %v, thumb version %d; want live and the old cover hidden", st.Phase, st.ThumbVersion)
	}
	close(g.done)
	eventually(t, "the fresh cover", func() bool { return rt.snapshot().ThumbVersion > 123 })
	if _, err := os.Stat(m.ThumbPath(id)); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingCancelsTheCoverPull(t *testing.T) {
	g := newFakeGrab(t)
	m := newTestManager(t)
	created, _ := add(m, "https://fake.test/u/bob")
	room := created[0].Room
	live := &atomic.Bool{}
	live.Store(true)
	fakeLive.Store("/u/bob", live)
	t.Cleanup(func() { fakeLive.Delete("/u/bob") })
	rt := newRuntime(m, room.ID)

	rt.liveThumb(context.Background(), room, m.Settings())
	<-g.asked
	rt.liveThumb(context.Background(), room, m.Settings())                   // one pull at a time
	rt.beginSession(room, &platform.StreamInfo{Anchor: "Bob"}, m.Settings()) // 录这一场
	defer rt.endSession("test")
	if err := <-g.ended; err == nil {
		t.Fatal("the cover pull went on after the recording started")
	}
	select {
	case <-g.asked:
		t.Fatal("a second pull started while one was running")
	default:
	}
}
