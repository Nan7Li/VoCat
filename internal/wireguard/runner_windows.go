//go:build windows

package wireguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// windowsRunner controls the official WireGuard for Windows tunnel service.
// The service owns the WireGuardNT adapter, while wg.exe exposes the same
// interface dump format used by the Linux runner.
type windowsRunner struct {
	wireguard string
	wg        string
}

func defaultRunner() Runner {
	return windowsRunner{
		wireguard: findWindowsTool("wireguard.exe", "HALO_WIREGUARD_EXE"),
		wg:        findWindowsTool("wg.exe", "HALO_WG_EXE"),
	}
}

func (runner windowsRunner) Available() bool {
	return runner.wireguard != "" && runner.wg != ""
}

func (runner windowsRunner) List(ctx context.Context) ([]string, error) {
	output, err := runner.runWG(ctx, "show", "interfaces")
	if err != nil {
		if isMissingWireGuardInterface(string(output)) {
			return nil, nil
		}
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

func (runner windowsRunner) Up(ctx context.Context, iface, configPath string) error {
	// Reinstalling the named tunnel makes updates deterministic even when a
	// previous service exists but is stopped outside of Halo.
	_, _ = runner.runWireGuard(ctx, "/uninstalltunnelservice", iface)
	output, err := runner.runWireGuard(ctx, "/installtunnelservice", configPath)
	if err != nil {
		return fmt.Errorf("install WireGuard tunnel %s: %w (%s)", iface, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (runner windowsRunner) Down(ctx context.Context, iface, _ string) error {
	output, err := runner.runWireGuard(ctx, "/uninstalltunnelservice", iface)
	if err != nil && !strings.Contains(strings.ToLower(string(output)), "does not exist") {
		return fmt.Errorf("uninstall WireGuard tunnel %s: %w (%s)", iface, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (runner windowsRunner) Show(ctx context.Context, iface string) (InterfaceInfo, error) {
	output, err := runner.runWG(ctx, "show", iface, "dump")
	if err != nil {
		if isMissingWireGuardInterface(string(output)) {
			return InterfaceInfo{}, nil
		}
		return InterfaceInfo{}, err
	}
	return parseWGDump(string(output)), nil
}

func (runner windowsRunner) runWireGuard(ctx context.Context, args ...string) ([]byte, error) {
	return runWindowsCommand(ctx, runner.wireguard, args...)
}

func (runner windowsRunner) runWG(ctx context.Context, args ...string) ([]byte, error) {
	return runWindowsCommand(ctx, runner.wg, args...)
}

func runWindowsCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	if path == "" {
		return nil, ErrAvailable
	}
	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w", filepath.Base(path), strings.Join(args, " "), err)
	}
	return output, nil
}

func findWindowsTool(name, envName string) string {
	if configured := strings.TrimSpace(os.Getenv(envName)); configured != "" {
		if path, err := exec.LookPath(configured); err == nil {
			return path
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	programFiles := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432")}
	for _, root := range programFiles {
		if strings.TrimSpace(root) == "" {
			continue
		}
		path := filepath.Join(root, "WireGuard", name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func isMissingWireGuardInterface(output string) bool {
	text := strings.ToLower(output)
	return strings.TrimSpace(text) == "" ||
		strings.Contains(text, "no such device") ||
		strings.Contains(text, "unable to access interface") ||
		strings.Contains(text, "not found")
}
