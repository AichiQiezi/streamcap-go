//go:build windows

package desktop

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// Douyin hands comments only to the page itself: the websocket URL carries a signature the page's
// own script produces, bound to the room and viewer, so it can't be reused or forged here. The page
// therefore runs in a hidden WebView2 window and its comment websocket is forwarded frame by frame.
// The script is read-only: it never sends anything on the page's behalf, heartbeats and replies stay
// the page's business.
const watcherJS = `(function(){
  var W = window.WebSocket;
  if (!W || W.__scWrapped) return;
  function b64(buf){ var b=new Uint8Array(buf), s='', n=0x8000;
    for (var i=0;i<b.length;i+=n) s+=String.fromCharCode.apply(null,b.subarray(i,i+n));
    return btoa(s); }
  function send(d){ try{
      if (d instanceof ArrayBuffer) window.chrome.webview.postMessage(b64(d));
      else if (typeof Blob!=='undefined' && d instanceof Blob)
        d.arrayBuffer().then(function(x){ window.chrome.webview.postMessage(b64(x)); });
    }catch(e){} }
  function Wrapped(url, protocols){
    var ws = protocols===undefined ? new W(url) : new W(url, protocols);
    try{ if (String(url).indexOf('/webcast/im/push/v2/')>=0)
      ws.addEventListener('message', function(ev){ send(ev.data); }); }catch(e){}
    return ws; }
  Wrapped.prototype = W.prototype;
  Wrapped.CONNECTING=W.CONNECTING; Wrapped.OPEN=W.OPEN; Wrapped.CLOSING=W.CLOSING; Wrapped.CLOSED=W.CLOSED;
  Wrapped.__scWrapped = true; window.WebSocket = Wrapped;
  function mute(){ try{ var l=document.querySelectorAll('video,audio'); for (var i=0;i<l.length;i++) l[i].muted=true; }catch(e){} }
  setInterval(mute, 2000);
  document.addEventListener('DOMContentLoaded', mute);
})();`

const (
	watcherW      = 1280 // a real, normally-sized page: a zero-sized one stops working
	watcherH      = 800
	watcherOffX   = -32000 // far off-screen, same trick as the macOS watcher
	watcherOffY   = -32000
	watcherSilent = 2 * time.Minute // no frames this long: reload the page

	wsPopup          = 0x80000000
	wsExToolWindow   = 0x00000080 // no taskbar button, no Alt+Tab entry
	wsExNoActivate   = 0x08000000 // never takes focus (so no visible flash either)
	swShowNoActivate = 4

	scWmDestroy = 0x0002
	scWmSize    = 0x0005
	scWmWatcher = 0x8000 + 17 // WM_APP + 17: run a function queued on the page's own thread
	scWmShut    = 0x8000 + 18 // WM_APP + 18: leave the page, then destroy the window
	scClassName = "streamcapWatch"
)

var (
	watcherUser32              = windows.NewLazySystemDLL("user32.dll")
	procWatcherRegisterClass   = watcherUser32.NewProc("RegisterClassExW")
	procWatcherCreateWindow    = watcherUser32.NewProc("CreateWindowExW")
	procWatcherDefWindowProc   = watcherUser32.NewProc("DefWindowProcW")
	procWatcherDestroyWindow   = watcherUser32.NewProc("DestroyWindow")
	procWatcherShowWindow      = watcherUser32.NewProc("ShowWindow")
	procWatcherPostMessage     = watcherUser32.NewProc("PostMessageW")
	procWatcherGetMessage      = watcherUser32.NewProc("GetMessageW")
	procWatcherTranslateMsg    = watcherUser32.NewProc("TranslateMessage")
	procWatcherDispatchMsg     = watcherUser32.NewProc("DispatchMessageW")
	procWatcherPostQuitMessage = watcherUser32.NewProc("PostQuitMessage")

	watcherKernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procWatcherGetModuleHandle = watcherKernel32.NewProc("GetModuleHandleW")
	watcherOle32               = windows.NewLazySystemDLL("ole32.dll")
	procWatcherCoInitialize    = watcherOle32.NewProc("CoInitializeEx")

	watcherClassOnce sync.Once
	watcherHosts     sync.Map // hwnd -> *watcherWin

	// One profile for every page, so the whole run shares a single WebView2 browser process.
	watcherProfile     string
	watcherProfileOnce sync.Once
)

