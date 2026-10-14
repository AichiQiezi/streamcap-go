//go:build !windows

package media

import "os/exec"

func hide(*exec.Cmd) {}
