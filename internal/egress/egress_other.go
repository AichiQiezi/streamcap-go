//go:build !windows && !darwin

package egress

import (
	"errors"
	"net"
	"syscall"
)

// pin is a no-op here: the source address alone (set by the dialer) is all we do.
func pin(int) func(string, string, syscall.RawConn) error { return nil }

func autoInterface() (*net.Interface, error) {
	return nil, errors.New("自动选择仅支持 Windows")
}
