package store

import "time"

// BiliAccount is the Bilibili account videos are uploaded to, from the TV QR login. The tokens
// authorise the app submit API; the cookies (SESSDATA, bili_jct, …) the web upload endpoints.
type BiliAccount struct {
	Mid          int64             `json:"mid"`
	Name         string            `json:"name"`
	Face         string            `json:"face"`
	AccessToken  string            `json:"access_token"`
	RefreshToken string            `json:"refresh_token"`
	ExpiresAt    time.Time         `json:"expires_at"`
	Cookies      map[string]string `json:"cookies"`
	LoginAt      time.Time         `json:"login_at"`
}

// UploadState is where an upload task is.
type UploadState string

const (
	UploadQueued     UploadState = "queued"
	UploadUploading  UploadState = "uploading"
	UploadSubmitting UploadState = "submitting"
	UploadDone       UploadState = "done"
	UploadFailed     UploadState = "failed"
	UploadCanceled   UploadState = "canceled"
)

// UploadTask is one Bilibili submission (稿件): one or more files, each becoming a part (P).
type UploadTask struct {
	ID    string   `json:"id"`
	Files []string `json:"files"` // absolute paths
	// Archive fields as sent to Bilibili.
	Title     string `json:"title"`
	Desc      string `json:"desc"`
	Tags      string `json:"tags"` // comma-separated
	Tid       int    `json:"tid"`
	Copyright int    `json:"copyright"` // 1 自制, 2 转载
	Source    string `json:"source"`
	// RoomID is set for tasks queued automatically after a live.
	RoomID string `json:"room_id,omitempty"`

	State      UploadState `json:"state"`
	Error      string      `json:"error,omitempty"`
	Total      int64       `json:"total"`
	Sent       int64       `json:"sent"`
	Bvid       string      `json:"bvid,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	FinishedAt time.Time   `json:"finished_at,omitempty"`
}

// UploadData is uploads.json: the account and the task list (newest last).
type UploadData struct {
	Account *BiliAccount `json:"account,omitempty"`
	Tasks   []UploadTask `json:"tasks"`
}
