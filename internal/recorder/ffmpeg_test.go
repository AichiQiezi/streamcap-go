package recorder

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Started from Finder or the Dock, the app's PATH lacks Homebrew: ffmpeg must still be found.
func TestFindsFFmpegOutsidePATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("knownDirs are for macOS")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := knownDirs
	knownDirs = []string{t.TempDir(), dir}
	t.Cleanup(func() { knownDirs = old })
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin") // launchd's

	if got := find("ffmpeg"); got != filepath.Join(dir, "ffmpeg") {
		t.Fatalf("find(ffmpeg) = %q, want the one in %s", got, dir)
	}
	if got := find("ffprobe"); got != "" {
		t.Fatalf("find(ffprobe) = %q, want none (not installed there)", got)
	}
}
