//go:build windows

package modem

import (
	"context"
	"net"
	"os/exec"
	"sort"
	"strings"
)

// discoverWindowsCellularInterfaces asks Windows' Mobile Broadband service
// for the authoritative interface names. The fallback only considers names
// that look cellular; it must not accidentally bind a modem to an arbitrary
// Ethernet or Wi-Fi adapter.
func discoverWindowsCellularInterfaces(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, "netsh.exe", "mbn", "show", "interfaces")
	output, commandErr := command.Output()
	// `netsh mbn show interfaces` returns exit code 1 when a cellular
	// interface is present but currently disconnected. The command still
	// writes the authoritative interface list to stdout, so parse stdout
	// before treating the exit status as a reason to fall back. This is
	// especially important for localized interface names such as 手机网络,
	// which cannot be recovered reliably from the generic net.Interfaces()
	// name heuristic.
	if names := parseWindowsMBNInterfaceNames(string(output)); len(names) > 0 {
		return names, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = commandErr

	interfaces, err := net.Interfaces()
	if err != nil {
		// A serial modem is still useful for AT/SMS/eSIM when the IP Helper
		// enumeration is temporarily unavailable, so discovery remains best
		// effort here.
		return nil, nil
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		name := strings.TrimSpace(iface.Name)
		if !looksLikeWindowsCellularInterface(name) {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result, nil
}

func looksLikeWindowsCellularInterface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, marker := range []string{
		"cellular", "mobile broadband", "wwan", "mobile data", "lte", "5g",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
