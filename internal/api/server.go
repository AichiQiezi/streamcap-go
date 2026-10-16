// Package api serves the web UI and its HTTP/WebSocket API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/AichiQiezi/streamcap-go/internal/monitor"
	"github.com/AichiQiezi/streamcap-go/internal/notify"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
	"github.com/AichiQiezi/streamcap-go/internal/sysutil"
)

// Hooks let the app layer react to UI requests that aren't about rooms.
type Hooks struct {
	Show func() // bring the window to the front (another launch of the app asks for this)
	Quit func() // graceful shutdown: stop recordings, save files, exit
	// OpenNotificationSettings opens the OS page where StreamCap's notifications are allowed.
	OpenNotificationSettings func() error
}

// Server bundles the handlers.
type Server struct {
	m        *monitor.Manager
	hub      *hub
	static   fs.FS // built web UI (may be nil)
	loopback bool  // bound to 127.0.0.1: reject foreign Host headers (DNS rebinding)
	hooks    Hooks
}

// New builds the HTTP handler. static is the embedded web/dist (nil = API only).
func New(m *monitor.Manager, static fs.FS, loopback bool, hooks Hooks) http.Handler {
	s := &Server{m: m, hub: newHub(m), static: static, loopback: loopback, hooks: hooks}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/recordings", s.add)
	mux.HandleFunc("POST /api/recordings/batch", s.batch)
	mux.HandleFunc("PUT /api/recordings/order", s.reorder)
	mux.HandleFunc("PATCH /api/recordings/{id}", s.edit)
	mux.HandleFunc("DELETE /api/recordings/{id}", s.remove)
	mux.HandleFunc("POST /api/recordings/{id}/monitor", s.monitor)
	mux.HandleFunc("POST /api/recordings/{id}/record", s.record)
	mux.HandleFunc("POST /api/recordings/{id}/check", s.check)
	mux.HandleFunc("POST /api/recordings/{id}/open-folder", s.openFolder)
	mux.HandleFunc("GET /api/recordings/{id}/thumbnail", s.thumbnail)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("PUT /api/cookies", s.putCookies)
	mux.HandleFunc("POST /api/notify/test", s.testNotify)
	mux.HandleFunc("POST /api/notify/system-settings", s.openNotificationSettings)
	mux.HandleFunc("GET /api/files", s.listFiles)
	mux.HandleFunc("GET /api/files/raw", s.rawFile)
	mux.HandleFunc("DELETE /api/files", s.deleteFile)
	mux.HandleFunc("POST /api/files/reveal", s.revealFile)
	mux.HandleFunc("GET /api/ws", s.ws)
	mux.HandleFunc("POST /api/app/show", s.appShow)
	mux.HandleFunc("POST /api/app/quit", s.appQuit)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { fail(w, http.StatusNotFound, "Not Found") })
	mux.HandleFunc("/", s.spa)
	return s.guard(mux)
}

// guard enforces the loopback Host check: the API has no auth and can set the folders we write to.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.loopback {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			host = strings.Trim(host, "[]")
			if host != "127.0.0.1" && host != "localhost" && host != "::1" {
				http.Error(w, "Invalid host header", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ---------------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"detail": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		// Requiring JSON keeps cross-site "simple" form posts from reaching the API.
		fail(w, http.StatusUnsupportedMediaType, "需要 JSON 请求体")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误："+err.Error())
		return false
	}
	return true
}

func (s *Server) viewOr404(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, monitor.ErrNotFound) {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	v, ok := s.m.View(id)
	if !ok {
		fail(w, http.StatusNotFound, monitor.ErrNotFound.Error())
		return
	}
	writeJSON(w, toJSON(v))
}

// ---- state & rooms -----------------------------------------------------------------------------

