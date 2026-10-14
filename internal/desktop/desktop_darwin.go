//go:build darwin

package desktop

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "darwin.h"
*/
import "C"

import (
	"os"
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
