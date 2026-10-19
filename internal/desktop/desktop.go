// Package desktop shows the UI in a native window (WebView2 on Windows, WKWebView on macOS) with a
// tray / menu-bar icon. Closing the window only hides it — recording continues — and the tray's
// "退出" (or Cmd+Q) runs the app's graceful shutdown.
//
// Run must be called from the main goroutine, which must be locked to the main OS thread
// (see cmd/streamcap: runtime.LockOSThread in init).
package desktop

import (
	_ "embed"
	"errors"
	"sync"
)

//go:embed assets/app.ico
var appICO []byte

//go:embed assets/app.png
var appPNG []byte

//go:embed assets/tray_template.png
var trayTemplate []byte

// ErrUnsupported means this platform has no native shell; use the browser instead.
var ErrUnsupported = errors.New("native window not supported on this platform")

// ErrNoWebView means the platform's web view runtime is missing (WebView2 on old Windows 10).
var ErrNoWebView = errors.New("web view runtime not available")

// OpenWatcher opens url in a hidden browser page and forwards every raw frame of the page's
// comment websocket to onFrame. It returns a function that closes the page. It is nil on platforms
// without a usable web view (Linux), and in -headless mode there is no run loop, so callers must
// not use it there either.
var OpenWatcher func(url string, onFrame func([]byte)) (close func())

// WatcherSupported reports whether this build can run the hidden page watcher.
func WatcherSupported() bool { return OpenWatcher != nil }

// Options configure the shell.
type Options struct {
	URL      string
	DataDir  string // WebView2 profile lives under here
	Window   bool   // false = tray only (browser mode)
	DevTools bool   // allow right-click → Inspect
	// Callbacks from the tray / app menu. They run on a background goroutine.
	OpenBrowser func()
	OpenFolder  func()
	Quit        func() // start graceful shutdown; the app calls Terminate when done
}

// shell is implemented per platform.
type shell interface {
	show()
	terminate()
}

var (
	mu      sync.Mutex
	current shell
	pending []func(shell) // calls made before the shell existed
)

func setCurrent(s shell) {
	mu.Lock()
	current = s
	p := pending
	pending = nil
	mu.Unlock()
	for _, f := range p {
		f(s)
	}
}

func with(f func(shell)) {
	mu.Lock()
	s := current
	if s == nil {
		pending = append(pending, f)
	}
	mu.Unlock()
	if s != nil {
		f(s)
	}
}

// Show brings the window to the front (opens the browser in tray-only mode). Safe from any goroutine.
func Show() { with(func(s shell) { s.show() }) }

// Terminate ends Run. Safe from any goroutine.
func Terminate() { with(func(s shell) { s.terminate() }) }
