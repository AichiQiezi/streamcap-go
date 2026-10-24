// Package upload submits recordings to Bilibili (投稿): TV QR login, chunked upload to the UPOS
// storage, and the app submit API. The request formats follow biliup-rs, which submits through
// the app API because the web one demands captchas.
package upload

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/store"
)

const (
	// BiliTV app key: the TV QR login and the app submit API are signed with it.
	tvAppKey = "4409e2ce8ffd12b8"
	tvAppSec = "59b43e04ad6965f34319062b478f83dd"

	webUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	appUA = "Mozilla/5.0 BiliDroid/7.80.0 (bbcallen@gmail.com) os/android model/MI 6 mobi_app/android build/7800300 channel/bili innerVer/7800310 osVer/13 network/2"

	// The upload line used when no probe is made (biliup-rs' fallback).
	uposLine = "probe_version=20221109&upcdn=bda2&zone=cs"

	maxBody = 4 << 20
)

// Client talks to Bilibili. The zero value is not usable; use NewClient.
type Client struct {
	http *http.Client
	// Hosts, overridable in tests.
	passport, member, api string
}

// NewClient returns a client for the real Bilibili hosts.
func NewClient() *Client {
	return &Client{
		http:     &http.Client{Timeout: 5 * time.Minute},
		passport: "https://passport.bilibili.com",
		member:   "https://member.bilibili.com",
		api:      "https://api.bilibili.com",
	}
}

// APIError is a non-zero "code" from a Bilibili API.
type APIError struct {
	API     string
	Code    int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s：错误码 %d", e.API, e.Code)
	}
	return fmt.Sprintf("%s：%s（%d）", e.API, e.Message, e.Code)
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// sign adds ts, appkey and sign (md5 of the sorted query + app secret), as the app APIs expect.
func sign(q url.Values) url.Values {
	q.Set("appkey", tvAppKey)
	q.Set("ts", strconv.FormatInt(time.Now().Unix(), 10))
	q.Del("sign")
	sum := md5.Sum([]byte(q.Encode() + tvAppSec)) // Encode sorts by key
	q.Set("sign", hex.EncodeToString(sum[:]))
	return q
}

// do sends req and decodes the {code, message, data} envelope into out (when non-nil).
// A non-zero code is returned as *APIError, along with the raw data.
func (c *Client) do(req *http.Request, name string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s：%w", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s：读取响应失败：%w", name, err)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%s：HTTP %d：%s", name, resp.StatusCode, snippet(body))
	}
	if env.Code != 0 {
		return &APIError{API: name, Code: env.Code, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s：响应格式变了：%w", name, err)
		}
	}
	return nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func (c *Client) postForm(ctx context.Context, u, name string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", appUA)
	return c.do(req, name, out)
}

// webRequest builds a request carrying the account's web cookies.
func webRequest(ctx context.Context, method, u string, body io.Reader, acct *store.BiliAccount) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if acct != nil {
		req.Header.Set("Cookie", cookieHeader(acct.Cookies))
	}
	return req, nil
}

func cookieHeader(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// ---- login -------------------------------------------------------------------------------------

// QRCode is a pending TV QR login: URL is what the QR code encodes.
type QRCode struct {
	URL      string `json:"url"`
	AuthCode string `json:"auth_code"`
}

// LoginQR starts a TV QR login.
func (c *Client) LoginQR(ctx context.Context) (QRCode, error) {
	var out QRCode
	err := c.postForm(ctx, c.passport+"/x/passport-tv-login/qrcode/auth_code", "获取登录二维码",
		sign(url.Values{"local_id": {"0"}}), &out)
	if err == nil && (out.URL == "" || out.AuthCode == "") {
		err = errors.New("获取登录二维码：响应里没有二维码")
	}
	return out, err
}

// LoginStatus is the outcome of one poll of a QR login.
type LoginStatus string

const (
	LoginWaiting LoginStatus = "waiting" // not scanned yet
	LoginScanned LoginStatus = "scanned" // scanned, waiting for the confirm tap
	LoginExpired LoginStatus = "expired"
	LoginDone    LoginStatus = "done"
)

// tokenData is the login / refresh payload: the OAuth token and the matching web cookies.
type tokenData struct {
	Mid          int64  `json:"mid"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenInfo    *struct {
		Mid          int64  `json:"mid"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	} `json:"token_info"`
	CookieInfo struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	} `json:"cookie_info"`
}

