package egress

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ipUnicastIf = 31 // IP_UNICAST_IF (ws2ipdef.h)

// pin makes a socket send out of interface index regardless of the routing table. Windows takes
// the IPv4 index in network byte order.
func pin(index int) func(string, string, syscall.RawConn) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(index))
	val := int(binary.LittleEndian.Uint32(b[:]))
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf, val)
		}); err != nil {
			return err
		}
		return serr
	}
}

// tunnelWords mark virtual adapters that VPNs and proxies install; their Ethernet-looking
// IfType doesn't give them away (OpenVPN's TAP adapter is one).
var tunnelWords = []string{"tap-windows", "tap-", "tun", "wintun", "wireguard", "openvpn", "vpn", "clash", "mihomo",
	"meta", "zerotier", "tailscale", "virtual", "vmware", "hyper-v", "virtualbox", "loopback"}

// autoInterface is the physical adapter with an IPv4 default gateway and the lowest metric: the
// way out the machine would use if no VPN had claimed the routes.
func autoInterface() (*net.Interface, error) {
	size := uint32(16 << 10)
	var buf []byte
	for {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return nil, err
		}
	}
	var (
		best   *windows.IpAdapterAddresses
		metric uint32
	)
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp || a.FirstGatewayAddress == nil {
			continue
		}
		switch a.IfType {
		case windows.IF_TYPE_TUNNEL, windows.IF_TYPE_PPP, windows.IF_TYPE_SOFTWARE_LOOPBACK:
			continue
		}
		desc := strings.ToLower(windows.UTF16PtrToString(a.Description) + " " + windows.UTF16PtrToString(a.FriendlyName))
		if containsAny(desc, tunnelWords) {
			continue
		}
		hasGW := false
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			if ip := g.Address.IP(); ip != nil && ip.To4() != nil && !ip.IsUnspecified() {
				hasGW = true
			}
		}
		if hasGW && (best == nil || a.Ipv4Metric < metric) {
			best, metric = a, a.Ipv4Metric
		}
	}
	if best == nil {
		return nil, errors.New("no physical adapter with a default gateway")
	}
	return net.InterfaceByIndex(int(best.IfIndex))
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
