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
	// Recorded is how long this session recorded (end events only).
	Recorded time.Duration
}

// Send formats ev with the user's templates and pushes it to every enabled channel. Failures are
// logged, never returned: a broken webhook must not affect recording.
func Send(ctx context.Context, s store.Settings, ev Event) {
	if ev.Start && !s.Bool("stream_start_notification_enabled") {
		return
	}
	if !ev.Start && !s.Bool("stream_end_notification_enabled") {
		return
	}
	title := s.String("custom_notification_title")
	if title == "" {
		title = "直播间状态更新通知"
	}
	content := render(s, ev)
	proxy := ""
	if s.Bool("enable_proxy") {
		proxy = platform.ProxyURL(s.String("proxy_address"))
	}
	client := &http.Client{Timeout: 15 * time.Second}
	proxied := platform.HTTPClient(proxy)

	type job struct {
		name string
		fn   func() error
	}
	var jobs []job
	if s.Bool("dingtalk_enabled") {
		jobs = append(jobs, job{"钉钉", func() error {
			return eachURL(s.String("dingtalk_webhook_url"), func(u string) error {
				at := map[string]any{"atMobiles": splitList(s.String("dingtalk_at_objects")), "isAtAll": s.Bool("dingtalk_at_all")}
				return postJSON(ctx, client, u, map[string]any{"msgtype": "text", "text": map[string]any{"content": content}, "at": at},
					func(r map[string]any) bool { return num(r["errcode"]) == 0 })
			})
		}})
	}
	if s.Bool("wechat_enabled") {
		jobs = append(jobs, job{"微信", func() error {
			return eachURL(s.String("wechat_webhook_url"), func(u string) error {
				return postJSON(ctx, client, u, map[string]any{"title": title, "content": content},
					func(r map[string]any) bool { return num(r["code"]) == 200 })
			})
		}})
	}
	if s.Bool("feishu_enabled") {
		jobs = append(jobs, job{"飞书", func() error {
			return eachURL(s.String("feishu_webhook_url"), func(u string) error {
				return postJSON(ctx, client, u, map[string]any{"msg_type": "text", "content": map[string]any{"text": content}},
					func(r map[string]any) bool { return r["msg"] == "success" || num(r["code"]) == 0 })
			})
		}})
	}
	if s.Bool("bark_enabled") {
		jobs = append(jobs, job{"Bark", func() error {
			return eachURL(s.String("bark_webhook_url"), func(u string) error {
				body := map[string]any{"title": title, "body": content, "level": s.String("bark_interrupt_level"),
					"badge": 1, "autoCopy": 1, "sound": s.String("bark_sound"), "isArchive": 1, "url": ev.URL}
				return postJSON(ctx, client, u, body, func(r map[string]any) bool { return num(r["code"]) == 200 })
			})
		}})
	}
	if s.Bool("ntfy_enabled") {
		jobs = append(jobs, job{"ntfy", func() error {
			return eachURL(s.String("ntfy_server_url"), func(u string) error {
				server, topic, ok := cutLast(u, "/")
				if !ok {
					return fmt.Errorf("ntfy 地址需要形如 https://ntfy.sh/topic")
				}
				body := map[string]any{"topic": topic, "title": title, "message": content, "tags": splitList(s.String("ntfy_tags")), "priority": 3}
				if a := s.String("ntfy_action_url"); a != "" {
					body["actions"] = []map[string]string{{"action": "view", "label": "view live", "url": a}}
				}
				if e := s.String("ntfy_email"); e != "" {
					body["email"] = e
				}
				return postJSON(ctx, client, server, body, func(r map[string]any) bool { return r["id"] != nil })
			})
		}})
	}
	if s.Bool("serverchan_enabled") {
		jobs = append(jobs, job{"Server酱", func() error {
			return eachURL(s.String("serverchan_sendkey"), func(key string) error {
				u := "https://sctapi.ftqq.com/" + key + ".send"
				if strings.HasPrefix(key, "sctp") {
					m := regexp.MustCompile(`^sctp(\d+)t`).FindStringSubmatch(key)
					if m == nil {
						return fmt.Errorf("无效的 sendkey")
					}
					u = fmt.Sprintf("https://%s.push.ft07.com/send/%s.send", m[1], key)
				}
				body := map[string]any{"title": title, "desp": content, "channel": s.Int("serverchan_channel", 9), "tags": s.String("serverchan_tags")}
				return postJSON(ctx, client, u, body, func(r map[string]any) bool { return num(r["code"]) == 0 })
			})
		}})
	}
	if s.Bool("telegram_enabled") {
		jobs = append(jobs, job{"Telegram", func() error {
			u := "https://api.telegram.org/bot" + s.String("telegram_api_token") + "/sendMessage"
			return postJSON(ctx, proxied, u, map[string]any{"chat_id": s.String("telegram_chat_id"), "text": content},
				func(r map[string]any) bool { return r["ok"] == true })
		}})
	}
	if s.Bool("email_enabled") {
		jobs = append(jobs, job{"邮件", func() error { return sendMail(s, title, content) }})
	}

	for _, j := range jobs {
		if err := j.fn(); err != nil {
			slog.Warn("push failed", "channel", j.name, "err", err)
		} else {
			slog.Info("push sent", "channel", j.name)
		}
	}
}

func render(s store.Settings, ev Event) string {
	now := time.Now().Format("2006-01-02 15:04:05")
	tpl := s.String("custom_stream_start_content")
	def := fmt.Sprintf("%s 开播了\n标题：%s\n%s\n时间：%s", ev.Anchor, ev.Title, ev.URL, now)
	if !ev.Start {
		tpl = s.String("custom_stream_end_content")
		def = fmt.Sprintf("%s 下播了\n本场录制：%s\n时间：%s", ev.Anchor, ev.Recorded.Round(time.Second), now)
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
