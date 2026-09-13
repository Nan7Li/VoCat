//go:build !windows

package device

import (
	"context"
	"fmt"

	"vocat/internal/modem"
)

func isWindowsMBNCandidate(modem.Candidate) bool { return false }

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
