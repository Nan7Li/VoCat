//go:build windows

package modem

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.bug.st/serial/enumerator"
)

// NewSystemDiscoverer returns the Windows COM-port discoverer. Windows does
// not expose Linux's /sys and /dev topology, but go-serial can query the
// system's serial-port registry and USB VID/PID metadata through SetupAPI.
func NewSystemDiscoverer() Discoverer { return windowsSerialDiscoverer{} }

type windowsSerialDiscoverer struct{}

func (windowsSerialDiscoverer) Discover(ctx context.Context) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, fmt.Errorf("discover Windows COM ports: %w", err)
	}

	result := make([]Candidate, 0, len(ports))
	for _, detail := range ports {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if detail == nil || strings.TrimSpace(detail.Name) == "" {
			continue
		}

		// Keep USB serial devices visible rather than guessing which modem
		// vendors are supported. The normal AT health probe is the authority;
		// this also supports carrier-branded or re-flashed modem firmware whose
		// VID/PID is not in a hard-coded allow-list.
		name := strings.TrimSpace(detail.Name)
		vendorID := strings.ToLower(strings.TrimSpace(detail.VID))
		productID := strings.ToLower(strings.TrimSpace(detail.PID))
		manufacturer, product := windowsUSBIdentity(vendorID, detail.Product)
		port := Port{
			Path:       name,
			StablePath: name,
			Name:       name,
			Role:       PortRoleUnknown,
		}
		serialNumber := strings.TrimSpace(detail.SerialNumber)
		id := candidateID(vendorID, productID, serialNumber, name)
		result = append(result, Candidate{
			HardwareKind:     "windows-com",
			ID:               id,
			VendorID:         vendorID,
			ProductID:        productID,
			Manufacturer:     manufacturer,
			Product:          product,
			SerialNumber:     serialNumber,
			USBPath:          "COM:" + name,
			ATPort:           port,
			Ports:            []Port{port},
		})
	}

	// A Windows COM port is only the modem control plane. When the Mobile
	// Broadband service exposes exactly one cellular interface, attach it to
	// every serial candidate so the existing AT identity probe and the native
	// Windows WWAN data backend refer to the same physical modem. With several
	// interfaces there is no stable parent relation in the serial enumerator;
	// leaving the field empty is safer than routing traffic through the wrong
	// SIM when multiple modems are attached.
	if interfaces, err := discoverWindowsCellularInterfaces(ctx); err != nil {
		return nil, err
	} else if len(interfaces) == 1 {
		for index := range result {
			result[index].NetworkInterface = interfaces[0]
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].ATPort.Name < result[j].ATPort.Name })
	return result, nil
}

func windowsUSBIdentity(vendorID, product string) (manufacturer, normalizedProduct string) {
	normalizedProduct = strings.TrimSpace(product)
	switch strings.ToLower(strings.TrimSpace(vendorID)) {
	case "2c7c":
		manufacturer = "Quectel"
		if normalizedProduct == "" {
			normalizedProduct = "Quectel modem"
		}
	case "2ca3":
		manufacturer = "DJI / Baiwang"
		if normalizedProduct == "" {
			normalizedProduct = "DJI / Baiwang 4G modem"
		}
	}
	return manufacturer, normalizedProduct
}
