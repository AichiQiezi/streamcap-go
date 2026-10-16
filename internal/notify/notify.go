// Package notify pushes live start/end messages to the channels enabled in settings.
// Request formats match the Python StreamCap so existing webhook setups keep working.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"regexp"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
	"github.com/AichiQiezi/streamcap-go/internal/store"
)

// Event is what happened.
type Event struct {
	Start  bool // true = went live, false = ended
	Anchor string
	Title  string
	URL    string
	// Recorded is how long this session recorded (end events of recorded rooms).
	Recorded time.Duration
	// Live is roughly how long the stream was live (end events of notify-only rooms).
	Live time.Duration
}

// message is what every channel gets: a title, the rendered body and the live room's link.
type message struct {
	Title, Body, URL string
}

// sender carries the per-send state the channels share.
type sender struct {
	ctx     context.Context
	s       store.Settings
	client  *http.Client
	proxied *http.Client // for channels that are usually blocked without the user's proxy
}

type channel struct {
	id     string // also the settings prefix: <id>_enabled etc.
	name   string
	enable string
	send   func(x *sender, m message) error
}

// SystemNotifier shows a desktop notification. Set by the app when it runs with a desktop (not
// -headless); nil otherwise, and then the system channel reports why it can't deliver.
var SystemNotifier func(title, body, url string) error

var channels = []channel{
	{"system", "系统通知", "system_notify_enabled", func(x *sender, m message) error {
		if SystemNotifier == nil {
			return fmt.Errorf("当前运行方式没有桌面（-headless），无法弹出系统通知")
		}
		return SystemNotifier(m.Title, m.Body, m.URL)
	}},
	{"qqbot", "QQ 机器人", "qqbot_enabled", sendQQ},
	{"dingtalk", "钉钉", "dingtalk_enabled", func(x *sender, m message) error {
		return eachURL(x.s.String("dingtalk_webhook_url"), func(u string) error {
			at := map[string]any{"atMobiles": splitList(x.s.String("dingtalk_at_objects")), "isAtAll": x.s.Bool("dingtalk_at_all")}
			return postJSON(x.ctx, x.client, u, map[string]any{"msgtype": "text", "text": map[string]any{"content": m.Body}, "at": at},
				func(r map[string]any) bool { return num(r["errcode"]) == 0 })
		})
	}},
	{"wechat", "微信", "wechat_enabled", func(x *sender, m message) error {
		return eachURL(x.s.String("wechat_webhook_url"), func(u string) error {
			return postJSON(x.ctx, x.client, u, map[string]any{"title": m.Title, "content": m.Body},
				func(r map[string]any) bool { return num(r["code"]) == 200 })
		})
	}},
	{"feishu", "飞书", "feishu_enabled", func(x *sender, m message) error {
		return eachURL(x.s.String("feishu_webhook_url"), func(u string) error {
			return postJSON(x.ctx, x.client, u, map[string]any{"msg_type": "text", "content": map[string]any{"text": m.Body}},
				func(r map[string]any) bool { return r["msg"] == "success" || num(r["code"]) == 0 })
		})
	}},
	{"bark", "Bark", "bark_enabled", func(x *sender, m message) error {
		return eachURL(x.s.String("bark_webhook_url"), func(u string) error {
			body := map[string]any{"title": m.Title, "body": m.Body, "level": x.s.String("bark_interrupt_level"),
				"badge": 1, "autoCopy": 1, "sound": x.s.String("bark_sound"), "isArchive": 1, "url": m.URL}
			return postJSON(x.ctx, x.client, u, body, func(r map[string]any) bool { return num(r["code"]) == 200 })
		})
	}},
	{"ntfy", "ntfy", "ntfy_enabled", func(x *sender, m message) error {
		return eachURL(x.s.String("ntfy_server_url"), func(u string) error {
			server, topic, ok := cutLast(u, "/")
			if !ok {
				return fmt.Errorf("ntfy 地址需要形如 https://ntfy.sh/topic")
			}
			body := map[string]any{"topic": topic, "title": m.Title, "message": m.Body, "tags": splitList(x.s.String("ntfy_tags")), "priority": 3}
			if a := x.s.String("ntfy_action_url"); a != "" {
				body["actions"] = []map[string]string{{"action": "view", "label": "view live", "url": a}}
			}
			if e := x.s.String("ntfy_email"); e != "" {
				body["email"] = e
			}
			return postJSON(x.ctx, x.client, server, body, func(r map[string]any) bool { return r["id"] != nil })
		})
	}},
	{"serverchan", "Server酱", "serverchan_enabled", func(x *sender, m message) error {
		return eachURL(x.s.String("serverchan_sendkey"), func(key string) error {
			u := "https://sctapi.ftqq.com/" + key + ".send"
			if strings.HasPrefix(key, "sctp") {
				mm := regexp.MustCompile(`^sctp(\d+)t`).FindStringSubmatch(key)
				if mm == nil {
					return fmt.Errorf("无效的 sendkey")
				}
				u = fmt.Sprintf("https://%s.push.ft07.com/send/%s.send", mm[1], key)
			}
			body := map[string]any{"title": m.Title, "desp": m.Body, "channel": x.s.Int("serverchan_channel", 9), "tags": x.s.String("serverchan_tags")}
			return postJSON(x.ctx, x.client, u, body, func(r map[string]any) bool { return num(r["code"]) == 0 })
		})
	}},
	{"telegram", "Telegram", "telegram_enabled", func(x *sender, m message) error {
		u := "https://api.telegram.org/bot" + x.s.String("telegram_api_token") + "/sendMessage"
		return postJSON(x.ctx, x.proxied, u, map[string]any{"chat_id": x.s.String("telegram_chat_id"), "text": m.Body},
			func(r map[string]any) bool { return r["ok"] == true })
	}},
	{"email", "邮件", "email_enabled", func(x *sender, m message) error { return sendMail(x.s, m.Title, m.Body) }},
}

