package main

import (
	"strings"
	"vocat/internal/device"
	"vocat/internal/store"
)

func reconcileWindowsDeviceConfig(config *store.Device, physical device.Device) bool {
	if config == nil || physical.Candidate.HardwareKind != "windows-com" {
		return false
	}
	networkInterface := strings.TrimSpace(physical.Candidate.NetworkInterface)
	if networkInterface == "" || strings.TrimSpace(config.Interface) != "" {
		return false
	}
	config.Interface = networkInterface
	backend := strings.ToLower(strings.TrimSpace(config.DeviceBackend))
	if backend == "" || backend == "at" {
		config.DeviceBackend = "mbn"
		if strings.TrimSpace(config.ESIMTransport) == "" {
			config.ESIMTransport = "at"
		}
	}
	return true
}