func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	views := s.m.Views()
	recs := make([]recJSON, 0, len(views))
	for _, v := range views {
		recs = append(recs, toJSON(v))
	}
	free, total := s.m.Disk()
	qualities := make([]map[string]string, 0, len(platform.Qualities))
	for _, q := range platform.Qualities {
		qualities = append(qualities, map[string]string{"value": string(q), "label": q.Label()})
	}
	platforms := make([]map[string]string, 0)
	for _, p := range platform.All() {
		platforms = append(platforms, map[string]string{"key": p.Key(), "name": p.Name()})
	}
	writeJSON(w, map[string]any{
		"recordings":        recs,
		"recording_enabled": s.m.RecordingEnabled(),
		"disk":              map[string]any{"path": s.m.SaveRoot(), "free": free, "total": total},
		"options": map[string]any{
			"qualities": qualities, "video_formats": store.VideoFormats, "audio_formats": store.AudioFormatList,
			"platforms": platforms,
		},
	})
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var items []recInput
	if !decode(w, r, &items) {
		return
	}
	inputs := make([]monitor.RoomInput, 0, len(items))
	for _, it := range items {
		inputs = append(inputs, it.toRoomInput())
	}
	views, skipped := s.m.Add(inputs)
	created := make([]recJSON, 0, len(views))
	for _, v := range views {
		created = append(created, toJSON(v))
	}
	if skipped == nil {
		skipped = []monitor.Skip{}
	}
	writeJSON(w, map[string]any{"created": created, "skipped": skipped})
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request) {
	var in recInput
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	_, err := s.m.Edit(id, in.toRoomInput())
	s.viewOr404(w, id, err)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.m.View(id); !ok {
		fail(w, http.StatusNotFound, monitor.ErrNotFound.Error())
		return
	}
	s.m.Delete([]string{id})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) monitor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	_, err := s.m.SetMonitor(id, body.On)
	s.viewOr404(w, id, err)
}

func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	_, err := s.m.SetRecording(id, body.On)
	s.viewOr404(w, id, err)
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	if err := s.m.CheckNow(r.PathValue("id")); err != nil {
		code := http.StatusConflict
		if errors.Is(err, monitor.ErrNotFound) {
			code = http.StatusNotFound
		}
		fail(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) batch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch body.Action {
	case "start", "stop":
		for _, id := range body.IDs {
			_, _ = s.m.SetMonitor(id, body.Action == "start")
		}
	case "check":
		for _, id := range body.IDs {
			_ = s.m.CheckNow(id)
		}
	case "delete":
		s.m.Delete(body.IDs)
	default:
		fail(w, http.StatusBadRequest, "未知操作")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(body.IDs)})
}

func (s *Server) openFolder(w http.ResponseWriter, r *http.Request) {
	v, ok := s.m.View(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, monitor.ErrNotFound.Error())
		return
	}
	dir := v.State.Dir
	if dir == "" {
		dir = firstNonEmpty(v.Room.RecordDir, s.m.SaveRoot())
	}
	if err := sysutil.OpenFolder(dir); err != nil {
		fail(w, http.StatusInternalServerError, "无法打开文件夹："+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": dir})
}

func (s *Server) thumbnail(w http.ResponseWriter, r *http.Request) {
	p := s.m.ThumbPath(r.PathValue("id"))
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "max-age=31536000, immutable") // URLs carry ?v=<version>
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, p)
}

func (s *Server) reorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.m.Reorder(body.IDs)
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- settings ----------------------------------------------------------------------------------

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"values":            s.m.Settings(),
		"default_save_path": s.m.SaveRoot(),
		"cookies":           s.m.Cookies(),
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var values map[string]any
	if !decode(w, r, &values) {
		return
	}
	known := store.DefaultSettings()
	var unknown []string
	for k := range values {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		fail(w, http.StatusBadRequest, "不可修改的配置项: "+strings.Join(unknown, ", "))
		return
	}
	s.m.UpdateSettings(values)
	writeJSON(w, map[string]bool{"ok": true})
}

