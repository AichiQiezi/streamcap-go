// Package store persists rooms, settings and cookies as JSON files in the data directory.
//
// Writes are atomic (temp file + rename) and coalesced: callers mark data dirty and a background
// goroutine flushes at most every flushGap, so no caller ever sleeps or does I/O while holding a lock.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const flushGap = 500 * time.Millisecond

// File is one JSON document on disk guarded by a mutex.
type File[T any] struct {
	path  string
	mu    sync.RWMutex
	data  T
	dirty chan struct{}
}

func openFile[T any](path string, def T) (*File[T], error) {
	f := &File[T]{path: path, data: def, dirty: make(chan struct{}, 1)}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// first run: keep defaults, write them out
		f.markDirty()
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &f.data); err != nil {
			// Keep the broken file for the user instead of silently overwriting it.
			bak := path + ".broken-" + time.Now().Format("20060102-150405")
			_ = os.Rename(path, bak)
			slog.Error("config file unreadable, moved aside", "file", path, "backup", bak, "err", err)
			f.data = def
			f.markDirty()
		}
	}
	go f.flusher()
	return f, nil
}

// Read runs fn with a read lock.
func (f *File[T]) Read(fn func(T)) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	fn(f.data)
}

// Update runs fn with the write lock and schedules a flush.
func (f *File[T]) Update(fn func(*T)) {
	f.mu.Lock()
	fn(&f.data)
	f.mu.Unlock()
	f.markDirty()
}

func (f *File[T]) markDirty() {
	select {
	case f.dirty <- struct{}{}:
	default:
	}
}

func (f *File[T]) flusher() {
	for range f.dirty {
		if err := f.Flush(); err != nil {
			slog.Error("save failed", "file", f.path, "err", err)
		}
		time.Sleep(flushGap)
	}
}

// Flush writes the current data to disk now.
func (f *File[T]) Flush() error {
	f.mu.RLock()
	raw, err := json.MarshalIndent(f.data, "", "  ")
	f.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// Store is the app's persistent state.
type Store struct {
	Dir      string
	Rooms    *File[[]Room]
	Settings *File[Settings]
	Cookies  *File[map[string]string]
	// Sessions journals live sessions whose files are not finalised yet (crash recovery).
	Sessions *File[map[string]SessionJournal]
}

// Open loads (or initialises) everything under dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	rooms, err := openFile(filepath.Join(dir, "rooms.json"), []Room{})
	if err != nil {
		return nil, fmt.Errorf("rooms: %w", err)
	}
	settings, err := openFile(filepath.Join(dir, "settings.json"), DefaultSettings())
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	// Fill keys added in newer versions without touching the user's values.
	settings.Update(func(s *Settings) {
		for k, v := range DefaultSettings() {
			if _, ok := (*s)[k]; !ok {
				(*s)[k] = v
			}
		}
	})
	cookies, err := openFile(filepath.Join(dir, "cookies.json"), map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("cookies: %w", err)
	}
	sessions, err := openFile(filepath.Join(dir, "sessions.json"), map[string]SessionJournal{})
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	return &Store{Dir: dir, Rooms: rooms, Settings: settings, Cookies: cookies, Sessions: sessions}, nil
}

// FlushAll writes every file synchronously (used on shutdown).
func (s *Store) FlushAll() {
	for _, fl := range []interface{ Flush() error }{s.Rooms, s.Settings, s.Cookies, s.Sessions} {
		if err := fl.Flush(); err != nil {
			slog.Error("flush on shutdown failed", "err", err)
		}
	}
}
