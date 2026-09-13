//go:build !windows

package device

import (
	"context"
	"fmt"

	"vocat/internal/modem"
)

func isWindowsMBNCandidate(modem.Candidate) bool { return false }

func (*Manager) readWindowsMBNICCID(context.Context, *managedDevice, modem.Candidate) string {
	return ""
}

func setWindowsCellularNetwork(
	context.Context,
	modem.Candidate,
	bool,
	string,
	string,
	string,
	string,
	string,
	string,
) (NetworkResult, error) {
	return NetworkResult{}, fmt.Errorf("%w: Windows MBN is unavailable on this platform", ErrDataBackendUnavailable)
}
