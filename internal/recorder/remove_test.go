package recorder

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRemoveRetryWaitsForAnOpenFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses to delete an open file")
	}
	f := filepath.Join(t.TempDir(), "part.ts")
	if err := os.WriteFile(f, []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := os.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	if os.Remove(f) == nil {
		t.Skip("this filesystem deletes open files; nothing to retry")
	}
	time.AfterFunc(time.Second, func() { h.Close() })
	if err := removeRetry(f); err != nil {
		t.Fatalf("removeRetry: %v", err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
}