func (t tokenData) account() (*store.BiliAccount, error) {
	if t.TokenInfo != nil {
		t.Mid, t.AccessToken, t.RefreshToken, t.ExpiresIn = t.TokenInfo.Mid, t.TokenInfo.AccessToken, t.TokenInfo.RefreshToken, t.TokenInfo.ExpiresIn
	}
	if t.AccessToken == "" {
		return nil, errors.New("登录结果里没有 access_token")
	}
	cookies := map[string]string{}
	for _, ck := range t.CookieInfo.Cookies {
		cookies[ck.Name] = ck.Value
	}
	if cookies["SESSDATA"] == "" || cookies["bili_jct"] == "" {
		return nil, errors.New("登录结果里没有 SESSDATA / bili_jct Cookie")
	}
	return &store.BiliAccount{
		Mid: t.Mid, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken,
		ExpiresAt: time.Now().Add(time.Duration(t.ExpiresIn) * time.Second), Cookies: cookies, LoginAt: time.Now(),
	}, nil
}

// PollLogin checks a QR login once. The account is returned with LoginDone.
func (c *Client) PollLogin(ctx context.Context, authCode string) (LoginStatus, *store.BiliAccount, error) {
	var data tokenData
	err := c.postForm(ctx, c.passport+"/x/passport-tv-login/qrcode/poll", "登录",
		sign(url.Values{"auth_code": {authCode}, "local_id": {"0"}}), &data)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 86039:
			return LoginWaiting, nil, nil
		case 86090:
			return LoginScanned, nil, nil
		case 86038:
			return LoginExpired, nil, nil
		}
	}
	if err != nil {
		return "", nil, err
	}
	acct, err := data.account()
	if err != nil {
		return "", nil, err
	}
	return LoginDone, acct, nil
}

// Refresh renews the access token (it lasts about half a year) and returns the updated account.
func (c *Client) Refresh(ctx context.Context, acct *store.BiliAccount) (*store.BiliAccount, error) {
	var data tokenData
	err := c.postForm(ctx, c.passport+"/x/passport-login/oauth2/refresh_token", "刷新登录",
		sign(url.Values{"access_key": {acct.AccessToken}, "refresh_token": {acct.RefreshToken}, "actionKey": {"appkey"}}), &data)
	if err != nil {
		return nil, err
	}
	fresh, err := data.account()
	if err != nil {
		return nil, err
	}
	fresh.Name, fresh.Face, fresh.LoginAt = acct.Name, acct.Face, acct.LoginAt
	if fresh.Mid == 0 {
		fresh.Mid = acct.Mid
	}
	return fresh, nil
}

// Profile is the logged-in user, from the web nav API.
type Profile struct {
	Mid  int64  `json:"mid"`
	Name string `json:"uname"`
	Face string `json:"face"`
}

// Me returns who the account's cookies log in as; an error means they no longer work.
func (c *Client) Me(ctx context.Context, acct *store.BiliAccount) (Profile, error) {
	req, err := webRequest(ctx, http.MethodGet, c.api+"/x/web-interface/nav", nil, acct)
	if err != nil {
		return Profile{}, err
	}
	var out struct {
		Profile
		IsLogin bool `json:"isLogin"`
	}
	// nav answers -101 (not logged in) with the data still present, so look at the code only.
	if err := c.do(req, "读取账号信息", &out); err != nil {
		return Profile{}, err
	}
	if !out.IsLogin {
		return Profile{}, errors.New("登录已失效")
	}
	return out.Profile, nil
}

// ---- partitions --------------------------------------------------------------------------------

// Partition is a 分区 that videos are submitted to (tid). Top-level ones only group the children.
type Partition struct {
	ID       int         `json:"id"`
	Name     string      `json:"name"`
	Desc     string      `json:"desc,omitempty"`
	Children []Partition `json:"children,omitempty"`
}

// Partitions lists the 分区 offered on the submit page.
func (c *Client) Partitions(ctx context.Context, acct *store.BiliAccount) ([]Partition, error) {
	req, err := webRequest(ctx, http.MethodGet, c.member+"/x/vupre/web/archive/pre?lang=cn", nil, acct)
	if err != nil {
		return nil, err
	}
	var out struct {
		TypeList []Partition `json:"typelist"`
	}
	if err := c.do(req, "读取分区", &out); err != nil {
		return nil, err
	}
	if len(out.TypeList) == 0 {
		return nil, errors.New("读取分区：列表为空")
	}
	return out.TypeList, nil
}

// ---- upload ------------------------------------------------------------------------------------

