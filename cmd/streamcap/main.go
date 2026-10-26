// Command streamcap records Douyin and Bilibili live streams.
//
// By default it opens a native window (WebView2 on Windows, WKWebView on macOS) with a tray /
// menu-bar icon; closing the window keeps recording in the background.
//
//	streamcap              native window
//	streamcap -browser     use the default browser instead (handy while developing the UI)
//	streamcap -devtools    allow right-click → Inspect in the native window
//	streamcap -headless    no UI at all (servers); stop with Ctrl+C / SIGTERM
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/api"
	"github.com/AichiQiezi/streamcap-go/internal/desktop"
	"github.com/AichiQiezi/streamcap-go/internal/monitor"
	"github.com/AichiQiezi/streamcap-go/internal/notify"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
	_ "github.com/AichiQiezi/streamcap-go/internal/platform/bilibili"
	_ "github.com/AichiQiezi/streamcap-go/internal/platform/douyin"
	"github.com/AichiQiezi/streamcap-go/internal/recorder"
	"github.com/AichiQiezi/streamcap-go/internal/store"
	"github.com/AichiQiezi/streamcap-go/internal/sysutil"
	"github.com/AichiQiezi/streamcap-go/internal/upload"
	"github.com/AichiQiezi/streamcap-go/web"
)

var version = "dev"

// GUI toolkits must run on the process's main thread.
func init() { runtime.LockOSThread() }

func main() {
	paths := resolvePaths()
	host := flag.String("host", "127.0.0.1", "listen address (anything but loopback exposes an unauthenticated API)")
	port := flag.Int("port", 6008, "listen port")
	dataDir := flag.String("data", paths.data, "folder for settings, room list and thumbnails")
	browser := flag.Bool("browser", false, "open the UI in the default browser instead of a native window")
	devtools := flag.Bool("devtools", false, "enable developer tools (right-click → Inspect) in the native window")
	headless := flag.Bool("headless", false, "no window, no browser, no tray")
	flag.Bool("no-open", false, "deprecated: same as -headless") // kept for old launch scripts
	flag.Parse()
	if f := flag.Lookup("no-open"); f != nil && f.Value.String() == "true" {
		*headless = true
	}

	logFile := setupLogging(paths.logs)
	defer logFile.Close()
	slog.Info("streamcap starting", "version", version, "data", *dataDir, "downloads", paths.downloads)

	if ff, fp := recorder.Binaries(); ff == "" || fp == "" {
		slog.Error("ffmpeg/ffprobe not found")
		alert("找不到 ffmpeg", "请安装 ffmpeg，或把 ffmpeg 和 ffprobe 放到程序同目录。\n\n程序会继续运行，但在此之前无法录制。")
	}

	recorder.SetStateDir(*dataDir)
	recorder.ReapOrphans() // ffmpeg left running by a previous instance that was killed

	addr := net.JoinHostPort(*host, fmt.Sprint(*port))
	url := fmt.Sprintf("http://127.0.0.1:%d", *port)
	if isLoopback(*host) {
		url = "http://" + addr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if isAddrInUse(err) && wakeRunningInstance(url) {
			slog.Info("already running; asked it to show its window")
			return
		}
		fatal("无法启动", fmt.Sprintf("端口 %d 被占用或无法监听：%v\n可以用 -port 换一个端口。", *port, err))
	}

	st, err := store.Open(*dataDir)
	if err != nil {
		fatal("无法启动", fmt.Sprintf("无法读取数据目录 %s：%v", *dataDir, err))
	}
	m := monitor.New(st, monitor.Options{DefaultSaveDir: paths.downloads, ThumbDir: filepath.Join(*dataDir, "thumbs")})
	up := upload.New(st, m.Settings)
	m.OnSessionSaved(func(s monitor.SavedSession) { up.SessionSaved(s.Room, s.Anchor, s.Title, s.Start, s.Files) })

	loopback := isLoopback(*host)
	if !loopback {
		slog.Warn("listening on a non-loopback address: the API has NO authentication", "addr", addr)
	}
	static := web.Dist()
	if static == nil {
		slog.Warn("web UI not embedded: run `npm run build` in web/ before `go build`")
	}

	if !*headless {
		notify.SystemNotifier = desktop.Notify
		// Douyin comments only come from a page the app runs itself (see internal/desktop).
		platform.OpenWatcher = desktop.OpenWatcher
	}
	windowMode := !*browser && !*headless
	app := &lifecycle{m: m, done: make(chan struct{}), window: windowMode}
	srv := &http.Server{
		Handler: api.New(m, up, static, loopback, api.Hooks{
			Show: app.show(url), Quit: app.quit, OpenNotificationSettings: desktop.OpenNotificationSettings,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	app.srv = srv
	m.Start()
	upCtx, stopUploads := context.WithCancel(context.Background())
	defer stopUploads()
	up.Start(upCtx)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server stopped", "err", err)
		}
	}()
	slog.Info("ready", "url", url)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP) // SIGHUP: terminal window closed
	go func() {
		s := <-sig
		slog.Info("signal received, shutting down", "signal", s)
		app.quit()
	}()
	go trayStatus(m)

	opts := desktop.Options{
		URL: url, DataDir: *dataDir, DevTools: *devtools,
		OpenBrowser: func() { openBrowser(url) },
		OpenFolder:  func() { _ = sysutil.OpenFolder(m.SaveRoot()) },
		Quit:        app.quit,
	}
	switch {
	case *headless:
		fmt.Printf("\n  StreamCap 已启动：%s\n  按 Ctrl+C 退出（会先保存正在录制的文件）\n\n", url)
		<-app.done // runs until a signal (or the UI's 退出) triggers shutdown
	case windowMode:
		opts.Window = true
		err := desktop.Run(opts) // blocks until Terminate
		if err == nil {
			break
		}
		// No native window here (Linux, or WebView2 missing): browser + tray instead.
		slog.Warn("native window unavailable, using the browser", "err", err)
		if errors.Is(err, desktop.ErrNoWebView) {
			alert("将使用浏览器打开", "未检测到 Microsoft Edge WebView2 运行时，本次改用浏览器打开界面。\n安装 WebView2 后即可使用独立窗口。")
		}
		app.window = false
		openBrowser(url)
		opts.Window = false
		if err := desktop.Run(opts); err != nil {
			<-app.done
		}
	default: // -browser
		openBrowser(url)
		opts.Window = false
		if err := desktop.Run(opts); err != nil { // no tray on this platform: run until a signal
			fmt.Printf("\n  StreamCap 已启动：%s\n  按 Ctrl+C 退出（会先保存正在录制的文件）\n\n", url)
			<-app.done
		}
	}
	app.quit() // the UI loop ended: make sure shutdown ran
	<-app.done
	slog.Info("bye")
}

