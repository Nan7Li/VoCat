//go:build !linux

package server

import (
	"context"
	"fmt"
	"net"
	"time"
)

func notificationViaInterfaceDialer(
	timeout time.Duration,
	iface string,
	_ int,
) func(context.Context, string, string) (net.Conn, error) {
	_ = timeout
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("Telegram via WireGuard interface %q requires Linux", iface)
	}
}
