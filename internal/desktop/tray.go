//go:build windows || darwin

package desktop

import (
	"runtime"

	"fyne.io/systray"
)

// trayReady builds the tray menu. show is how "打开" reaches the window (or the browser).
func trayReady(opts Options, show func()) func() {
	return func() {
		if runtime.GOOS == "darwin" {
			systray.SetTemplateIcon(trayTemplate, trayTemplate)
		} else {
			systray.SetIcon(appICO)
			systray.SetTitle("StreamCap")
		}
		systray.SetTooltip("StreamCap 直播录制")
		systray.SetOnTapped(show) // Windows: left click opens the window

		open := systray.AddMenuItem("打开 StreamCap", "显示主窗口")
		browser := systray.AddMenuItem("在浏览器中打开", "用默认浏览器打开界面")
		folder := systray.AddMenuItem("打开录像文件夹", "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("退出", "停止录制、保存文件后退出")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					show()
				case <-browser.ClickedCh:
					call(opts.OpenBrowser)
				case <-folder.ClickedCh:
					call(opts.OpenFolder)
				case <-quit.ClickedCh:
					quit.Disable()
					quit.SetTitle("正在退出…")
					call(opts.Quit)
				}
			}
		}()
	}
}

// SetStatus updates the tray tooltip (e.g. "正在录制 2 个直播间").
func SetStatus(text string) {
	if text == "" {
		text = "StreamCap 直播录制"
	}
	systray.SetTooltip(text)
}

func call(f func()) {
	if f != nil {
		f()
	}
}
