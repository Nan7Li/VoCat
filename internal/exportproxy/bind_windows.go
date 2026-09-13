//go:build windows

package exportproxy

import (
	"context"
	"fmt"
	"math/bits"
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	windowsIPUnicastIf  = 31
	windowsIPv6UnicastIf = 31
)

func platformSupported() error { return nil }

// Windows has no SO_BINDTODEVICE equivalent. Winsock exposes the same
// per-socket egress selection used by WireGuard: IP_UNICAST_IF takes the
// interface index in network byte order, while IPV6_UNICAST_IF takes host byte
// order. The interface is resolved at dial time so a modem can disappear and
// re-enumerate without restarting the proxy listener.
func boundDialer(networkInterface string) net.Dialer {
	return net.Dialer{Control: func(network, _ string, raw syscall.RawConn) error {
		iface, err := net.InterfaceByName(networkInterface)
		if err != nil {
			return fmt.Errorf("find cellular interface %q: %w", networkInterface, err)
		}
		if iface.Index <= 0 {
			return fmt.Errorf("cellular interface %q has invalid index %d", networkInterface, iface.Index)
		}
		index := uint32(iface.Index)
		return raw.Control(func(fd uintptr) {
			// The options are family-specific. Applying the wrong-family option
			// fails harmlessly on Windows, so set both for an unspecified TCP
			// family and let the socket's address family select the effective one.
			if network != "tcp6" {
				_ = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, windowsIPUnicastIf, int(bits.ReverseBytes32(index)))
			}
			if network != "tcp4" {
				_ = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, windowsIPv6UnicastIf, int(index))
			}
		})
	}}
}

func boundResolver(networkInterface string) *net.Resolver {
	dialer := boundDialer(networkInterface)
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var lastError error
		for _, server := range []string{"1.1.1.1", "8.8.8.8"} {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(server, "53"))
			if err == nil {
				return connection, nil
			}
			lastError = err
		}
		return nil, lastError
	}}
}
