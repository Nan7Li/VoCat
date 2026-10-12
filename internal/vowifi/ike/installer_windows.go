//go:build windows && (amd64 || arm64)

package ike

import (
	"context"
)

type windowsChildSAInstaller struct{}

func defaultChildSAInstaller() ChildSAInstaller { return windowsChildSAInstaller{} }

func (windowsChildSAInstaller) Install(ctx context.Context, config ChildSAConfig) (ChildSAHandle, error) {
	return (windowsUserspaceInstaller{}).Install(ctx, config)
}
