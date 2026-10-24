package egress

import (
	"errors"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// pin binds a socket to interface index (IP_BOUND_IF), past the routing table.
func pin(index int) func(string, string, syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, index)
		}); err != nil {
			return err
		}
		return serr
	}
}

// autoInterface is not implemented on macOS: pick the interface by name (usually en0).
func autoInterface() (*net.Interface, error) {
	return nil, errors.New("自动选择仅支持 Windows，请直接选择网卡（通常是 en0）")
}
