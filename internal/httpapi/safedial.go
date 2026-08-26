package httpapi

import (
	"fmt"
	"net"
	"syscall"
	"time"
)

// publicAddr reports whether an address is somewhere on the public internet.
//
// Everything else is refused: loopback, the RFC1918 ranges, link-local
// (169.254.0.0/16, which is where cloud metadata services live), carrier-grade
// NAT, IPv6 unique-local, multicast and the reserved blocks. On this server
// that list covers the Docker bridges, the Unraid host, the LAN and the
// container itself.
func publicAddr(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 100 && v4[1]&0xc0 == 64: // 100.64.0.0/10, carrier-grade NAT
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24, IETF protocol assignments
			return false
		case v4[0] == 198 && v4[1]&0xfe == 18: // 198.18.0.0/15, benchmarking
			return false
		case v4[0] >= 240: // 240.0.0.0/4, reserved, includes broadcast
			return false
		}
		return true
	}
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc { // fc00::/7, unique-local
		return false
	}
	return true
}

// safeDialer refuses to open a connection to a non-public address.
//
// The check belongs here rather than on the hostname because this is the only
// place the address is finally known. A name checked up front and resolved
// afterwards is two different answers: a host can return a public address to
// the check and a private one to the connection a moment later, and each
// redirect hop resolves separately. Control runs with the concrete IP the
// socket is about to reach, so every hop and every retry is covered by the
// same rule.
var safeDialer = &net.Dialer{
	Timeout:   10 * time.Second,
	KeepAlive: 30 * time.Second,
	Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("refusing to dial %q", address)
		}
		if ip := net.ParseIP(host); !publicAddr(ip) {
			return fmt.Errorf("%s is not a public address", host)
		}
		return nil
	},
}
