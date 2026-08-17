//go:build linux

package wireguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type systemRunner struct{}

func defaultRunner() Runner {
	return systemRunner{}
}

func (systemRunner) List(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "wg", "show", "interfaces")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(output) + err.Error())
		if strings.Contains(text, "no such device") || strings.TrimSpace(string(output)) == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("wg show interfaces: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return strings.Fields(string(output)), nil
}

func (systemRunner) Available() bool {
	if _, err := exec.LookPath("wg"); err != nil {
		return false
	}
	if _, err := exec.LookPath("ip"); err == nil {
		return true
	}
	_, err := exec.LookPath("wg-quick")
	return err == nil
}

func (systemRunner) Up(ctx context.Context, iface, configPath string) error {
	if _, err := exec.LookPath("wg-quick"); err == nil {
		return runWGQuick(ctx, "up", configPath, iface)
	}
	return nativeUp(ctx, iface, configPath)
}

func (systemRunner) Down(ctx context.Context, iface, configPath string) error {
	if _, err := exec.LookPath("wg-quick"); err == nil {
		if err := runWGQuick(ctx, "down", configPath, iface); err == nil {
			return nil
		}
	}
	return nativeDown(ctx, iface)
}

func (systemRunner) Show(ctx context.Context, iface string) (InterfaceInfo, error) {
	cmd := exec.CommandContext(ctx, "wg", "show", iface, "dump")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(output) + err.Error())
		if strings.Contains(text, "no such device") ||
			strings.Contains(text, "cannot find device") ||
			strings.Contains(text, "unable to access interface") {
			return InterfaceInfo{}, nil
		}
		return InterfaceInfo{}, fmt.Errorf("wg show %s: %w (%s)", iface, err, strings.TrimSpace(string(output)))
	}
	return parseWGDump(string(output)), nil
}

func runWGQuick(ctx context.Context, action, configPath, iface string) error {
	cmd := exec.CommandContext(ctx, "wg-quick", action, configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick %s %s: %w (%s)", action, iface, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func nativeUp(ctx context.Context, iface, configPath string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read wireguard config: %w", err)
	}
	parsed, err := parseConfig(string(raw))
	if err != nil {
		return err
	}
	_ = nativeDown(ctx, iface)
	if err := runIP(ctx, "link", "add", "name", iface, "type", "wireguard"); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = nativeDown(ctx, iface)
		}
	}()
	for _, address := range parsed.Addresses {
		if err := runIP(ctx, "address", "add", address, "dev", iface); err != nil {
			return err
		}
	}
	if mtu, err := strconv.Atoi(parsed.MTU); err == nil && mtu >= 576 && mtu <= 9000 {
		if err := runIP(ctx, "link", "set", "dev", iface, "mtu", strconv.Itoa(mtu)); err != nil {
			return err
		}
	}
	setconfPath := filepath.Join(filepath.Dir(configPath), iface+".setconf")
	if err := os.WriteFile(setconfPath, []byte(wgSetConf(parsed)), 0o600); err != nil {
		return fmt.Errorf("write wg setconf: %w", err)
	}
	if err := runCmd(ctx, "wg", "setconf", iface, setconfPath); err != nil {
		return err
	}
	if err := runIP(ctx, "link", "set", "dev", iface, "up"); err != nil {
		return err
	}
	table := TableFor(iface)
	hasDefault := false
	for _, peer := range parsed.Peers {
		for _, cidr := range peer.AllowedIPs {
			if isDefaultRoute(cidr) {
				hasDefault = true
				continue
			}
			family := "-4"
			if isIPv6CIDR(cidr) {
				family = "-6"
			}
			if err := runIP(ctx, family, "route", "replace", cidr, "dev", iface); err != nil {
				return err
			}
		}
	}
	if hasDefault {
		if err := runIP(ctx, "-4", "route", "replace", "default", "dev", iface, "table", strconv.Itoa(table)); err != nil {
			return err
		}
		_ = runIP(ctx, "-4", "rule", "add", "fwmark", strconv.Itoa(table), "table", strconv.Itoa(table), "pref", strconv.Itoa(table))
	}
	cleanup = false
	return nil
}

func nativeDown(ctx context.Context, iface string) error {
	err := runIP(ctx, "link", "delete", "dev", iface)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "cannot find device") ||
		strings.Contains(strings.ToLower(err.Error()), "does not exist") {
		return nil
	}
	return err
}

func runIP(ctx context.Context, args ...string) error {
	return runCmd(ctx, "ip", args...)
}

func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
