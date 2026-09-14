//go:build windows && !amd64 && !arm64

package ike

import (
	"context"
	"errors"
)

type windowsChildSAInstaller struct{}

func defaultChildSAInstaller() ChildSAInstaller { return windowsChildSAInstaller{} }

func (windowsChildSAInstaller) Install(context.Context, ChildSAConfig) (ChildSAHandle, error) {
	return nil, errors.New("ike: Windows ePDG CHILD_SA data plane requires amd64 or arm64; refusing to report a tunnel as established")
}
