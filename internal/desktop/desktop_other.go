//go:build !windows

package desktop

import "errors"

// Run is the native desktop entry point.  Keep a real stub on other systems
// so adding the Windows desktop binary does not change Linux/OpenWrt builds.
func Run() error {
	return errors.New("vocat desktop is available only on Windows")
}

// ShowError is a no-op outside Windows; command-line callers already report
// the returned error through their normal logger.
func ShowError(error) {}
