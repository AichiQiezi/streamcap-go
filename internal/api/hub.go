package api

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/AichiQiezi/streamcap-go/internal/monitor"
)

// hub fans manager events out to WebSocket clients. Room updates are coalesced per room and
// flushed every flushEvery, so a burst of progress ticks becomes one message per room.
type hub struct {
	m *monitor.Manager

	mu      sync.Mutex
	clients map[*client]struct{}
	pending map[string]struct{}
}

type client struct {
	send chan []byte
}

const flushEvery = 300 * time.Millisecond

func newHub(m *monitor.Manager) *hub {
	h := &hub{m: m, clients: map[*client]struct{}{}, pending: map[string]struct{}{}}
	m.Subscribe(h.onEvent)
	go h.flusher()
	return h
}

func (h *hub) onEvent(ev monitor.Event) {
	switch ev.Type {
	case monitor.EventRoom:
		h.mu.Lock()
		h.pending[ev.ID] = struct{}{}
		h.mu.Unlock()
	case monitor.EventRemoved:
		h.mu.Lock()
		for _, id := range ev.IDs {
			delete(h.pending, id)
		}
		h.mu.Unlock()
		h.broadcast("removed", ev.IDs)
	case monitor.EventToast:
		h.broadcast("toast", map[string]string{"text": ev.Text})
	case monitor.EventQuitting:
		h.broadcast("quitting", map[string]any{})
	case monitor.EventOrder:
		h.broadcast("order", ev.IDs)
	}
}

func (h *hub) flusher() {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for range t.C {
		h.mu.Lock()
		ids := make([]string, 0, len(h.pending))
		for id := range h.pending {
			ids = append(ids, id)
		}
		h.pending = map[string]struct{}{}
		h.mu.Unlock()
		for _, id := range ids {
			if v, ok := h.m.View(id); ok {
				h.broadcast("recording", toJSON(v))
			}
		}
	}
}

func (h *hub) broadcast(event string, data any) {
	msg, err := json.Marshal(map[string]any{"event": event, "data": data})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default: // slow client: drop; it resyncs via /api/state on reconnect
		}
	}
}

func (h *hub) serve(ctx context.Context, conn *websocket.Conn) {
	c := &client{send: make(chan []byte, 256)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // reader: the UI sends pings; any read error ends the connection
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case msg := <-c.send:
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}
