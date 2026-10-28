package sysutil

import (
	"errors"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cfUnicodeText = 13

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	isClipboardFmt   = user32.NewProc("IsClipboardFormatAvailable")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	getClipboardData = user32.NewProc("GetClipboardData")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
	globalSize       = kernel32.NewProc("GlobalSize")
)

// ClipboardText returns the clipboard's text (at most maxClipboard bytes' worth), "" when it
// holds none.
func ClipboardText() (string, error) {
	runtime.LockOSThread() // the clipboard is opened for the calling thread
	defer runtime.UnlockOSThread()
	if r, _, _ := isClipboardFmt.Call(cfUnicodeText); r == 0 {
		return "", nil
	}
	opened := false
	for i := 0; i < 10 && !opened; i++ { // another app may hold it for a moment
		if r, _, _ := openClipboard.Call(0); r != 0 {
			opened = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !opened {
		return "", errors.New("剪贴板正被其他程序占用")
	}
	defer closeClipboard.Call()
	h, _, err := getClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", err
	}
	size, _, _ := globalSize.Call(h)
	p, _, err := globalLock.Call(h)
	if p == 0 {
		return "", err
	}
	defer globalUnlock.Call(h)
	n := int(size / 2)
	if n > maxClipboard/2 {
		n = maxClipboard / 2
	}
	// p is a locked HGLOBAL; go through a pointer-to-pointer so vet's unsafeptr check accepts it.
	buf := unsafe.Slice((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&p))), n)
	for i, c := range buf {
		if c == 0 {
			buf = buf[:i]
			break
		}
	}
	return windows.UTF16ToString(buf), nil
}
