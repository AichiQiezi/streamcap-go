package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// QQ official bot (q.qq.com): a proactive C2C message to each listed user openid. The openid is
// per bot, and only exists for users who have messaged the bot at least once.
// Same calls as qqbot-notify/notify.py.
var qqAPI = "https://api.bot.qq.com" // var for tests

var qqTokens = struct {
	sync.Mutex
	m map[string]qqToken // key: appID + secret, so a changed secret fetches a new token
}{m: map[string]qqToken{}}

type qqToken struct {
	token   string
	expires time.Time
}

func sendQQ(x *sender, m message) error {
	appID := strings.TrimSpace(x.s.String("qqbot_app_id"))
	secret := strings.TrimSpace(x.s.String("qqbot_secret"))
	if appID == "" || secret == "" || strings.TrimSpace(x.s.String("qqbot_openid")) == "" {
		return fmt.Errorf("AppID、AppSecret、接收人 OpenID 都需要填写")
	}
	text := m.Body
	if m.URL != "" && !strings.Contains(text, m.URL) {
		text += "\n" + m.URL
	}
	return eachURL(x.s.String("qqbot_openid"), func(openid string) error {
		err := qqPost(x.ctx, x.client, appID, secret, openid, text)
		if err == errQQAuth { // token revoked or expired early: fetch a fresh one once
			qqForget(appID, secret)
			err = qqPost(x.ctx, x.client, appID, secret, openid, text)
		}
		return err
	})
}

var errQQAuth = errors.New("QQ 机器人鉴权失败（access_token 无效）")

func qqPost(ctx context.Context, c *http.Client, appID, secret, openid, text string) error {
	token, err := qqAccessToken(ctx, c, appID, secret)
	if err != nil {
		return err
	}
	// No msg_id: a proactive message, not a reply.
	raw, _ := json.Marshal(map[string]any{"content": text, "msg_type": 0})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qqAPI+"/v2/users/"+openid+"/messages", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode == http.StatusUnauthorized {
		return errQQAuth
	}
	// Errors come back as {"code": …, "message": …} or {"err_code": …}, sometimes with HTTP 200.
	if resp.StatusCode >= 300 || num(body["code"]) > 0 || num(body["err_code"]) > 0 {
		if msg, _ := body["message"].(string); msg != "" {
			return fmt.Errorf("HTTP %d：%s（code %v）", resp.StatusCode, msg, body["code"])
		}
		return fmt.Errorf("HTTP %d: %v", resp.StatusCode, body)
	}
	return nil
}

func qqAccessToken(ctx context.Context, c *http.Client, appID, secret string) (string, error) {
	key := appID + "\x00" + secret
	qqTokens.Lock()
	t, ok := qqTokens.m[key]
	qqTokens.Unlock()
	if ok && time.Until(t.expires) > 2*time.Minute {
		return t.token, nil
	}

	raw, _ := json.Marshal(map[string]string{"appId": appID, "clientSecret": secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qqAPI+"/app/getAppAccessToken", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"` // a string in practice, a number in the docs
		Message     string          `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.AccessToken == "" { // failures still answer HTTP 200
		if body.Message != "" {
			return "", fmt.Errorf("获取 access_token 失败：%s（检查 AppID / AppSecret）", body.Message)
		}
		return "", fmt.Errorf("获取 access_token 失败：HTTP %d（检查 AppID / AppSecret）", resp.StatusCode)
	}
	secs := 7200
	var n json.Number
	if json.Unmarshal(bytes.Trim(body.ExpiresIn, `"`), &n) == nil {
		if v, err := n.Int64(); err == nil && v > 0 {
			secs = int(v)
		}
	}
	qqTokens.Lock()
	qqTokens.m[key] = qqToken{token: body.AccessToken, expires: time.Now().Add(time.Duration(secs) * time.Second)}
	qqTokens.Unlock()
	return body.AccessToken, nil
}

func qqForget(appID, secret string) {
	qqTokens.Lock()
	delete(qqTokens.m, appID+"\x00"+secret)
	qqTokens.Unlock()
}
