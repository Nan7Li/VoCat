package wireguard

import (
	"fmt"
	"strings"
)

func wgSetConf(parsed parsedConfig) string {
	var builder strings.Builder
	builder.WriteString("[Interface]\n")
	fmt.Fprintf(&builder, "PrivateKey = %s\n", parsed.PrivateKey)
	if parsed.ListenPort != "" {
		fmt.Fprintf(&builder, "ListenPort = %s\n", parsed.ListenPort)
	}
	for _, peer := range parsed.Peers {
		builder.WriteString("\n[Peer]\n")
		fmt.Fprintf(&builder, "PublicKey = %s\n", peer.PublicKey)
		if peer.PresharedKey != "" && peer.PresharedKey != "********" {
			fmt.Fprintf(&builder, "PresharedKey = %s\n", peer.PresharedKey)
		}
		if peer.Endpoint != "" {
			fmt.Fprintf(&builder, "Endpoint = %s\n", peer.Endpoint)
		}
		if len(peer.AllowedIPs) > 0 {
			fmt.Fprintf(&builder, "AllowedIPs = %s\n", strings.Join(peer.AllowedIPs, ", "))
		}
		if peer.PersistentKeepalive != "" {
			fmt.Fprintf(&builder, "PersistentKeepalive = %s\n", peer.PersistentKeepalive)
		}
	}
	return builder.String()
}
