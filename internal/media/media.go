// Package media probes recordings (resolution/codec) and grabs cover thumbnails from local files.
//
// It only ever reads files on disk — never the stream URL — because some CDNs hand out single-use
// URLs and a second pull on the recorder's URL breaks the recording.
package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/AichiQiezi/streamcap-go/internal/recorder"
)

// Each probe/thumbnail spawns a process; cap them so many parallel recordings don't spike the CPU.
var slots = make(chan struct{}, 2)

func acquire(ctx context.Context) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release() { <-slots }

// Info is what we show about a running recording's video.
type Info struct {
	Width  int
	Height int
	FPS    float64
	Codec  string
}

// Probe reads the first video stream's parameters from a (possibly still growing) file.
func Probe(ctx context.Context, file string) (Info, error) {
	_, ffprobe := recorder.Binaries()
	if ffprobe == "" {
		return Info{}, fmt.Errorf("ffprobe not found")
	}
	if err := acquire(ctx); err != nil {
		return Info{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height,avg_frame_rate", "-of", "json", file)
	hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return Info{}, err
	}
	var parsed struct {
		Streams []struct {
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed.Streams) == 0 {
		return Info{}, fmt.Errorf("no video stream")
	}
	s := parsed.Streams[0]
	return Info{Width: s.Width, Height: s.Height, FPS: parseRate(s.AvgFrameRate), Codec: s.CodecName}, nil
}

// Thumbnail writes a 640px-wide JPEG of a frame near the end of file to dest (atomically).
func Thumbnail(ctx context.Context, file, dest string) error {
	ffmpeg, _ := recorder.Binaries()
	if ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found")
	}
	if err := acquire(ctx); err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tmp := dest + ".tmp.jpg"
	cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-v", "error", "-sseof", "-3", "-i", file,
		"-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "4", tmp)
	hide(cmd)
	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		return err
	}
	if st, err := os.Stat(tmp); err != nil || st.Size() == 0 {
		os.Remove(tmp)
		return fmt.Errorf("empty thumbnail")
	}
	return os.Rename(tmp, dest)
}

func parseRate(r string) float64 {
	num, den, ok := strings.Cut(r, "/")
	if !ok {
		f, _ := strconv.ParseFloat(r, 64)
		return f
	}
	n, _ := strconv.ParseFloat(num, 64)
	d, _ := strconv.ParseFloat(den, 64)
	if d == 0 {
		return 0
	}
	return float64(int(n/d*100+0.5)) / 100
}
