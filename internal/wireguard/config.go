package wireguard

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"vocat/internal/store"
)

const maxConfigBytes = 64 << 10

var (
	interfaceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,14}$`)
	base64KeyPattern     = regexp.MustCompile(`^[A-Za-z0-9+/]{42,44}={0,2}$`)
	reservedInterfaces   = map[string]struct{}{
		"lo": {}, "all": {}, "default": {}, "hostname": {},
		"docker0": {}, "sit0": {}, "tunl0": {}, "ip6tnl0": {},
	}
)

type parsedPeer struct {
	PublicKey           string
	PresharedKey        string
	Endpoint            string
	AllowedIPs          []string
	PersistentKeepalive string
}

type parsedConfig struct {
	Address      string
	Addresses    []string
	PrivateKey   string
	ListenPort   string
	MTU          string
	DNS          []string
	PeerCount    int
	PeerKeys     []string
	Preshared    map[string]string
	Peers        []parsedPeer
}

func ValidateInterfaceName(name string) error {
	return validateInterfaceName(name)
}

func validateInterfaceName(name string) error {
	name = strings.TrimSpace(name)
	if !interfaceNamePattern.MatchString(name) {
		return fmt.Errorf("interface name %q must be 1-15 characters starting with a letter (a-z, 0-9)", name)
	}
	if _, reserved := reservedInterfaces[name]; reserved {
		return fmt.Errorf("interface name %q is reserved", name)
	}
	if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") ||
		strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "cni") {
		return fmt.Errorf("interface name %q collides with a system prefix", name)
	}
	return nil
}

func parseConfig(raw string) (parsedConfig, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if strings.TrimSpace(raw) == "" {
		return parsedConfig{}, fmt.Errorf("WireGuard config is empty")
	}
	if len(raw) > maxConfigBytes {
		return parsedConfig{}, fmt.Errorf("WireGuard config exceeds %d bytes", maxConfigBytes)
	}

	var parsed parsedConfig
	parsed.Preshared = make(map[string]string)
	section := ""
	peerKey := ""
	hasInterface := false
	var currentPeer *parsedPeer
	for lineNumber, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			peerKey = ""
			switch section {
			case "interface":
				if hasInterface {
					return parsedConfig{}, fmt.Errorf("line %d: only one [Interface] section is allowed", lineNumber+1)
				}
				hasInterface = true
			case "peer":
				parsed.PeerCount++
				parsed.Peers = append(parsed.Peers, parsedPeer{})
				currentPeer = &parsed.Peers[len(parsed.Peers)-1]
			default:
				return parsedConfig{}, fmt.Errorf("line %d: unsupported section %q", lineNumber+1, trimmed)
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			return parsedConfig{}, fmt.Errorf("line %d: expected key = value", lineNumber+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch {
		case section == "interface" && strings.EqualFold(key, "PrivateKey"):
			if err := validateKey(value, true); err != nil {
				return parsedConfig{}, fmt.Errorf("Interface PrivateKey: %w", err)
			}
			parsed.PrivateKey = value
		case section == "interface" && strings.EqualFold(key, "Address"):
			addresses := splitCommaList(value)
			if len(addresses) == 0 {
				return parsedConfig{}, fmt.Errorf("Interface Address is required")
			}
			parsed.Addresses = append(parsed.Addresses, addresses...)
			if parsed.Address == "" {
				parsed.Address = addresses[0]
			}
		case section == "interface" && strings.EqualFold(key, "ListenPort"):
			parsed.ListenPort = value
		case section == "interface" && strings.EqualFold(key, "MTU"):
			parsed.MTU = value
		case section == "interface" && strings.EqualFold(key, "DNS"):
			parsed.DNS = append(parsed.DNS, splitCommaList(value)...)
		case section == "peer" && strings.EqualFold(key, "PublicKey"):
			if err := validateKey(value, false); err != nil {
				return parsedConfig{}, fmt.Errorf("Peer PublicKey: %w", err)
			}
			peerKey = value
			parsed.PeerKeys = append(parsed.PeerKeys, value)
			if currentPeer != nil {
				currentPeer.PublicKey = value
			}
		case section == "peer" && strings.EqualFold(key, "PresharedKey"):
			if err := validateKey(value, true); err != nil {
				return parsedConfig{}, fmt.Errorf("Peer PresharedKey: %w", err)
			}
			if peerKey != "" {
				parsed.Preshared[peerKey] = value
			}
			if currentPeer != nil {
				currentPeer.PresharedKey = value
			}
		case section == "peer" && strings.EqualFold(key, "Endpoint"):
			if currentPeer != nil {
				currentPeer.Endpoint = value
			}
		case section == "peer" && strings.EqualFold(key, "AllowedIPs"):
			if currentPeer != nil {
				currentPeer.AllowedIPs = append(currentPeer.AllowedIPs, splitCommaList(value)...)
			}
		case section == "peer" && strings.EqualFold(key, "PersistentKeepalive"):
			if currentPeer != nil {
				currentPeer.PersistentKeepalive = value
			}
		}
	}
	if !hasInterface {
		return parsedConfig{}, fmt.Errorf("config must contain an [Interface] section")
	}
	if parsed.PrivateKey == "" {
		return parsedConfig{}, fmt.Errorf("Interface PrivateKey is required")
	}
	if parsed.Address == "" || len(parsed.Addresses) == 0 {
		return parsedConfig{}, fmt.Errorf("Interface Address is required")
	}
	if parsed.PeerCount < 1 {
		return parsedConfig{}, fmt.Errorf("config must contain at least one [Peer]")
	}
	if len(parsed.PeerKeys) < parsed.PeerCount {
		return parsedConfig{}, fmt.Errorf("every [Peer] needs a PublicKey")
	}
	return parsed, nil
}

func validateKey(value string, allowMasked bool) error {
	if allowMasked && value == store.SecretMask {
		return nil
	}
	if !base64KeyPattern.MatchString(value) {
		return fmt.Errorf("must be a 32-byte base64 key")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("must be a 32-byte base64 key")
	}
	return nil
}

func redactConfig(raw string) string {
	return store.RedactText(raw, store.WireGuardTunnel{Config: raw})
}

func mergeMaskedConfig(submitted, stored string) (string, error) {
	if !strings.Contains(submitted, store.SecretMask) {
		return submitted, nil
	}
	storedParsed, err := parseConfig(stored)
	if err != nil {
		return "", fmt.Errorf("stored tunnel config is invalid: %w", err)
	}
	var builder strings.Builder
	section := ""
	peerKey := ""
	for _, line := range strings.Split(strings.ReplaceAll(submitted, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			peerKey = ""
			builder.WriteString(line)
			builder.WriteByte('\n')
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(value) != store.SecretMask {
			if ok && section == "peer" && strings.EqualFold(strings.TrimSpace(key), "PublicKey") {
				peerKey = strings.TrimSpace(value)
			}
			builder.WriteString(line)
			builder.WriteByte('\n')
			continue
		}
		key = strings.TrimSpace(key)
		replacement := store.SecretMask
		switch {
		case section == "interface" && strings.EqualFold(key, "PrivateKey"):
			replacement = storedParsed.PrivateKey
		case section == "peer" && strings.EqualFold(key, "PresharedKey"):
			if secret, ok := storedParsed.Preshared[peerKey]; ok {
				replacement = secret
			}
		}
		if replacement == store.SecretMask {
			return "", fmt.Errorf("cannot restore masked %s; resubmit the full config", key)
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		fmt.Fprintf(&builder, "%s%s = %s\n", indent, key, replacement)
	}
	return strings.TrimSuffix(builder.String(), "\n") + "\n", nil
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isDefaultRoute(cidr string) bool {
	switch strings.TrimSpace(cidr) {
	case "0.0.0.0/0", "::/0":
		return true
	default:
		return false
	}
}

func isIPv6CIDR(cidr string) bool {
	return strings.Contains(cidr, ":")
}

func TableFor(iface string) int {
	sum := uint32(2166136261)
	for _, b := range []byte(iface) {
		sum ^= uint32(b)
		sum *= 16777619
	}
	return 51000 + int(sum%900)
}

func nextInterfaceName(existing []string) string {
	used := make(map[string]struct{}, len(existing))
	for _, name := range existing {
		used[strings.TrimSpace(name)] = struct{}{}
	}
	for index := 0; index < 100; index++ {
		candidate := fmt.Sprintf("halo%d", index)
		if _, taken := used[candidate]; !taken {
			return candidate
		}
	}
	return "halo99"
}
