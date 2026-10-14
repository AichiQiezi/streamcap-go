//go:build !windows && !darwin

package desktop

// Run is not available here (Linux etc.): the caller falls back to the browser.
func Run(Options) error { return ErrUnsupported }

// SetStatus is a no-op without a tray.
func SetStatus(string) {}

// Alert prints nothing special off Windows/macOS; errors go to the log and stderr.
func Alert(string, string) {}