func newSender(ctx context.Context, s store.Settings) *sender {
	proxy := ""
	if s.Bool("enable_proxy") {
		proxy = platform.ProxyURL(s.String("proxy_address"))
	}
	return &sender{ctx: ctx, s: s, client: &http.Client{Timeout: 15 * time.Second}, proxied: platform.HTTPClient(proxy)}
}

func title(s store.Settings) string {
	if t := s.String("custom_notification_title"); t != "" {
		return t
	}
	return "直播间状态更新通知"
}

// Send formats ev with the user's templates and pushes it to every enabled channel. Whether a room
// alerts at all is the caller's decision (the room's own switch); here only "下播时也提醒" gates.
// Failures are logged, never returned: a broken webhook must not affect recording.
func Send(ctx context.Context, s store.Settings, ev Event) {
	if !ev.Start && !s.Bool("stream_end_notification_enabled") {
		return
	}
	x := newSender(ctx, s)
	m := message{Title: title(s), Body: render(s, ev), URL: ev.URL}
	for _, c := range channels {
		if !s.Bool(c.enable) {
			continue
		}
		if err := c.send(x, m); err != nil {
			slog.Warn("push failed", "channel", c.name, "err", err)
		} else {
			slog.Info("push sent", "channel", c.name)
		}
	}
}

// Test sends a sample message through one channel, enabled or not, and returns its error so the
// settings page can show it.
func Test(ctx context.Context, s store.Settings, id string) error {
	for _, c := range channels {
		if c.id == id {
			now := time.Now().Format("2006-01-02 15:04:05")
			body := fmt.Sprintf("这是一条来自 StreamCap 的测试消息，收到说明「%s」已配置好。\n时间：%s", c.name, now)
			return c.send(newSender(ctx, s), message{Title: "StreamCap 测试通知", Body: body})
		}
	}
	return fmt.Errorf("未知的推送渠道：%s", id)
}

func render(s store.Settings, ev Event) string {
	now := time.Now().Format("2006-01-02 15:04:05")
	tpl := s.String("custom_stream_start_content")
	def := fmt.Sprintf("%s 开播了\n标题：%s\n%s\n时间：%s", ev.Anchor, ev.Title, ev.URL, now)
	if !ev.Start {
		tpl = s.String("custom_stream_end_content")
		switch {
		case ev.Recorded > 0:
			def = fmt.Sprintf("%s 下播了\n本场录制：%s\n时间：%s", ev.Anchor, ev.Recorded.Round(time.Second), now)
		case ev.Live > 0:
			def = fmt.Sprintf("%s 下播了\n直播时长约：%s\n时间：%s", ev.Anchor, max(ev.Live.Round(time.Minute), time.Minute), now)
		default:
			def = fmt.Sprintf("%s 下播了\n时间：%s", ev.Anchor, now)
		}
	}
	if strings.TrimSpace(tpl) == "" {
		return def
	}
	r := strings.NewReplacer("{anchor}", ev.Anchor, "{title}", ev.Title, "{url}", ev.URL, "{time}", now,
		"[直播间名称]", ev.Anchor, "[时间]", now)
	return r.Replace(tpl)
}

func postJSON(ctx context.Context, c *http.Client, url string, body any, ok func(map[string]any) bool) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	if resp.StatusCode >= 300 || !ok(parsed) {
		return fmt.Errorf("HTTP %d: %v", resp.StatusCode, parsed)
	}
	return nil
}

func sendMail(s store.Settings, title, content string) error {
	host := s.String("smtp_server")
	to := splitList(s.String("recipient_email"))
	if host == "" || len(to) == 0 {
		return fmt.Errorf("SMTP 服务器或收件人未配置")
	}
	addr := host
	if !strings.Contains(host, ":") {
		addr = net.JoinHostPort(host, "465")
	}
	from := s.String("sender_email")
	name := base64.StdEncoding.EncodeToString([]byte(s.String("sender_name")))
	subj := base64.StdEncoding.EncodeToString([]byte(title))
	msg := fmt.Sprintf("From: =?UTF-8?B?%s?= <%s>\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		name, from, strings.Join(to, ", "), subj, content)
	h, _, _ := net.SplitHostPort(addr)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: h})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, h)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Auth(smtp.PlainAuth("", s.String("email_username"), s.String("email_password"), h)); err != nil {
		return err
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func eachURL(list string, fn func(string) error) error {
	var errs []string
	for _, u := range splitList(list) {
		if err := fn(u); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(s, "，", ","), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func num(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return -1
}
