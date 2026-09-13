//go:build !linux

package qmiport

import (
	"context"
	"errors"
)

// Lease is a no-op placeholder on platforms without Linux QMI character
// devices. Windows uses the AT-over-COM backend.
type Lease struct{}

var ErrUnsupported = errors.New("QMI control ports are supported only on Linux")

func Acquire(context.Context, string) (*Lease, error) { return nil, ErrUnsupported }
func (*Lease) Release()                              {}
