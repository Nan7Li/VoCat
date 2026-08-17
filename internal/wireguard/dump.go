package wireguard

import (
	"strconv"
	"strings"
	"time"
)

func parseWGDump(raw string) InterfaceInfo {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return InterfaceInfo{}
	}
	fields := strings.Split(lines[0], "\t")
	info := InterfaceInfo{Running: true}
	if len(fields) >= 3 {
		info.PublicKey = fields[1]
		info.ListenPort, _ = strconv.Atoi(fields[2])
	}
	var latest int64
	for _, line := range lines[1:] {
		peer := strings.Split(line, "\t")
		if len(peer) < 7 {
			continue
		}
		info.Peers++
		if info.Endpoint == "" && peer[2] != "(none)" && peer[2] != "" {
			info.Endpoint = peer[2]
		}
		if handshake, _ := strconv.ParseInt(peer[4], 10, 64); handshake > latest {
			latest = handshake
		}
		rx, _ := strconv.ParseInt(peer[5], 10, 64)
		tx, _ := strconv.ParseInt(peer[6], 10, 64)
		info.TransferRX += rx
		info.TransferTX += tx
	}
	if latest > 0 {
		info.LatestHandshake = time.Unix(latest, 0).UTC().Format(time.RFC3339)
	}
	return info
}