// testNotify sends a sample message through one channel. `values` are the settings page's unsaved
// edits, so a channel can be tried before saving.
func (s *Server) testNotify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channel string         `json:"channel"`
		Values  map[string]any `json:"values"`
	}
	if !decode(w, r, &in) {
		return
	}
	settings := s.m.Settings().Clone()
	known := store.DefaultSettings()
	for k, v := range in.Values {
		if _, ok := known[k]; ok {
			settings[k] = v
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second) // macOS may wait on the permission prompt
	defer cancel()
	if err := notify.Test(ctx, settings, in.Channel); err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) openNotificationSettings(w http.ResponseWriter, _ *http.Request) {
	if s.hooks.OpenNotificationSettings == nil {
		fail(w, http.StatusNotImplemented, "当前运行方式没有桌面")
		return
	}
	if err := s.hooks.OpenNotificationSettings(); err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) putCookies(w http.ResponseWriter, r *http.Request) {
	var values map[string]string
	if !decode(w, r, &values) {
		return
	}
	s.m.UpdateCookies(values)
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- files ---------------------------------------------------------------------------------------

var mediaExts = map[string]string{
	".ts": "video", ".flv": "video", ".mp4": "video", ".mkv": "video", ".mov": "video",
	".mp3": "audio", ".m4a": "audio", ".aac": "audio", ".wav": "audio",
}

type fileJSON struct {
	Path   string  `json:"path"`
	Name   string  `json:"name"`
	Folder string  `json:"folder"`
	Size   int64   `json:"size"`
	Mtime  float64 `json:"mtime"`
	Kind   string  `json:"kind"`
	Ext    string  `json:"ext"`
}

func (s *Server) listFiles(w http.ResponseWriter, _ *http.Request) {
	root := s.m.SaveRoot()
	files := []fileJSON{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || strings.Contains(name, ".merging.") || strings.Contains(name, ".tmp.") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		kind, ok := mediaExts[ext]
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		folder := path.Dir(rel)
		if folder == "." {
			folder = ""
		}
		files = append(files, fileJSON{
			Path: rel, Name: name, Folder: folder, Size: info.Size(),
			Mtime: float64(info.ModTime().UnixNano()) / 1e9, Kind: kind, Ext: ext[1:],
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Mtime > files[j].Mtime })
	writeJSON(w, map[string]any{"root": root, "files": files})
}

// resolveFile maps a UI-relative path to a file under the save root, refusing anything outside it.
func (s *Server) resolveFile(w http.ResponseWriter, rel string) (string, bool) {
	root, err := filepath.Abs(s.m.SaveRoot())
	if err != nil || rel == "" {
		fail(w, http.StatusBadRequest, "非法路径")
		return "", false
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if r, err := filepath.Rel(root, full); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		fail(w, http.StatusBadRequest, "非法路径")
		return "", false
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		fail(w, http.StatusNotFound, "文件不存在")
		return "", false
	}
	return full, true
}

func (s *Server) rawFile(w http.ResponseWriter, r *http.Request) {
	full, ok := s.resolveFile(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	f, err := os.Open(full)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	types := map[string]string{".mp4": "video/mp4", ".ts": "video/mp2t", ".flv": "video/x-flv", ".mkv": "video/x-matroska",
		".mov": "video/quicktime", ".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".wav": "audio/wav"}
	if ct, ok := types[strings.ToLower(filepath.Ext(full))]; ok {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, st.Name(), st.ModTime(), f) // handles Range for seeking
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	full, ok := s.resolveFile(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	for _, active := range s.m.ActiveFiles() {
		if a, _ := filepath.Abs(active); a == full {
			fail(w, http.StatusConflict, "文件正在录制中，不能删除")
			return
		}
	}
	if err := os.Remove(full); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) revealFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	var err error
	if rel == "" {
		err = sysutil.OpenFolder(s.m.SaveRoot())
	} else {
		full, ok := s.resolveFile(w, rel)
		if !ok {
			return
		}
		err = sysutil.Reveal(full)
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法打开文件夹："+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- app lifecycle -------------------------------------------------------------------------------------

func (s *Server) appShow(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if s.hooks.Show != nil {
		go s.hooks.Show()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) appQuit(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if s.hooks.Quit == nil {
		fail(w, http.StatusNotImplemented, "当前模式不支持从界面退出")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
	go s.hooks.Quit()
}

// ---- websocket & static ----------------------------------------------------------------------------

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	// Same-origin only: the default origin check rejects cross-site pages.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Debug("ws accept failed", "err", err)
		return
	}
	s.hub.serve(r.Context(), conn)
}

func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	if s.static == nil {
		fail(w, http.StatusNotFound, "界面未构建：请先在 web/ 下执行 npm run build，再重新编译")
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p != "" {
		if st, err := fs.Stat(s.static, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.static, p)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.static, "index.html")
}