func init() {
	OpenWatcher = openWatcher
}

type watcherWin struct {
	hwnd uintptr
	c    *edge.Chromium
	dir  string
	done chan struct{}
	stop atomic.Bool

	mu     sync.Mutex
	queue  []func()
	lastAt time.Time
}

// openWatcher starts the page on a thread of its own: WebView2 needs the thread that created it to
// keep pumping its message loop, and the app's own window already owns the main one.
func openWatcher(url string, onFrame func([]byte)) func() {
	w := &watcherWin{
		dir:  watcherProfileDir(),
		done: make(chan struct{}),
	}
	go w.run(url, onFrame)
	return func() {
		w.stop.Store(true)
		if hwnd := w.window(); hwnd != 0 {
			_, _, _ = procWatcherPostMessage.Call(hwnd, scWmShut, 0, 0)
		}
		<-w.done
	}
}

// watcherProfileDir is a throwaway WebView2 profile: no account state, nothing kept between runs.
// Earlier runs' profiles are removed the first time one is needed; one still in use by another
// running instance is locked, fails to delete and is left alone.
func watcherProfileDir() string {
	watcherProfileOnce.Do(func() {
		watcherProfile = filepath.Join(os.TempDir(), fmt.Sprintf("streamcap-watch-%d", os.Getpid()))
		old, _ := filepath.Glob(filepath.Join(os.TempDir(), "streamcap-watch-*"))
		for _, dir := range old {
			if dir != watcherProfile {
				_ = os.RemoveAll(dir)
			}
		}
	})
	return watcherProfile
}

func (w *watcherWin) window() uintptr {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hwnd
}

// dispatch runs f on the page's thread (the one that pumps its message loop).
func (w *watcherWin) dispatch(f func()) {
	w.mu.Lock()
	w.queue = append(w.queue, f)
	hwnd := w.hwnd
	w.mu.Unlock()
	if hwnd != 0 {
		_, _, _ = procWatcherPostMessage.Call(hwnd, scWmWatcher, 0, 0)
	}
}

func (w *watcherWin) run(url string, onFrame func([]byte)) {
	runtime.LockOSThread()
	defer close(w.done)
	// The page gets a thread of its own, so COM has to be set up on that thread too (the edge
	// package only does it for the thread it was initialised on).
	if r, _, _ := procWatcherCoInitialize.Call(0, 2 /* COINIT_APARTMENTTHREADED */); int32(r) < 0 && uint32(r) != 0x80010106 {
		slog.Warn("danmaku watcher: CoInitializeEx failed", "hr", fmt.Sprintf("%08x", uint32(r)))
	}
	defer w.stop.Store(true) // the watchdog goroutine (started below) must not outlive the page
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("danmaku watcher: crashed", "err", r)
		}
	}()
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		slog.Warn("danmaku watcher: no profile dir", "err", err)
		return
	}
	if w.stop.Load() {
		return
	}
	hwnd, err := createWatcherWindow()
	if err != nil {
		slog.Warn("danmaku watcher: cannot create window", "err", err)
		return
	}
	w.mu.Lock()
	w.hwnd = hwnd
	w.lastAt = time.Now()
	w.mu.Unlock()
	watcherHosts.Store(hwnd, w)
	defer watcherHosts.Delete(hwnd)
	if w.stop.Load() {
		// close() ran while the window was being created and had no handle to post to yet.
		w.destroy(hwnd)
		return
	}

	c := edge.NewChromium()
	c.DataPath = w.dir // a throwaway profile: no account state, nothing kept between runs
	c.MessageCallback = func(s string) {
		data, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(data) == 0 {
			return
		}
		w.touch()
		onFrame(data)
	}
	if !c.Embed(hwnd) {
		slog.Warn("danmaku watcher: WebView2 unavailable")
		w.destroy(hwnd)
		return
	}
	w.c = c

	if s, err := c.GetSettings(); err == nil {
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutIsZoomControlEnabled(false)
		_ = s.PutIsStatusBarEnabled(false)
	}
	// Skip the video itself: the page only needs its comment websocket, and a live stream is a lot
	// of bandwidth to pull for nothing.
	c.WebResourceRequestedCallback = func(req *edge.ICoreWebView2WebResourceRequest, args *edge.ICoreWebView2WebResourceRequestedEventArgs) {
		uri, err := req.GetUri()
		if err != nil || !isMediaURL(uri) {
			return
		}
		resp, err := c.Environment().CreateWebResourceResponse(nil, 204, "No Content", "")
		if err != nil {
			return
		}
		_ = args.PutResponse(resp)
	}
	c.AddWebResourceRequestedFilter("*", edge.COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL)
	c.Init(watcherJS)
	c.Navigate(url)
	slog.Info("danmaku watcher: watching page", "url", url)

	// Douyin sends a heartbeat well within this window; a long silence means a dead page.
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for range t.C {
			if w.stop.Load() {
				return
			}
			if w.since() > watcherSilent {
				w.touch()
				slog.Info("danmaku watcher: no frames, reloading page", "url", url)
				w.dispatch(func() { c.Navigate(url) })
			}
		}
	}()

	w.pump()
}