type preupload struct {
	OK        int    `json:"OK"`
	Message   string `json:"message"`
	Auth      string `json:"auth"`
	BizID     int64  `json:"biz_id"`
	ChunkSize int64  `json:"chunk_size"`
	Endpoint  string `json:"endpoint"`
	UposURI   string `json:"upos_uri"`
}

// rawJSON calls a UPOS-style endpoint that answers bare JSON (no code envelope) into out.
func (c *Client) rawJSON(req *http.Request, name string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s：%w", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s：读取响应失败：%w", name, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s：HTTP %d：%s", name, resp.StatusCode, snippet(body))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s：响应不是 JSON：%s", name, snippet(body))
	}
	return nil
}

// UploadVideo uploads one file and returns the name the submit API refers to it by. progress is
// called with the number of bytes just sent (from several goroutines).
func (c *Client) UploadVideo(ctx context.Context, acct *store.BiliAccount, file string, progress func(int64)) (string, error) {
	st, err := os.Stat(file)
	if err != nil {
		return "", err
	}
	size := st.Size()
	name := filepath.Base(file)

	q, _ := url.ParseQuery(uposLine)
	for k, v := range map[string]string{"r": "upos", "profile": "ugcupos/bup", "ssl": "0", "version": "2.11.0",
		"build": "2110000", "name": name, "size": strconv.FormatInt(size, 10)} {
		q.Set(k, v)
	}
	req, err := webRequest(ctx, http.MethodGet, c.member+"/preupload?"+q.Encode(), nil, acct)
	if err != nil {
		return "", err
	}
	var pre preupload
	if err := c.rawJSON(req, "申请上传", &pre); err != nil {
		return "", err
	}
	if pre.OK != 1 || pre.Endpoint == "" || pre.UposURI == "" || pre.ChunkSize <= 0 {
		return "", fmt.Errorf("申请上传被拒绝：%s", firstNonEmpty(pre.Message, "未返回上传地址，可能是登录失效"))
	}
	base := "https:" + pre.Endpoint + "/" + strings.TrimPrefix(pre.UposURI, "upos://")
	auth := pre.Auth

	// Start the multipart upload.
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"?uploads&output=json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Upos-Auth", auth)
	req.Header.Set("User-Agent", webUA)
	var init struct {
		OK       int    `json:"OK"`
		UploadID string `json:"upload_id"`
	}
	if err := c.rawJSON(req, "开始上传", &init); err != nil {
		return "", err
	}
	if init.UploadID == "" {
		return "", errors.New("开始上传：没有拿到 upload_id")
	}

	chunks := int((size + pre.ChunkSize - 1) / pre.ChunkSize)
	if err := c.putChunks(ctx, file, base, auth, init.UploadID, size, pre.ChunkSize, chunks, progress); err != nil {
		return "", err
	}

	// Complete. The part eTags are not checked by the server; biliup sends a fixed "etag".
	type part struct {
		PartNumber int    `json:"partNumber"`
		ETag       string `json:"eTag"`
	}
	parts := make([]part, chunks)
	for i := range parts {
		parts[i] = part{PartNumber: i + 1, ETag: "etag"}
	}
	body, _ := json.Marshal(map[string]any{"parts": parts})
	cq := url.Values{"output": {"json"}, "name": {name}, "profile": {"ugcupos/bup"}, "uploadId": {init.UploadID},
		"biz_id": {strconv.FormatInt(pre.BizID, 10)}}
	var done struct {
		OK      int    `json:"OK"`
		Message string `json:"message"`
	}
	for attempt := 0; ; attempt++ {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"?"+cq.Encode(), bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("X-Upos-Auth", auth)
		req.Header.Set("User-Agent", webUA)
		req.Header.Set("Content-Type", "application/json")
		err = c.rawJSON(req, "完成上传", &done)
		if err == nil && done.OK == 1 {
			break
		}
		if err == nil {
			err = fmt.Errorf("完成上传：%s", firstNonEmpty(done.Message, "服务器未确认"))
		}
		if attempt >= 3 || !sleep(ctx, time.Duration(attempt+1)*3*time.Second) {
			return "", err
		}
	}
	stem := path.Base(pre.UposURI)
	return strings.TrimSuffix(stem, path.Ext(stem)), nil
}

const chunkWorkers = 3

