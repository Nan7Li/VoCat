//go:build windows

package main

import "vocat/internal/winifmib"

func readInterfaceTrafficCounters(interfaceName string) (uint64, uint64, error) {
	return winifmib.InterfaceCounters(interfaceName)
}

