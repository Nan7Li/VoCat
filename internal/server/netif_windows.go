//go:build windows

package server

import "vocat/internal/winifmib"

func netIfCounters(interfaceName string) (uint64, uint64, error) {
	return winifmib.InterfaceCounters(interfaceName)
}