func (c *Client) putChunks(ctx context.Context, file, base, auth, uploadID string, size, chunkSize int64, chunks int, progress func(int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var (
		wg       sync.WaitGroup
		firstErr atomic.Value
	)
	for w := 0; w < chunkWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := os.Open(file)
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				cancel()
				return
			}
			defer f.Close()
			buf := make([]byte, chunkSize)
			for i := range jobs {
				start := int64(i) * chunkSize
				n, err := f.ReadAt(buf, start)
				if err != nil && !errors.Is(err, io.EOF) {
					firstErr.CompareAndSwap(nil, err)
					cancel()
					return
				}
				if err := c.putChunk(ctx, base, auth, uploadID, i, chunks, start, buf[:n], size); err != nil {
					firstErr.CompareAndSwap(nil, err)
					cancel()
					return
				}
				progress(int64(n))
			}
		}()
	}
feed:
	for i := 0; i < chunks; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err, ok := firstErr.Load().(error); ok {
		return err
	}
	return ctx.Err()
}

// putChunk sends one part, retrying network errors and 5xx a few times.
func (c *Client) putChunk(ctx context.Context, base, auth, uploadID string, i, chunks int, start int64, data []byte, total int64) error {
	q := url.Values{
		"partNumber": {strconv.Itoa(i + 1)}, "uploadId": {uploadID}, "chunk": {strconv.Itoa(i)},
		"chunks": {strconv.Itoa(chunks)}, "size": {strconv.Itoa(len(data))}, "start": {strconv.FormatInt(start, 10)},
		"end": {strconv.FormatInt(start+int64(len(data)), 10)}, "total": {strconv.FormatInt(total, 10)},
	}
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 && !sleep(ctx, time.Duration(attempt*attempt)*2*time.Second) {
			return ctx.Err()
		}
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPut, base+"?"+q.Encode(), bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.ContentLength = int64(len(data))
		req.Header.Set("X-Upos-Auth", auth)
		req.Header.Set("User-Agent", webUA)
		req.Header.Set("Content-Type", "application/octet-stream")
		var resp *http.Response
		resp, err = c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		err = fmt.Errorf("上传第 %d/%d 块：HTTP %d：%s", i+1, chunks, resp.StatusCode, snippet(body))
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return err
		}
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// UploadCover uploads a JPEG cover and returns its URL for the submission.
func (c *Client) UploadCover(ctx context.Context, acct *store.BiliAccount, jpeg []byte) (string, error) {
	form := url.Values{
		"cover": {"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)},
		"csrf":  {acct.Cookies["bili_jct"]},
	}
	req, err := webRequest(ctx, http.MethodPost, c.member+"/x/vu/web/cover/up", strings.NewReader(form.Encode()), acct)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		URL string `json:"url"`
	}
	if err := c.do(req, "上传封面", &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// ---- submit ------------------------------------------------------------------------------------

// Video is one part of a submission.
type Video struct {
	Title    string `json:"title"`
	Filename string `json:"filename"` // from UploadVideo
	Desc     string `json:"desc"`
}

// Archive is the submission (稿件) body of the app submit API.
type Archive struct {
	Copyright    int     `json:"copyright"`
	Source       string  `json:"source"`
	Tid          int     `json:"tid"`
	Cover        string  `json:"cover"`
	Title        string  `json:"title"`
	DescFormatID int     `json:"desc_format_id"`
	Desc         string  `json:"desc"`
	Dynamic      string  `json:"dynamic"`
	Subtitle     subOpts `json:"subtitle"`
	Tag          string  `json:"tag"`
	Videos       []Video `json:"videos"`
	NoReprint    int     `json:"no_reprint"`
	OpenElec     int     `json:"open_elec"`
	IsOnlySelf   int     `json:"is_only_self"` // 1: 仅自己可见
}

type subOpts struct {
	Open int    `json:"open"`
	Lan  string `json:"lan"`
}

// Submit publishes the archive and returns its BV id.
func (c *Client) Submit(ctx context.Context, acct *store.BiliAccount, a Archive) (string, error) {
	q := sign(url.Values{
		"access_key": {acct.AccessToken}, "build": {"7800300"}, "c_locale": {"zh-Hans_CN"}, "s_locale": {"zh-Hans_CN"},
		"channel": {"bili"}, "disable_rcmd": {"0"}, "mobi_app": {"android"}, "platform": {"android"},
		"statistics": {`{"appId":1,"platform":3,"version":"7.80.0","abtest":""}`},
	})
	body, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.member+"/x/vu/app/add?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", appUA)
	var out struct {
		Aid  int64  `json:"aid"`
		Bvid string `json:"bvid"`
	}
	if err := c.do(req, "投稿", &out); err != nil {
		return "", err
	}
	if out.Bvid == "" && out.Aid != 0 {
		return "av" + strconv.FormatInt(out.Aid, 10), nil
	}
	return out.Bvid, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
