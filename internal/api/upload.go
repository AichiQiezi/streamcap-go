package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/AichiQiezi/streamcap-go/internal/upload"
)

// ---- Bilibili account ----------------------------------------------------------------------------

func (s *Server) biliAccount(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"account": s.up.Account()})
}

func (s *Server) biliLogin(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	code, err := s.up.StartLogin(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	img, err := qr.Encode(code.URL, qr.M)
	if err != nil {
		fail(w, http.StatusInternalServerError, "生成二维码失败："+err.Error())
		return
	}
	img.Scale = 8
	writeJSON(w, map[string]string{
		"auth_code": code.AuthCode,
		"qr":        "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG()),
	})
}

func (s *Server) biliLoginPoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AuthCode string `json:"auth_code"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.AuthCode) == "" {
		fail(w, http.StatusBadRequest, "缺少 auth_code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	status, err := s.up.PollLogin(ctx, body.AuthCode)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"status": status, "account": s.up.Account()})
}

func (s *Server) biliLogout(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	s.up.Logout()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) biliPartitions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	parts, err := s.up.Partitions(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"partitions": parts})
}

// ---- upload tasks --------------------------------------------------------------------------------

func (s *Server) listUploads(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"tasks": s.up.Tasks()})
}

// uploadDraft is a submission form prefilled from the settings' templates for one recording.
func (s *Server) uploadDraft(w http.ResponseWriter, r *http.Request) {
	full, ok := s.resolveFile(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	d := s.up.Draft([]string{full}, s.up.VarsForFile(full))
	writeJSON(w, map[string]any{
		"title": d.Title, "desc": d.Desc, "tags": d.Tags, "tid": d.Tid, "copyright": d.Copyright, "source": d.Source,
		"only_self": d.OnlySelf,
	})
}

func (s *Server) addUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths     []string `json:"paths"` // relative to the save root, as /api/files lists them
		Title     string   `json:"title"`
		Desc      string   `json:"desc"`
		Tags      string   `json:"tags"`
		Tid       int      `json:"tid"`
		Copyright int      `json:"copyright"`
		Source    string   `json:"source"`
		OnlySelf  bool     `json:"only_self"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.up.Account() == nil {
		fail(w, http.StatusConflict, "请先在「设置 → B站投稿」扫码登录")
		return
	}
	req := upload.Request{Title: in.Title, Desc: in.Desc, Tags: in.Tags, Tid: in.Tid, Copyright: in.Copyright, Source: in.Source,
		OnlySelf: in.OnlySelf}
	for _, p := range in.Paths {
		full, ok := s.resolveFile(w, p)
		if !ok {
			return
		}
		req.Files = append(req.Files, full)
	}
	task, err := s.up.Enqueue(req)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, task)
}

func (s *Server) uploadAction(fn func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct{}
			if !decode(w, r, &body) {
				return
			}
		}
		if err := fn(r.PathValue("id")); err != nil {
			code := http.StatusConflict
			if errors.Is(err, upload.ErrNotFound) {
				code = http.StatusNotFound
			}
			fail(w, code, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}
