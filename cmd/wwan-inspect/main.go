package main

import (
	"context"
	"encoding/json"
	"os"
	"time"
	"vocat/internal/modem"
)

// Enumerates host interfaces and serial metadata only. It never opens a modem
// port, reads a SIM identity, sends AT commands or changes radio/data state.
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries, err := modem.NewSystemDiscoverer().Discover(ctx)
	if err != nil {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	summary := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		summary = append(summary, map[string]any{"manufacturer": entry.Manufacturer, "hardware": entry.HardwareKind, "product": entry.Product, "port": entry.ATPort.Name, "role": entry.ATPort.Role, "cellular_interface": entry.NetworkInterface})
	}
	json.NewEncoder(os.Stdout).Encode(summary)
}
