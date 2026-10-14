//go:build windows

package desktop

import (
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"fyne.io/systray"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	user32             = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongW = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProc = user32.NewProc("CallWindowProcW")
	procShowWindow     = user32.NewProc("ShowWindow")
	procSetForeground  = user32.NewProc("SetForegroundWindow")
	procIsIconic       = user32.NewProc("IsIconic")
	procMessageBox     = user32.NewProc("MessageBoxW")
)

const (
	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4
	wmClose     = 0x0010
	swHide      = 0
	swShow      = 5
	swRestore   = 9
)

type winShell struct {
	w        webview2.WebView
	hwnd     uintptr
	origProc uintptr
	browser  func()
	quitOnce sync.Once
	quitCh   chan struct{}
}

var active *winShell // the window procedure needs it; there is only ever one window

// Run shows the window (or only the tray when opts.Window is false) and blocks until Terminate.
func Run(opts Options) error {
	runtime.LockOSThread()
	s := &winShell{browser: opts.OpenBrowser, quitCh: make(chan struct{})}

	if opts.Window {
		s.w = webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:     opts.DevTools,
			AutoFocus: true,
			DataPath:  filepath.Join(opts.DataDir, "webview2"),
			WindowOptions: webview2.WindowOptions{
				Title: "StreamCap", Width: 1280, Height: 820, IconId: 1, Center: true,
			},
		})
		if s.w == nil {
			return ErrNoWebView // WebView2 runtime missing: caller falls back to the browser
		}
	}

	// The tray runs its own message loop on its own thread.
	go func() {
		runtime.LockOSThread()
		systray.Run(trayReady(opts, s.show), nil)
	}()
	setCurrent(s)

	if s.w == nil { // tray-only mode
		<-s.quitCh
		systray.Quit()
		return nil
	}

	s.hwnd = uintptr(s.w.Window())
	active = s
	// Subclass the window: the close button hides it instead of destroying it.
	s.origProc, _, _ = procSetWindowLongW.Call(s.hwnd, gwlpWndProc, windows.NewCallback(wndProc))
	s.w.SetSize(1000, 640, webview2.HintMin)
	s.w.Navigate(opts.URL)
	s.w.Run()
	s.w.Destroy()
	systray.Quit()
	return nil
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	s := active
	if msg == wmClose && s != nil {
		procShowWindow.Call(hwnd, swHide)
		return 0
	}
	r, _, _ := procCallWindowProc.Call(s.origProc, hwnd, msg, wp, lp)
	return r
}

func (s *winShell) show() {
	if s.w == nil {
		if s.browser != nil {
			s.browser()
		}
		return
	}
	s.w.Dispatch(func() {
		if iconic, _, _ := procIsIconic.Call(s.hwnd); iconic != 0 {
			procShowWindow.Call(s.hwnd, swRestore)
		} else {
			procShowWindow.Call(s.hwnd, swShow)
		}
		procSetForeground.Call(s.hwnd)
	})
}

func (s *winShell) terminate() {
	s.quitOnce.Do(func() {
		close(s.quitCh)
		if s.w != nil {
			// PostQuitMessage must run on the UI thread to reach its message loop.
			s.w.Dispatch(func() { s.w.Terminate() })
		}
	})
}

// Alert shows a blocking message box (the GUI build has no console to print to).
func Alert(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	procMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10) // MB_ICONERROR
}
