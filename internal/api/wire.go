package api

import (
	"strconv"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/monitor"
	"github.com/AichiQiezi/streamcap-go/internal/platform"
)

// recJSON is the room shape the web UI consumes (kept compatible with the Python control API).
type recJSON struct {
	ID                 string    `json:"id"`
	URL                string    `json:"url"`
	Name               string    `json:"name"`
	Platform           string    `json:"platform"`
	PlatformKey        string    `json:"platform_key"`
	LiveTitle          *string   `json:"live_title"`
	Quality            string    `json:"quality"`
	Format             string    `json:"format"`
	State              string    `json:"state"`
	StatusInfo         *string   `json:"status_info"`
	Monitor            bool      `json:"monitor"`
	IsLive             bool      `json:"is_live"`
	IsRecording        bool      `json:"is_recording"`
	DurationS          int64     `json:"duration_s"`
	StartedAt          *string   `json:"started_at"`
	RecordingDir       *string   `json:"recording_dir"`
	SegmentRecord      bool      `json:"segment_record"`
	SegmentTime        string    `json:"segment_time"`
	VideoBitrate       *int      `json:"video_bitrate"`
	ScheduledRecording bool      `json:"scheduled_recording"`
	ScheduledStartTime *string   `json:"scheduled_start_time"`
	MonitorHours       *string   `json:"monitor_hours"`
	EnabledMessagePush bool      `json:"enabled_message_push"`
	OnlyNotifyNoRecord bool      `json:"only_notify_no_record"`
	FlvDirect          bool      `json:"flv_use_direct_download"`
	CheckedAt          *string   `json:"checked_at"`
	LastError          *string   `json:"last_error"`
	Reconnects         int       `json:"reconnects"`
	Stats              statsJSON `json:"stats"`
}

type statsJSON struct {
	File         *string  `json:"file"`
	SizeBytes    int64    `json:"size_bytes"`
	SpeedBps     int64    `json:"speed_bps"`
	Width        *int     `json:"width"`
	Height       *int     `json:"height"`
	FPS          *float64 `json:"fps"`
	VCodec       *string  `json:"vcodec"`
	ThumbVersion int64    `json:"thumb_version"`
	// Danmaku: comments recorded in this session so far; null when the session records none.
	Danmaku *int `json:"danmaku"`
}

func toJSON(v monitor.View) recJSON {
	r, s := v.Room, v.State
	name := r.Name
	if name == "" {
		name = "直播间"
	}
	platName := r.Platform
	if res := platform.ByKey(r.Platform); res != nil {
		platName = res.Name()
	}
	out := recJSON{
		ID: r.ID, URL: r.URL, Name: name, Platform: platName, PlatformKey: r.Platform,
		Quality: string(r.Quality), Format: r.Format, State: string(s.Phase),
		Monitor: r.Monitor, IsLive: s.IsLive, IsRecording: s.Phase == monitor.PhaseRecording,
		DurationS: int64(s.Duration().Seconds()), SegmentRecord: r.Segment, SegmentTime: strconv.Itoa(r.SegmentSec),
		ScheduledRecording: r.Scheduled, EnabledMessagePush: r.Push, OnlyNotifyNoRecord: r.NotifyOnly,
		Reconnects: s.Reconnects,
		Stats: statsJSON{
			File: strPtr(s.File), SizeBytes: s.Bytes, SpeedBps: int64(s.SpeedBps),
			Width: intPtr(s.Width), Height: intPtr(s.Height), VCodec: strPtr(s.Codec), ThumbVersion: s.ThumbVersion,
			Danmaku: danmakuCount(s),
		},
	}
	if s.FPS > 0 {
		f := s.FPS
		out.Stats.FPS = &f
	}
	out.LiveTitle = strPtr(s.LiveTitle)
	out.StatusInfo = strPtr(s.Detail)
	out.LastError = strPtr(s.LastError)
	out.RecordingDir = strPtr(firstNonEmpty(s.Dir, r.RecordDir))
	if r.BitrateK > 0 {
		b := r.BitrateK
		out.VideoBitrate = &b
	}
	if r.Scheduled {
		out.ScheduledStartTime = strPtr(r.ScheduleStart)
		out.MonitorHours = strPtr(strconv.FormatFloat(r.ScheduleHours, 'f', -1, 64))
	}
	if !s.SessionStart.IsZero() {
		out.StartedAt = strPtr(s.SessionStart.Format(time.RFC3339))
	}
	if !s.CheckedAt.IsZero() {
		out.CheckedAt = strPtr(s.CheckedAt.Format(time.RFC3339))
	}
	return out
}

// recInput mirrors the UI's RecInput; pointers distinguish "absent" from zero values.
type recInput struct {
	URL                string  `json:"url"`
	StreamerName       *string `json:"streamer_name"`
	Quality            *string `json:"quality"`
	RecordFormat       *string `json:"record_format"`
	SegmentRecord      *bool   `json:"segment_record"`
	SegmentTime        *string `json:"segment_time"`
	VideoBitrate       *int    `json:"video_bitrate"`
	RecordingDir       *string `json:"recording_dir"`
	MonitorStatus      *bool   `json:"monitor_status"`
	ScheduledRecording *bool   `json:"scheduled_recording"`
	ScheduledStartTime *string `json:"scheduled_start_time"`
	MonitorHours       *string `json:"monitor_hours"`
	EnabledMessagePush *bool   `json:"enabled_message_push"`
	OnlyNotifyNoRecord *bool   `json:"only_notify_no_record"`
}

func (in recInput) toRoomInput() monitor.RoomInput {
	out := monitor.RoomInput{
		URL: in.URL, Name: in.StreamerName, Quality: in.Quality, Format: in.RecordFormat,
		Segment: in.SegmentRecord, RecordDir: in.RecordingDir, Monitor: in.MonitorStatus,
		Scheduled: in.ScheduledRecording, Push: in.EnabledMessagePush, NotifyOnly: in.OnlyNotifyNoRecord,
	}
	if in.VideoBitrate != nil {
		b := *in.VideoBitrate
		out.BitrateK = &b
	} else if in.StreamerName != nil || in.Quality != nil { // a full form submit with bitrate cleared
		zero := 0
		out.BitrateK = &zero
	}
	if in.SegmentTime != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(*in.SegmentTime)); err == nil && n > 0 {
			out.SegmentSec = &n
		}
	}
	if in.ScheduledStartTime != nil {
		v := strings.Split(*in.ScheduledStartTime, ",")[0]
		out.ScheduleStart = &v
	}
	if in.MonitorHours != nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(strings.Split(*in.MonitorHours, ",")[0]), 64); err == nil {
			out.ScheduleHours = &f
		}
	}
	return out
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func danmakuCount(s monitor.State) *int {
	if !s.DanmakuOn {
		return nil
	}
	n := s.Danmaku
	return &n
}
