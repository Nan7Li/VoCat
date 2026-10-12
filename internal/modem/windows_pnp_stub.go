//go:build !windows

package modem

import "context"

// WindowsUSBDeviceProblem is the cross-platform shape used by doctor. The
// Windows implementation fills it from SetupAPI; other platforms return no
// Windows diagnostics.
type WindowsUSBDeviceProblem struct {
	InstanceID   string `json:"instanceId"`
	FriendlyName string `json:"friendlyName,omitempty"`
	ProblemCode  uint32 `json:"problemCode"`
}

func WindowsUSBModemProblems(context.Context) ([]WindowsUSBDeviceProblem, error) {
	return nil, nil
}