// destroy tears the window down and drains the thread's WM_QUIT, so a later page that happens to
// reuse this OS thread doesn't inherit a quit message and exit at once.
func (w *watcherWin) destroy(hwnd uintptr) {
	_, _, _ = procWatcherDestroyWindow.Call(hwnd)
	w.pump()
}

func (w *watcherWin) touch() {
	w.mu.Lock()
	w.lastAt = time.Now()
	w.mu.Unlock()
}

func (w *watcherWin) since() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Since(w.lastAt)
}

func isMediaURL(uri string) bool {
	for _, ext := range []string{".flv", ".m3u8", ".ts", ".m4s", ".mp4"} {
		if i := indexFold(uri, ext+"?"); i >= 0 || hasSuffixFold(uri, ext) {
			return true
		}
	}
	return false
}

func createWatcherWindow() (uintptr, error) {
	watcherClassOnce.Do(func() { registerWatcherClass() })
	hinst := moduleHandle()
	cn, _ := windows.UTF16PtrFromString(scClassName)
	title, _ := windows.UTF16PtrFromString("StreamCap danmaku")
	offX, offY := int32(watcherOffX), int32(watcherOffY)
	hwnd, _, err := procWatcherCreateWindow.Call(
		uintptr(wsExToolWindow|wsExNoActivate),
		uintptr(unsafe.Pointer(cn)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsPopup),
		uintptr(uint32(offX)), uintptr(uint32(offY)),
		watcherW, watcherH,
		0, 0, uintptr(hinst), 0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW: %w", err)
	}
	// Shown, but off-screen and never activated: a hidden WebView2 stops working.
	_, _, _ = procWatcherShowWindow.Call(hwnd, swShowNoActivate)
	return hwnd, nil
}

func registerWatcherClass() {
	hinst := moduleHandle()
	cn, _ := windows.UTF16PtrFromString(scClassName)
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   windows.NewCallback(watcherWndProc),
		HInstance:     hinst,
		LpszClassName: cn,
	}
	if r, _, err := procWatcherRegisterClass.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		slog.Warn("danmaku watcher: RegisterClassExW failed", "err", err)
	}
}

func watcherWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if v, ok := watcherHosts.Load(hwnd); ok {
		w := v.(*watcherWin)
		switch msg {
		case scWmSize:
			if w.c != nil {
				w.c.Resize()
			}
			return 0
		case scWmWatcher:
			w.mu.Lock()
			q := w.queue
			w.queue = nil
			w.mu.Unlock()
			for _, f := range q {
				f()
			}
			return 0
		case scWmShut:
			// Leaving the page first closes its websocket; only then is the window torn down.
			if w.c != nil {
				w.c.Navigate("about:blank")
			}
			_, _, _ = procWatcherDestroyWindow.Call(hwnd)
			return 0
		case scWmDestroy:
			watcherHosts.Delete(hwnd)
			_, _, _ = procWatcherPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := procWatcherDefWindowProc.Call(hwnd, msg, wp, lp)
	return r
}

// pump runs the page's message loop until the window is destroyed.
func (w *watcherWin) pump() {
	var msg msgT
	for {
		r, _, _ := procWatcherGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		_, _, _ = procWatcherTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procWatcherDispatchMsg.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

func hasSuffixFold(s, sub string) bool {
	return len(s) >= len(sub) && equalFold(s[len(s)-len(sub):], sub)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func moduleHandle() windows.Handle {
	h, _, _ := procWatcherGetModuleHandle.Call(0)
	return windows.Handle(h)
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type msgT struct {
	Hwnd     windows.HWND
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       struct{ X, Y int32 }
	LPrivate uint32
}
