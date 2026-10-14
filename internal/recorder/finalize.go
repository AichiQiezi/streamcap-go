package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FinalizeOptions control what happens to a session's files once the live ends.
type FinalizeOptions struct {
	Merge          bool // join the session's parts into one file (skipped when segmenting)
	ConvertToMP4   bool // remux TS/FLV/MKV/MOV to MP4 (stream copy, no re-encode)
	DeleteOriginal bool // delete the source files after a successful merge/remux (else move to original/)
}

// Finalize merges/remuxes a finished session's files and returns the resulting files.
// It never deletes inputs unless the output was produced and is non-empty.
func Finalize(ctx context.Context, files []string, format string, segmented bool, opt FinalizeOptions) []string {
	files = existing(files)
	if len(files) == 0 {
		return nil
	}
	out := files
	if opt.Merge && !segmented && len(files) > 1 {
		if tmp, err := concat(ctx, files, format); err != nil {
			slog.Error("merge failed, keeping parts", "err", err, "first", files[0])
		} else {
			// The session's first part carries the session name; the merged file takes it over.
			final := files[0]
			disposeOriginals(files, opt.DeleteOriginal)
			if err := os.Rename(tmp, final); err != nil {
				slog.Error("rename merged file failed", "tmp", tmp, "err", err)
				final = tmp
			}
			out = []string{final}
		}
	}
	if opt.ConvertToMP4 && !isAudio(format) && strings.ToUpper(format) != "MP4" {
		var converted []string
		for _, f := range out {
			mp4, err := remuxMP4(ctx, f)
			if err != nil {
				slog.Error("remux to mp4 failed, keeping original", "file", f, "err", err)
				converted = append(converted, f)
				continue
			}
			disposeOriginals([]string{f}, opt.DeleteOriginal)
			converted = append(converted, mp4)
		}
		out = converted
	}
	return out
}

// concat joins files (same codec parameters, as consecutive parts of one live are) without
// re-encoding, into a temp file next to them; the caller renames it into place.
func concat(ctx context.Context, files []string, format string) (string, error) {
	ffmpeg, _ := Binaries()
	dir := filepath.Dir(files[0])
	base := strings.TrimSuffix(filepath.Base(files[0]), filepath.Ext(files[0]))
	list := filepath.Join(dir, "."+base+".concat.txt")
	var b strings.Builder
	for _, f := range files {
		abs, _ := filepath.Abs(f)
		b.WriteString("file '" + strings.ReplaceAll(filepath.ToSlash(abs), "'", `'\''`) + "'\n")
	}
	if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	defer os.Remove(list)
	tmp := filepath.Join(dir, base+".merging."+Ext(format))
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", list,
		"-map", "0", "-c", "copy", "-fflags", "+genpts"}
	if m := muxerFor(format); m == "mp4" || m == "mov" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-f", muxerFor(format), tmp)
	if err := runFFmpeg(ctx, ffmpeg, args); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// remuxMP4 copies streams into an MP4 container next to the source.
func remuxMP4(ctx context.Context, src string) (string, error) {
	ffmpeg, _ := Binaries()
	out := strings.TrimSuffix(src, filepath.Ext(src)) + ".mp4"
	tmp := out + ".tmp.mp4"
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", src, "-map", "0", "-c", "copy",
		"-bsf:a", "aac_adtstoasc", "-movflags", "+faststart", tmp}
	if err := runFFmpeg(ctx, ffmpeg, args); err != nil {
		// Some sources carry non-AAC audio where the bitstream filter fails; retry without it.
		args = []string{"-y", "-hide_banner", "-loglevel", "error", "-i", src, "-map", "0", "-c", "copy",
			"-movflags", "+faststart", tmp}
		if err2 := runFFmpeg(ctx, ffmpeg, args); err2 != nil {
			os.Remove(tmp)
			return "", fmt.Errorf("%v; retry: %v", err, err2)
		}
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	return out, nil
}

func runFFmpeg(ctx context.Context, bin string, args []string) error {
	if bin == "" {
		return fmt.Errorf("ffmpeg not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	var tail tailBuffer
	tail.max = 2048
	cmd.Stderr = &tail
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, tail.String())
	}
	if st, err := os.Stat(args[len(args)-1]); err != nil || st.Size() == 0 {
		return fmt.Errorf("output missing or empty")
	}
	return nil
}

// disposeOriginals deletes inputs, or moves them into an "original" sub-folder.
func disposeOriginals(files []string, del bool) {
	for _, f := range files {
		if del {
			if err := os.Remove(f); err != nil {
				slog.Warn("remove original failed", "file", f, "err", err)
			}
			continue
		}
		dir := filepath.Join(filepath.Dir(f), "original")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			_ = os.Rename(f, filepath.Join(dir, filepath.Base(f)))
		}
	}
}

func existing(files []string) []string {
	var out []string
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.Size() > 0 {
			out = append(out, f)
		}
	}
	return out
}
