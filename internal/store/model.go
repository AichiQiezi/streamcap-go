package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// Room is one monitored live room as persisted in rooms.json.
type Room struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Platform string `json:"platform"` // resolver key
	// Name is the streamer name. CustomName means the user typed it, so auto-detection must not overwrite it.
	Name       string           `json:"name"`
	CustomName bool             `json:"custom_name"`
	Quality    platform.Quality `json:"quality"`
	Format     string           `json:"format"` // TS MP4 FLV MKV MOV / MP3 M4A AAC WAV
	Segment    bool             `json:"segment"`
	SegmentSec int              `json:"segment_sec"`
	BitrateK   int              `json:"bitrate_k"`  // 0 = copy the source stream
	RecordDir  string           `json:"record_dir"` // per-room override of the save root
	Monitor    bool             `json:"monitor"`
	Scheduled  bool             `json:"scheduled"`
	// ScheduleStart is "HH:MM:SS"; ScheduleHours is how long the daily window lasts.
	ScheduleStart string  `json:"schedule_start"`
	ScheduleHours float64 `json:"schedule_hours"`
	Push          bool    `json:"push"`
	NotifyOnly    bool    `json:"notify_only"`
	// AutoUpload submits the session's files to Bilibili once the live has ended and they are saved.
	AutoUpload bool      `json:"auto_upload,omitempty"`
	CreatedAt  time.Time `json:"created_at"`

	// Ident is the streamer's id on the site (platform.StreamInfo.Ident), to refuse adding the same
	// streamer twice through different links. Filled at add time, or by the first successful check.
	Ident string `json:"ident,omitempty"`
}

// SessionJournal records a live session until its files are merged/remuxed, so a crash or kill
// mid-session can be finished on the next start. Part n's files are named Base (n=1) or Base_p<n>.
type SessionJournal struct {
	RoomID    string    `json:"room_id"`
	Anchor    string    `json:"anchor"`
	Dir       string    `json:"dir"`
	Base      string    `json:"base"`
	Format    string    `json:"format"`
	Segmented bool      `json:"segmented"`
	Parts     int       `json:"parts"`
	Start     time.Time `json:"start"`
}

// AudioFormats are formats that record audio only.
var AudioFormats = map[string]bool{"MP3": true, "M4A": true, "AAC": true, "WAV": true}

// VideoFormats lists supported video containers.
var VideoFormats = []string{"TS", "MP4", "FLV", "MKV", "MOV"}

// AudioFormatList lists supported audio formats in display order.
var AudioFormatList = []string{"MP3", "M4A", "AAC", "WAV"}

// InSchedule reports whether t falls in the room's daily window (always true when not scheduled).
func (r Room) InSchedule(t time.Time) bool {
	if !r.Scheduled || r.ScheduleHours <= 0 {
		return true
	}
	start, err := time.ParseInLocation("15:04:05", normalizeClock(r.ScheduleStart), t.Location())
	if err != nil {
		return true
	}
	dur := time.Duration(r.ScheduleHours * float64(time.Hour))
	// The window may have started yesterday and run past midnight.
	for _, dayOffset := range []int{0, -1} {
		d := t.AddDate(0, 0, dayOffset)
		s := time.Date(d.Year(), d.Month(), d.Day(), start.Hour(), start.Minute(), start.Second(), 0, t.Location())
		if !t.Before(s) && t.Before(s.Add(dur)) {
			return true
		}
	}
	return false
}

func normalizeClock(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 5 {
		return s + ":00"
	}
	return s
}

// Settings is the global configuration. It is a loose map (like the Python app's user_settings.json)
// so the UI can round-trip keys; typed accessors below read it.
type Settings map[string]any

// DefaultSettings are the values for a fresh install.
func DefaultSettings() Settings {
	return Settings{
		"live_save_path":              "",
		"record_quality":              "OD",
		"video_format":                "TS",
		"loop_time_seconds":           "120",
		"resume_wait_minutes":         "10",
		"record_danmaku":              true,
		"platform_max_concurrent":     "2",
		"segmented_recording_enabled": false,
		"video_segment_time":          "1800",
		"default_live_source":         "FLV",
		"recording_space_threshold":   "2",
		"merge_sessions":              true,
		"convert_to_mp4":              true,
		"delete_original":             true,
		"folder_name_platform":        true,
		"folder_name_author":          true,
		"folder_name_time":            false,
		"folder_name_title":           false,
		"filename_includes_title":     false,
		"ascii_save_path":             false,
		"enable_proxy":                false,
		"proxy_address":               "",
		"default_platform_with_proxy": "",

		"stream_start_notification_enabled": false,
		"stream_end_notification_enabled":   false,
		"only_notify_no_record":             false,
		"custom_notification_title":         "",
		"custom_stream_start_content":       "",
		"custom_stream_end_content":         "",
		"system_notify_enabled":             true,
		"qqbot_enabled":                     false,
		"qqbot_app_id":                      "",
		"qqbot_secret":                      "",
		"qqbot_openid":                      "",
		"dingtalk_enabled":                  false,
		"dingtalk_webhook_url":              "",
		"dingtalk_at_objects":               "",
		"dingtalk_at_all":                   false,
		"wechat_enabled":                    false,
		"wechat_webhook_url":                "",
		"feishu_enabled":                    false,
		"feishu_webhook_url":                "",
		"bark_enabled":                      false,
		"bark_webhook_url":                  "",
		"bark_interrupt_level":              "active",
		"bark_sound":                        "",
		"ntfy_enabled":                      false,
		"ntfy_server_url":                   "https://ntfy.sh/xxxxx",
		"ntfy_tags":                         "tada",
		"ntfy_email":                        "",
		"ntfy_action_url":                   "",
		"serverchan_enabled":                false,
		"serverchan_sendkey":                "",
		"serverchan_channel":                "9",
		"serverchan_tags":                   "直播通知",
		"telegram_enabled":                  false,
		"telegram_api_token":                "",
		"telegram_chat_id":                  "",
		"email_enabled":                     false,
		"smtp_server":                       "smtp.qq.com",
		"email_username":                    "",
		"email_password":                    "",
		"sender_email":                      "",
		"sender_name":                       "",
		"recipient_email":                   "",

		// Bilibili upload defaults. Templates take {anchor} {title} {date} {time} {platform} {url}.
		"bili_upload_title":        "【直播录像】{anchor} {date} {title}",
		"bili_upload_desc":         "{anchor} 的直播录像\n开播时间：{date} {time}\n直播间：{url}",
		"bili_upload_tags":         "直播录像,{anchor}",
		"bili_upload_tid":          "21",
		"bili_upload_copyright":    "2",
		"bili_upload_min_minutes":  "10",
		"bili_upload_auto_private": true, // after-live submissions are 仅自己可见
	}
}

// Bool reads a boolean setting (accepts JSON bools and "true"/"false" strings).
func (s Settings) Bool(key string) bool {
	switch v := s[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	default:
		return false
	}
}

// String reads a string setting.
func (s Settings) String(key string) string {
	switch v := s[key].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// Float reads a numeric setting stored either as a number or a numeric string.
func (s Settings) Float(key string, def float64) float64 {
	switch v := s[key].(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

// Int reads an integer setting.
func (s Settings) Int(key string, def int) int {
	return int(s.Float(key, float64(def)))
}

// Clone copies the map (settings handed to other goroutines must not alias the stored map).
func (s Settings) Clone() Settings {
	out := make(Settings, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}
