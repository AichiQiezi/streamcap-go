package upload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/AichiQiezi/streamcap-go/internal/media"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

// Limits of the submit form.
const (
	maxTitle = 80
	maxDesc  = 2000
	maxTags  = 10
	maxTag   = 20
)

// Event tells subscribers that a task changed (Task set), was removed (Removed set), or that the
// account changed (Account true). Handlers must not block.
type Event struct {
	Task    *TaskView
	Removed string
	Account bool
}

// TaskView is a task as the UI sees it, with live progress.
type TaskView struct {
	store.UploadTask
	Names    []string `json:"names"` // file names, for display
	SpeedBps int64    `json:"speed_bps"`
}

// AccountView is the logged-in account without its secrets.
type AccountView struct {
	Mid       int64     `json:"mid"`
	Name      string    `json:"name"`
	Face      string    `json:"face"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Request is a submission the user (or a finished live) asks for.
type Request struct {
	Files     []string `json:"files"` // absolute paths
	Title     string   `json:"title"`
	Desc      string   `json:"desc"`
	Tags      string   `json:"tags"`
	Tid       int      `json:"tid"`
	Copyright int      `json:"copyright"`
	Source    string   `json:"source"`
	RoomID    string   `json:"-"`
}

// Uploader runs upload tasks one at a time, in the order they were queued.
type Uploader struct {
	st       *store.Store
	c        *Client
	settings func() store.Settings

	mu      sync.Mutex
	subs    []func(Event)
	running string             // id of the task being uploaded
	cancel  context.CancelFunc // cancels it
	sent    atomic.Int64       // bytes of the running task sent so far
	speed   atomic.Int64
	parts   []Partition
	partsAt time.Time

	wake chan struct{}
}

// New creates the uploader; call Start to run queued tasks.
func New(st *store.Store, settings func() store.Settings) *Uploader {
	return &Uploader{st: st, c: NewClient(), settings: settings, wake: make(chan struct{}, 1)}
}

// Start resumes the queue (a task cut off by a quit is uploaded again from the start) and
// renews the login token when it is close to expiring.
func (u *Uploader) Start(ctx context.Context) {
	u.st.Uploads.Update(func(d *store.UploadData) {
		for i := range d.Tasks {
			if t := &d.Tasks[i]; t.State == store.UploadUploading || t.State == store.UploadSubmitting {
				t.State, t.Sent = store.UploadQueued, 0
			}
		}
	})
	go u.worker(ctx)
	go u.renew(ctx)
}

// Subscribe registers an event handler.
func (u *Uploader) Subscribe(fn func(Event)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.subs = append(u.subs, fn)
}

func (u *Uploader) emit(ev Event) {
	u.mu.Lock()
	subs := append([]func(Event){}, u.subs...)
	u.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func (u *Uploader) emitTask(id string) {
	if v, ok := u.Task(id); ok {
		u.emit(Event{Task: &v})
	}
}

func (u *Uploader) kick() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// ---- account -----------------------------------------------------------------------------------

func (u *Uploader) account() *store.BiliAccount {
	var a *store.BiliAccount
	u.st.Uploads.Read(func(d store.UploadData) {
		if d.Account != nil {
			cp := *d.Account
			a = &cp
		}
	})
	return a
}

// Account returns the logged-in account, or nil.
func (u *Uploader) Account() *AccountView {
	a := u.account()
	if a == nil {
		return nil
	}
	return &AccountView{Mid: a.Mid, Name: a.Name, Face: a.Face, ExpiresAt: a.ExpiresAt}
}

// StartLogin begins a QR login; the QR code encodes the returned URL.
func (u *Uploader) StartLogin(ctx context.Context) (QRCode, error) {
	return u.c.LoginQR(ctx)
}

// PollLogin checks a QR login and saves the account once it is confirmed.
func (u *Uploader) PollLogin(ctx context.Context, authCode string) (LoginStatus, error) {
	status, acct, err := u.c.PollLogin(ctx, authCode)
	if err != nil || status != LoginDone {
		return status, err
	}
	if p, err := u.c.Me(ctx, acct); err == nil {
		acct.Name, acct.Face = p.Name, p.Face
		if acct.Mid == 0 {
			acct.Mid = p.Mid
		}
	} else {
		slog.Warn("bilibili: logged in but reading the profile failed", "err", err)
	}
	u.st.Uploads.Update(func(d *store.UploadData) { d.Account = acct })
	slog.Info("bilibili: upload account logged in", "mid", acct.Mid, "name", acct.Name)
	u.emit(Event{Account: true})
	u.kick()
	return LoginDone, nil
}

// Logout forgets the account.
func (u *Uploader) Logout() {
	u.st.Uploads.Update(func(d *store.UploadData) { d.Account = nil })
	u.emit(Event{Account: true})
}

func (u *Uploader) renew(ctx context.Context) {
	for {
		if a := u.account(); a != nil && a.RefreshToken != "" && time.Until(a.ExpiresAt) < 30*24*time.Hour {
			fresh, err := u.c.Refresh(ctx, a)
			if err != nil {
				slog.Warn("bilibili: token refresh failed", "err", err)
			} else {
				u.st.Uploads.Update(func(d *store.UploadData) {
					if d.Account != nil && d.Account.AccessToken == a.AccessToken {
						d.Account = fresh
					}
				})
				slog.Info("bilibili: login token renewed", "expires", fresh.ExpiresAt.Format(time.DateOnly))
				u.emit(Event{Account: true})
			}
		}
		if !sleep(ctx, 12*time.Hour) {
			return
		}
	}
}

// Partitions lists the 分区 (cached for an hour).
func (u *Uploader) Partitions(ctx context.Context) ([]Partition, error) {
	u.mu.Lock()
	if u.parts != nil && time.Since(u.partsAt) < time.Hour {
		p := u.parts
		u.mu.Unlock()
		return p, nil
	}
	u.mu.Unlock()
	a := u.account()
	if a == nil {
		return nil, errors.New("请先登录 B站账号")
	}
	parts, err := u.c.Partitions(ctx, a)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.parts, u.partsAt = parts, time.Now()
	u.mu.Unlock()
	return parts, nil
}

// ---- tasks -------------------------------------------------------------------------------------

func (u *Uploader) view(t store.UploadTask) TaskView {
	v := TaskView{UploadTask: t}
	for _, f := range t.Files {
		v.Names = append(v.Names, filepath.Base(f))
	}
	u.mu.Lock()
	running := u.running
	u.mu.Unlock()
	if t.ID == running && t.State == store.UploadUploading {
		v.Sent, v.SpeedBps = u.sent.Load(), u.speed.Load()
	}
	return v
}

// Tasks returns every task, newest first.
func (u *Uploader) Tasks() []TaskView {
	var ts []store.UploadTask
	u.st.Uploads.Read(func(d store.UploadData) { ts = append(ts, d.Tasks...) })
	out := make([]TaskView, 0, len(ts))
	for i := len(ts) - 1; i >= 0; i-- {
		out = append(out, u.view(ts[i]))
	}
	return out
}

// Task returns one task.
func (u *Uploader) Task(id string) (TaskView, bool) {
	var (
		t  store.UploadTask
		ok bool
	)
	u.st.Uploads.Read(func(d store.UploadData) {
		for _, x := range d.Tasks {
			if x.ID == id {
				t, ok = x, true
			}
		}
	})
	if !ok {
		return TaskView{}, false
	}
	return u.view(t), true
}

// ErrNotFound is returned for unknown task ids.
var ErrNotFound = errors.New("上传任务不存在")

func (u *Uploader) update(id string, fn func(*store.UploadTask)) bool {
	found := false
	u.st.Uploads.Update(func(d *store.UploadData) {
		for i := range d.Tasks {
			if d.Tasks[i].ID == id {
				fn(&d.Tasks[i])
				found = true
				return
			}
		}
	})
	return found
}

// Enqueue checks and queues a submission.
func (u *Uploader) Enqueue(r Request) (TaskView, error) {
	r, err := normalize(r)
	if err != nil {
		return TaskView{}, err
	}
	var total int64
	for _, f := range r.Files {
		st, err := os.Stat(f)
		if err != nil || st.IsDir() {
			return TaskView{}, fmt.Errorf("文件不存在：%s", filepath.Base(f))
		}
		total += st.Size()
	}
	t := store.UploadTask{
		ID: newID(), Files: r.Files, Title: r.Title, Desc: r.Desc, Tags: r.Tags, Tid: r.Tid, Copyright: r.Copyright,
		Source: r.Source, RoomID: r.RoomID, State: store.UploadQueued, Total: total, CreatedAt: time.Now(),
	}
	u.st.Uploads.Update(func(d *store.UploadData) { d.Tasks = append(d.Tasks, t) })
	slog.Info("bilibili: upload queued", "title", t.Title, "files", len(t.Files))
	v := u.view(t)
	u.emit(Event{Task: &v})
	u.kick()
	return v, nil
}

func normalize(r Request) (Request, error) {
	r.Title = clip(collapse(r.Title), maxTitle)
	r.Desc = clip(strings.TrimSpace(r.Desc), maxDesc)
	r.Source = clip(strings.TrimSpace(r.Source), 200)
	r.Tags = strings.Join(splitTags(r.Tags), ",")
	switch {
	case len(r.Files) == 0:
		return r, errors.New("没有要上传的文件")
	case r.Title == "":
		return r, errors.New("标题不能为空")
	case r.Tags == "":
		return r, errors.New("至少要有一个标签")
	case r.Tid <= 0:
		return r, errors.New("请选择分区")
	case r.Copyright != 1 && r.Copyright != 2:
		return r, errors.New("请选择自制或转载")
	case r.Copyright == 2 && r.Source == "":
		return r, errors.New("转载需要填写来源")
	}
	if r.Copyright == 1 {
		r.Source = ""
	}
	return r, nil
}

// Retry queues a failed or canceled task again.
func (u *Uploader) Retry(id string) error {
	ok := u.update(id, func(t *store.UploadTask) {
		if t.State == store.UploadFailed || t.State == store.UploadCanceled {
			t.State, t.Error, t.Sent, t.FinishedAt = store.UploadQueued, "", 0, time.Time{}
		}
	})
	if !ok {
		return ErrNotFound
	}
	u.emitTask(id)
	u.kick()
	return nil
}

// Cancel stops a queued or running task.
func (u *Uploader) Cancel(id string) error {
	u.mu.Lock()
	if u.running == id && u.cancel != nil {
		u.cancel() // the worker marks it canceled
	}
	u.mu.Unlock()
	ok := u.update(id, func(t *store.UploadTask) {
		if t.State == store.UploadQueued {
			t.State, t.FinishedAt = store.UploadCanceled, time.Now()
		}
	})
	if !ok {
		return ErrNotFound
	}
	u.emitTask(id)
	return nil
}

// Remove deletes a task from the list (cancelling it if it runs). The files are kept.
func (u *Uploader) Remove(id string) error {
	u.mu.Lock()
	if u.running == id && u.cancel != nil {
		u.cancel()
	}
	u.mu.Unlock()
	found := false
	u.st.Uploads.Update(func(d *store.UploadData) {
		for i := range d.Tasks {
			if d.Tasks[i].ID == id {
				d.Tasks = append(d.Tasks[:i], d.Tasks[i+1:]...)
				found = true
				return
			}
		}
	})
	if !found {
		return ErrNotFound
	}
	u.emit(Event{Removed: id})
	return nil
}

// ---- worker ------------------------------------------------------------------------------------

func (u *Uploader) next() (store.UploadTask, bool) {
	var (
		t  store.UploadTask
		ok bool
	)
	u.st.Uploads.Read(func(d store.UploadData) {
		for _, x := range d.Tasks {
			if x.State == store.UploadQueued {
				t, ok = x, true
				return
			}
		}
	})
	return t, ok
}

func (u *Uploader) worker(ctx context.Context) {
	for {
		if t, ok := u.next(); ok {
			u.run(ctx, t)
			continue
		}
		select {
		case <-u.wake:
		case <-ctx.Done():
			return
		}
	}
}

func (u *Uploader) run(parent context.Context, t store.UploadTask) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	u.mu.Lock()
	u.running, u.cancel = t.ID, cancel
	u.mu.Unlock()
	u.sent.Store(0)
	u.speed.Store(0)
	defer func() {
		u.mu.Lock()
		u.running, u.cancel = "", nil
		u.mu.Unlock()
	}()

	stopTicker := u.reportProgress(t.ID)
	bvid, err := u.upload(ctx, t)
	stopTicker()

	u.update(t.ID, func(x *store.UploadTask) {
		x.FinishedAt = time.Now()
		x.Sent = u.sent.Load()
		switch {
		case err == nil:
			x.State, x.Bvid, x.Error = store.UploadDone, bvid, ""
		case ctx.Err() != nil && parent.Err() == nil:
			x.State, x.Error = store.UploadCanceled, ""
		case parent.Err() != nil: // quitting: run it again next start
			x.State, x.Sent, x.FinishedAt = store.UploadQueued, 0, time.Time{}
		default:
			x.State, x.Error = store.UploadFailed, explain(err)
		}
	})
	if err == nil {
		slog.Info("bilibili: submitted", "title", t.Title, "bvid", bvid)
	} else if ctx.Err() == nil {
		slog.Error("bilibili: upload failed", "title", t.Title, "err", err)
	}
	u.emitTask(t.ID)
}

// reportProgress pushes the running task's progress twice a second until stopped.
func (u *Uploader) reportProgress(id string) func() {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		last, lastAt := int64(0), time.Now()
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				n := u.sent.Load()
				if dt := now.Sub(lastAt).Seconds(); dt > 0 {
					// Smooth over chunk boundaries: bytes are counted a chunk at a time.
					u.speed.Store((u.speed.Load()*3 + int64(float64(n-last)/dt)) / 4)
				}
				last, lastAt = n, now
				u.emitTask(id)
			}
		}
	}()
	return func() { close(done) }
}

func (u *Uploader) upload(ctx context.Context, t store.UploadTask) (string, error) {
	acct := u.account()
	if acct == nil {
		return "", errors.New("还没有登录 B站账号：到「设置 → B站投稿」扫码登录后重试")
	}
	for _, f := range t.Files {
		if _, err := os.Stat(f); err != nil {
			return "", fmt.Errorf("文件不存在：%s", filepath.Base(f))
		}
	}
	u.update(t.ID, func(x *store.UploadTask) { x.State, x.Error, x.Sent = store.UploadUploading, "", 0 })
	u.emitTask(t.ID)

	videos := make([]Video, 0, len(t.Files))
	for i, f := range t.Files {
		name, err := u.c.UploadVideo(ctx, acct, f, func(n int64) { u.sent.Add(n) })
		if err != nil {
			return "", err
		}
		title := ""
		if len(t.Files) > 1 {
			stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
			title = clip(fmt.Sprintf("P%d %s", i+1, stem), maxTitle)
		}
		videos = append(videos, Video{Title: title, Filename: name})
	}

	u.update(t.ID, func(x *store.UploadTask) { x.State, x.Sent = store.UploadSubmitting, u.sent.Load() })
	u.emitTask(t.ID)

	cover := ""
	if jpeg, err := coverFor(ctx, t.Files[0]); err != nil {
		slog.Warn("bilibili: no cover, Bilibili will pick one", "err", err)
	} else if cover, err = u.c.UploadCover(ctx, acct, jpeg); err != nil {
		slog.Warn("bilibili: cover upload failed, Bilibili will pick one", "err", err)
		cover = ""
	}
	return u.c.Submit(ctx, acct, Archive{
		Copyright: t.Copyright, Source: t.Source, Tid: t.Tid, Cover: cover, Title: t.Title, Desc: t.Desc,
		Tag: t.Tags, Videos: videos,
	})
}

// coverFor grabs a frame a little into the video (the first seconds are often black or a loading screen).
func coverFor(ctx context.Context, file string) ([]byte, error) {
	at := 30 * time.Second
	if d, err := media.Duration(ctx, file); err == nil && d/10 < at {
		at = d / 10
	}
	return media.Cover(ctx, file, at)
}

// explain turns errors into something the user can act on.
func explain(err error) string {
	var api *APIError
	if errors.As(err, &api) {
		switch api.Code {
		case -101, -2, 61000, -658:
			return api.Error() + "：登录已失效，请在「设置 → B站投稿」重新扫码登录后重试"
		case 21070, 21015:
			return api.Error() + "：投稿太频繁，稍后再重试"
		}
	}
	return err.Error()
}

// ---- templates & auto upload -------------------------------------------------------------------

// Vars fill the title/description/tag templates.
type Vars struct {
	Anchor, Title, Platform, URL string
	Start                        time.Time
}

// Fill replaces {anchor} {title} {date} {time} {platform} {url} in tmpl.
func Fill(tmpl string, v Vars) string {
	date, clock := "", ""
	if !v.Start.IsZero() {
		date, clock = v.Start.Format("2006-01-02"), v.Start.Format("15:04")
	}
	return strings.NewReplacer("{anchor}", v.Anchor, "{title}", v.Title, "{date}", date, "{time}", clock,
		"{platform}", v.Platform, "{url}", v.URL).Replace(tmpl)
}

// Draft is a submission prefilled from the templates in settings.
func (u *Uploader) Draft(files []string, v Vars) Request {
	s := u.settings()
	cr := s.Int("bili_upload_copyright", 2)
	r := Request{
		Files: files, Title: clip(collapse(Fill(s.String("bili_upload_title"), v)), maxTitle),
		Desc: strings.TrimSpace(Fill(s.String("bili_upload_desc"), v)),
		Tags: strings.Join(splitTags(Fill(s.String("bili_upload_tags"), v)), ","),
		Tid:  s.Int("bili_upload_tid", 21), Copyright: cr,
	}
	if cr == 2 {
		r.Source = v.URL // empty when the room is unknown: the form asks for it
	}
	return r
}

// recName matches recordings named 主播_2026-10-14_19-58-02[_标题][_p2][_001] (internal/monitor/paths.go).
var recName = regexp.MustCompile(`^(.+?)_(\d{4}-\d{2}-\d{2})_(\d{2})-(\d{2})-(\d{2})(?:_(.*?))?(?:_p\d+)?(?:_\d{3})?$`)

// VarsForFile guesses the template values of a recording from its name and the room list.
func (u *Uploader) VarsForFile(file string) Vars {
	stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	v := Vars{Anchor: stem}
	if m := recName.FindStringSubmatch(stem); m != nil {
		v.Anchor, v.Title = m[1], m[6]
		v.Start, _ = time.ParseInLocation("2006-01-02 15-04-05", m[2]+" "+m[3]+"-"+m[4]+"-"+m[5], time.Local)
	} else if st, err := os.Stat(file); err == nil {
		v.Start = st.ModTime()
	}
	var rooms []store.Room
	u.st.Rooms.Read(func(rs []store.Room) { rooms = append(rooms, rs...) })
	for _, r := range rooms {
		if r.Name != "" && (r.Name == v.Anchor || strings.Contains(filepath.ToSlash(file), "/"+r.Name+"/")) {
			v.Anchor, v.URL = r.Name, r.URL
			if p := platform.ByKey(r.Platform); p != nil {
				v.Platform = p.Name()
			}
			break
		}
	}
	return v
}

// SessionSaved is called once a live's files are saved; rooms with auto upload get them queued.
func (u *Uploader) SessionSaved(room store.Room, anchor, title string, start time.Time, files []string) {
	if !room.AutoUpload {
		return
	}
	var videos []string
	var total time.Duration
	for _, f := range files {
		if store.AudioFormats[strings.ToUpper(strings.TrimPrefix(filepath.Ext(f), "."))] {
			continue
		}
		videos = append(videos, f)
		if d, err := media.Duration(context.Background(), f); err == nil {
			total += d
		}
	}
	if len(videos) == 0 {
		return
	}
	min := time.Duration(u.settings().Float("bili_upload_min_minutes", 10) * float64(time.Minute))
	if total < min {
		slog.Info("bilibili: auto upload skipped, recording too short", "anchor", anchor, "length", total.Round(time.Second))
		return
	}
	v := Vars{Anchor: anchor, Title: title, URL: room.URL, Start: start}
	if p := platform.ByKey(room.Platform); p != nil {
		v.Platform = p.Name()
	}
	r := u.Draft(videos, v)
	r.RoomID = room.ID
	if _, err := u.Enqueue(r); err != nil {
		slog.Error("bilibili: auto upload not queued", "anchor", anchor, "err", err)
	}
}

// ---- helpers -----------------------------------------------------------------------------------

var spaces = regexp.MustCompile(`\s+`)

func collapse(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

func splitTags(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == '\n' }) {
		t = clip(strings.TrimSpace(t), maxTag)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == maxTags {
			break
		}
	}
	return out
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
