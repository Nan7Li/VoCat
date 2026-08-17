//go:build linux

package server

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func notificationViaInterfaceDialer(
	timeout time.Duration,
	iface string,
	mark int,
) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 0,
		Control: func(_, _ string, conn syscall.RawConn) error {
			var sockErr error
			if err := conn.Control(func(fd uintptr) {
				if bindErr := unix.BindToDevice(int(fd), iface); bindErr != nil {
					sockErr = fmt.Errorf("bind Telegram traffic to %s: %w", iface, bindErr)
					return
				}
				if mark > 0 {
					if markErr := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark); markErr != nil {
						sockErr = fmt.Errorf("mark Telegram traffic via %s: %w", iface, markErr)
					}
				}
			}); err != nil {
				return err
			}
			return sockErr
		},
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
}
