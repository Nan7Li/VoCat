//go:build !linux && !windows

package modem

func NewSystemDiscoverer() Discoverer {
	return unsupportedDiscoverer{}
}
