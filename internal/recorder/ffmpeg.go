// Package recorder runs ffmpeg to capture one continuous piece ("part") of a live stream and
// post-processes finished sessions (merge parts, remux to MP4).
package recorder

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

var (
	ffmpegOnce sync.Once
	ffmpegBin  string
	ffprobeBin string
)

// Binaries returns the ffmpeg and ffprobe paths: next to the executable (bundled Windows build,
// either beside it or in an "ffmpeg" sub-folder) first, then PATH.
func Binaries() (ffmpeg, ffprobe string) {
	ffmpegOnce.Do(func() {
		ffmpegBin = find("ffmpeg")
		ffprobeBin = find("ffprobe")
	})
	return ffmpegBin, ffprobeBin
}

func find(name string) string {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, p := range []string{filepath.Join(dir, exe), filepath.Join(dir, "ffmpeg", exe), filepath.Join(dir, "ffmpeg", "bin", exe)} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// buildArgs assembles the ffmpeg command line for one part.
func buildArgs(job *Job, output string) []string {
	args := []string{"-y", "-hide_banner", "-loglevel", "error"} // stdin stays open: "q" is the graceful stop
	// Everything up to "-i" is an input option.
	isHTTP := strings.HasPrefix(job.URL, "http://") || strings.HasPrefix(job.URL, "https://")
	if job.Proxy != "" && isHTTP {
		args = append(args, "-http_proxy", job.Proxy)
	}
	args = append(args, "-rw_timeout", "15000000", "-user_agent", userAgent)
	if len(job.Headers) > 0 {
		keys := make([]string, 0, len(job.Headers))
		for k := range job.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k + ": " + job.Headers[k] + "\r\n")
		}
		args = append(args, "-headers", b.String())
	}
	if isHTTP {
		// Retry transient network errors inside one ffmpeg run. These are http-protocol options:
		// on any other input ffmpeg aborts with "Option reconnect not found". No -reconnect_at_eof:
		// a clean EOF must end the run so the monitor re-resolves a fresh URL.
		args = append(args, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "8")
	}
	args = append(args,
		"-analyzeduration", "15000000", "-probesize", "10000000",
		"-fflags", "+discardcorrupt+genpts",
		"-thread_queue_size", "1024",
		"-i", job.URL,
		"-sn", "-dn",
		"-max_muxing_queue_size", "2048",
	)

	audioOnly := isAudio(job.Format)
	if audioOnly {
		args = append(args, "-map", "0:a?", "-vn")
		switch job.Format {
		case "MP3":
			args = append(args, "-c:a", "libmp3lame", "-b:a", "320k")
		case "WAV":
			args = append(args, "-c:a", "pcm_s16le")
		default: // M4A / AAC
			args = append(args, "-c:a", "copy")
		}
	} else {
		args = append(args, "-map", "0:v?", "-map", "0:a?")
		if job.BitrateK > 0 {
			args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-b:v", itoa(job.BitrateK)+"k")
		} else {
			args = append(args, "-c:v", "copy")
		}
		args = append(args, "-c:a", "copy")
	}

	muxer := muxerFor(job.Format)
	if job.Segment && job.SegmentSec > 0 {
		args = append(args, "-f", "segment", "-segment_time", itoa(job.SegmentSec), "-segment_format", muxer, "-reset_timestamps", "1")
		if muxer == "mp4" || muxer == "mov" {
			args = append(args, "-segment_format_options", "movflags=+frag_keyframe+empty_moov")
		}
	} else {
		args = append(args, "-f", muxer)
		if muxer == "mp4" || muxer == "mov" {
			// Fragmented so the file is playable (and probe-able) while still being written,
			// and survives a hard kill.
			args = append(args, "-movflags", "+frag_keyframe+empty_moov+default_base_moof")
		}
	}
	if muxer == "mpegts" {
		args = append(args, "-mpegts_flags", "+resend_headers")
	}
	args = append(args, "-flush_packets", "1", output)
	return args
}

func isAudio(format string) bool {
	switch strings.ToUpper(format) {
	case "MP3", "M4A", "AAC", "WAV":
		return true
	}
	return false
}

func muxerFor(format string) string {
	switch strings.ToUpper(format) {
	case "MP4":
		return "mp4"
	case "FLV":
		return "flv"
	case "MKV":
		return "matroska"
	case "MOV":
		return "mov"
	case "MP3":
		return "mp3"
	case "M4A":
		return "ipod"
	case "AAC":
		return "adts"
	case "WAV":
		return "wav"
	default:
		return "mpegts"
	}
}

// Ext is the file extension (without dot) for a format.
func Ext(format string) string {
	switch strings.ToUpper(format) {
	case "MP4", "FLV", "MKV", "MOV", "MP3", "M4A", "AAC", "WAV":
		return strings.ToLower(format)
	default:
		return "ts"
	}
}