// lifecycle runs the one graceful shutdown, whichever path asks for it first.
type lifecycle struct {
	m      *monitor.Manager
	srv    *http.Server
	window bool
	once   sync.Once
	done   chan struct{}
}

func (a *lifecycle) show(url string) func() {
	return func() {
		if a.window {
			desktop.Show()
		} else {
			openBrowser(url)
		}
	}
}

func (a *lifecycle) quit() {
	a.once.Do(func() {
		slog.Info("quit requested")
		go func() {
			defer close(a.done)
			if a.window {
				desktop.Show() // the UI shows "正在停止录制并保存…" while we finish
			}
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			a.m.Shutdown(ctx) // stops ffmpeg gracefully, merges/remuxes finished sessions
			sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = a.srv.Shutdown(sctx)
			scancel()
			slog.Info("shutdown complete", "took", time.Since(start).Round(time.Millisecond))
			desktop.Terminate()
		}()
	})
}

// trayStatus keeps the tray text current, and keeps the computer from sleeping on idle while
// anything records (a sleeping laptop silently ends every recording).
func trayStatus(m *monitor.Manager) {
	last, awake := "", false
	for range time.Tick(3 * time.Second) {
		n := 0
		for _, v := range m.Views() {
			if v.State.Phase == monitor.PhaseRecording || v.State.Phase == monitor.PhaseReconnecting {
				n++
			}
		}
		text := "StreamCap 直播录制"
		if n > 0 {
			text = fmt.Sprintf("StreamCap · 正在录制 %d 个直播间", n)
		}
		if text != last {
			desktop.SetStatus(text)
			last = text
		}
		if busy := n > 0; busy != awake {
			sysutil.KeepAwake(busy)
			awake = busy
		}
	}
}

// wakeRunningInstance asks an already-running StreamCap on this port to show itself.
func wakeRunningInstance(url string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Post(url+"/api/app/show", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type appPaths struct{ data, downloads, logs string }

// resolvePaths decides where data/, downloads/ and logs/ live:
//   - macOS .app bundle: ~/Library/Application Support, ~/Movies/StreamCap, ~/Library/Logs
//   - otherwise next to the executable (portable), or %LOCALAPPDATA% if that folder isn't writable
//   - `go run`: the working directory
func resolvePaths() appPaths {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	home, _ := os.UserHomeDir()
	switch {
	case strings.Contains(exe, ".app/Contents/MacOS"):
		return appPaths{
			data:      filepath.Join(home, "Library", "Application Support", "StreamCap"),
			downloads: filepath.Join(home, "Movies", "StreamCap"),
			logs:      filepath.Join(home, "Library", "Logs", "StreamCap"),
		}
	case strings.Contains(dir, "go-build") || strings.HasPrefix(dir, os.TempDir()):
		dir, _ = os.Getwd()
	case !writable(dir):
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dir = filepath.Join(local, "StreamCap")
		} else {
			dir = filepath.Join(home, ".streamcap")
		}
	}
	return appPaths{data: filepath.Join(dir, "data"), downloads: filepath.Join(dir, "downloads"), logs: filepath.Join(dir, "logs")}
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func setupLogging(dir string) *os.File {
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "streamcap.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 20<<20 {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	var w io.Writer = os.Stderr
	switch {
	case err != nil:
	case isTerminal(os.Stderr):
		// Run from a terminal: echo the log there too. (File first: MultiWriter stops at the first
		// failing writer.)
		w = io.MultiWriter(f, os.Stderr)
	default:
		// Started from Finder / Explorer: stderr goes nowhere, so send it to the log as well. Then a
		// Go panic or an AppKit / WebView2 error that kills the process still leaves a trace.
		w = f
		if rerr := redirectStderr(f); rerr != nil {
			defer slog.Warn("could not redirect stderr to the log", "err", rerr)
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return f
}


func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use") ||
		strings.Contains(err.Error(), "Only one usage")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// alert shows a message box on Windows (the GUI build has no console) and logs everywhere.
func alert(title, msg string) {
	slog.Warn(title + ": " + msg)
	desktop.Alert(title, msg)
}

func fatal(title, msg string) {
	slog.Error(title + ": " + msg)
	fmt.Fprintln(os.Stderr, title+"："+msg)
	desktop.Alert(title, msg)
	os.Exit(1)
}
