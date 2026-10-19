//go:build darwin

package desktop

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework UserNotifications -framework WebKit
#include <stdlib.h>
#include "darwin.h"
*/
import "C"

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"fyne.io/systray"
	webview "github.com/webview/webview_go"
)

type macShell struct {
	w        webview.WebView
	browser  func()
	quit     func()
	quitOnce sync.Once
	quitCh   chan struct{}
}

var mac *macShell

//export goQuitRequested
func goQuitRequested() {
	if mac != nil && mac.quit != nil {
		go mac.quit()
	}
}

// Run shows the window (or only the menu-bar icon when opts.Window is false) and blocks until Terminate.
func Run(opts Options) error {
	runtime.LockOSThread()
	s := &macShell{browser: opts.OpenBrowser, quit: opts.Quit, quitCh: make(chan struct{})}
	mac = s

	if !opts.Window {
		// Menu-bar only: systray owns the main run loop.
		setCurrent(s)
		systray.Run(trayReady(opts, s.show), nil)
		return nil
	}

	s.w = webview.New(opts.DevTools)
	if s.w == nil {
		return ErrNoWebView
	}
	s.w.SetTitle("StreamCap")
	s.w.SetSize(1280, 820, webview.HintNone)
	inBundle := strings.Contains(executable(), ".app/Contents/MacOS/")
	setIcon := 1
	if inBundle {
		setIcon = 0 // the bundle's AppIcon.icns is used
	}
	C.sc_setup(s.w.Window(), unsafe.Pointer(&appPNG[0]), C.int(len(appPNG)), C.int(setIcon))

	// The menu-bar icon attaches to webview's run loop.
	start, end := systray.RunWithExternalLoop(trayReady(opts, s.show), nil)
	s.w.Dispatch(start)
	setCurrent(s)
	s.w.Navigate(opts.URL)
	s.w.Run()
	end()
	s.w.Destroy()
	return nil
}

func (s *macShell) show() {
	if s.w == nil {
		if s.browser != nil {
			s.browser()
		}
		return
	}
	s.w.Dispatch(func() { C.sc_show() })
}

func (s *macShell) terminate() {
	s.quitOnce.Do(func() {
		close(s.quitCh)
		if s.w != nil {
			s.w.Dispatch(func() { s.w.Terminate() })
		} else {
			systray.Quit()
		}
	})
}

func executable() string {
	p, _ := os.Executable()
	return p
}

// Alert is unused on macOS (errors are logged; the app runs from a terminal or Finder).
func Alert(string, string) {}

// Notify shows a Notification Center banner; clicking it opens url. Outside an .app bundle (go run,
// the bare binary) Notification Center refuses unbundled processes, so it falls back to AppleScript,
// whose banners come from "Script Editor" and can't open the link.
func Notify(title, body, url string) error {
	ct, cb, cu := C.CString(title), C.CString(body), C.CString(url)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cb))
	defer C.free(unsafe.Pointer(cu))
	res := C.sc_notify(ct, cb, cu)
	if res == nil {
		return nil
	}
	msg := C.GoString(res)
	C.free(unsafe.Pointer(res))
	switch {
	case strings.HasPrefix(msg, "denied"):
		return fmt.Errorf("系统通知被关闭了：到「系统设置 → 通知 → StreamCap」里允许通知（%s）", msg)
	case msg == "not running from an .app bundle":
		return osascriptNotify(title, body)
	default:
		return fmt.Errorf("发送系统通知失败：%s", msg)
	}
}

// OpenNotificationSettings opens System Settings at StreamCap's notification switch.
func OpenNotificationSettings() error {
	return exec.Command("open", "x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=io.github.aichiqiezi.streamcap").Run()
}

func osascriptNotify(title, body string) error {
	// Pass the text as arguments rather than splicing it into the script: no quoting to get wrong.
	script := `on run argv
display notification (item 2 of argv) with title (item 1 of argv) sound name "default"
end run`
	if out, err := exec.Command("osascript", "-e", script, title, body).CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- hidden page watcher ----

var watcherMu sync.Mutex
var watchers = map[int64]func([]byte){}
var watcherSeq int64

//export goDanmakuFrame
func goDanmakuFrame(wid C.longlong, data unsafe.Pointer, n C.int) {
	if n <= 0 {
		return
	}
	watcherMu.Lock()
	fn := watchers[int64(wid)]
	watcherMu.Unlock()
	if fn != nil {
		fn(C.GoBytes(data, n))
	}
}

// openWatcher is the macOS implementation: an off-screen WKWebView with a script injected before
// the page loads (see darwin.m). Only available when the app has a run loop, i.e. window / menu-bar
// mode; -headless leaves it nil.
func init() {
	OpenWatcher = openWatcher
}

func openWatcher(url string, onFrame func([]byte)) func() {
	// The view is created on the main queue, which the app's run loop services; a call made before
	// the loop starts (a room already live at launch) is simply queued.
	cu := C.CString(url)
	defer C.free(unsafe.Pointer(cu))
	watcherMu.Lock()
	watcherSeq++
	id := watcherSeq
	watchers[id] = onFrame
	watcherMu.Unlock()
	C.sc_watcher_open(cu, C.longlong(id))
	return func() {
		watcherMu.Lock()
		delete(watchers, id)
		watcherMu.Unlock()
		C.sc_watcher_close(C.longlong(id))
	}
}
