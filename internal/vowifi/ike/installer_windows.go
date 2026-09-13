//go:build windows

package ike

import (
	"context"
	"errors"
)

// Windows WFP can host manually keyed transport SAs and has tunnel/NAT-T
// primitives, but a manual tunnel SA alone does not provide the inner address,
// virtual interface, and selector-scoped routing that ChildSAConfig requires.
// Until the WFP virtual-interface path or a Wintun user-space ESP/NAT-T path is
// complete, fail before claiming a tunnel exists.
type windowsChildSAInstaller struct{}

func defaultChildSAInstaller() ChildSAInstaller { return windowsChildSAInstaller{} }

func (windowsChildSAInstaller) Install(context.Context, ChildSAConfig) (ChildSAHandle, error) {
	return nil, errors.New("ike: Windows ePDG CHILD_SA data plane is not available: WFP manual tunnel SAs do not yet provide the required inner interface and selector routing, and the Wintun ESP/NAT-T path is not installed; refusing to report a tunnel as established")
}
