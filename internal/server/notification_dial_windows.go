//go:build windows

package server

import (
	"context"
	"fmt"
	"math/bits"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsNotificationIPUnicastIf   = 31
	windowsNotificationIPv6UnicastIf = 31
)

func notificationViaInterfaceDialer(
	timeout time.Duration,
	iface string,
	_ int,
) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 0,
		Control: func(network, _ string, raw syscall.RawConn) error {
			interfaceInfo, err := net.InterfaceByName(iface)
			if err != nil {
				return fmt.Errorf("find cellular interface %q: %w", iface, err)
			}
			if interfaceInfo.Index <= 0 {
				return fmt.Errorf("cellular interface %q has invalid index %d", iface, interfaceInfo.Index)
			}
			index := uint32(interfaceInfo.Index)
			return raw.Control(func(fd uintptr) {
				if network != "tcp6" {
					_ = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, windowsNotificationIPUnicastIf, int(bits.ReverseBytes32(index)))
				}
				if network != "tcp4" {
					_ = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, windowsNotificationIPv6UnicastIf, int(index))
				}
			})
		},
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
}
