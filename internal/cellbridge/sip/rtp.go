// Adapted from CellBridge gateway/internal/sip/media.go
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: RTP is parsed with the standard library. The peer address
// is the authenticated SIP source plus the SDP port, never an address taken
// from the SDP connection line when that address is somebody else. Sequence
// numbers advance and old or repeated packets are dropped.

package sip

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
)

const (
	rtpHeaderLen = 12
	rtpMaxAudio  = 320
	rtpFrame     = 160
)

func parseRTP(packet []byte) (pt byte, seq uint16, payload []byte, ok bool) {
	if len(packet) < rtpHeaderLen || packet[0]>>6 != 2 {
		return 0, 0, nil, false
	}
	padding := packet[0]&0x20 != 0
	extension := packet[0]&0x10 != 0
	csrc := int(packet[0] & 0x0f)
	pt = packet[1] & 0x7f
	seq = binary.BigEndian.Uint16(packet[2:4])
	header := rtpHeaderLen + csrc*4
	if len(packet) < header {
		return 0, 0, nil, false
	}
	if extension {
		if len(packet) < header+4 {
			return 0, 0, nil, false
		}
		words := int(binary.BigEndian.Uint16(packet[header+2 : header+4]))
		header += 4 + words*4
		if header < 0 || len(packet) < header {
			return 0, 0, nil, false
		}
	}
	end := len(packet)
	if padding {
		if end == 0 {
			return 0, 0, nil, false
		}
		pad := int(packet[end-1])
		if pad < 1 || header+pad > end {
			return 0, 0, nil, false
		}
		end -= pad
	}
	if end < header {
		return 0, 0, nil, false
	}
	return pt, seq, packet[header:end], true
}

func buildRTP(seq uint16, timestamp, ssrc uint32, payload []byte) []byte {
	packet := make([]byte, rtpHeaderLen+len(payload))
	packet[0] = 0x80
	packet[1] = 0
	binary.BigEndian.PutUint16(packet[2:4], seq)
	binary.BigEndian.PutUint32(packet[4:8], timestamp)
	binary.BigEndian.PutUint32(packet[8:12], ssrc)
	copy(packet[rtpHeaderLen:], payload)
	return packet
}

// validateAudioSDP accepts an offer or answer that contains PCMU and a port
// aimed at the authenticated SIP source. 0.0.0.0 means "use that source".
// Any other address is refused so media is not sent to a third party.
func validateAudioSDP(body string, source net.IP) (net.IP, int, error) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var sessionIP, mediaIP net.IP
	var port int
	var formats []string
	mappings := map[int]string{}
	inAudio := false
	sawAudio := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m="):
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			inAudio = len(fields) >= 4 && strings.EqualFold(fields[0], "audio") &&
				(strings.EqualFold(fields[2], "RTP/AVP") || strings.EqualFold(fields[2], "RTP/AVPF"))
			if inAudio {
				sawAudio = true
				parsed, err := strconv.Atoi(strings.Split(fields[1], "/")[0])
				if err != nil {
					return nil, 0, errBadSDP
				}
				port = parsed
				formats = append([]string(nil), fields[3:]...)
			}
		case strings.HasPrefix(line, "c="):
			fields := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(fields) < 3 {
				continue
			}
			ip := net.ParseIP(strings.Split(fields[2], "/")[0])
			if inAudio {
				mediaIP = ip
			} else if !sawAudio {
				sessionIP = ip
			}
		case inAudio && strings.HasPrefix(strings.ToLower(line), "a=rtpmap:"):
			fields := strings.Fields(strings.TrimPrefix(line[len("a="):], "rtpmap:"))
			if len(fields) >= 2 {
				pt, err := strconv.Atoi(fields[0])
				if err == nil {
					mappings[pt] = strings.ToUpper(fields[1])
				}
			}
		}
	}
	if !sawAudio || port == 0 || len(formats) == 0 {
		return nil, 0, errBadSDP
	}
	if port < 1 || port > 65535 {
		return nil, 0, errBadSDP
	}
	if !offerHasPCMU(formats, mappings) {
		return nil, 0, errBadSDP
	}
	ip := mediaIP
	if ip == nil {
		ip = sessionIP
	}
	if ip == nil {
		return nil, 0, errBadSDP
	}
	pinned, ok := pinMediaIP(ip, source)
	if !ok {
		return nil, 0, errBadSDP
	}
	return pinned, port, nil
}

func offerHasPCMU(formats []string, mappings map[int]string) bool {
	for _, format := range formats {
		pt, err := strconv.Atoi(format)
		if err != nil || pt != 0 {
			continue
		}
		// Static payload 0 is PCMU/8000/1. An explicit rtpmap must say the
		// same thing: dynamic payload types, 16 kHz, and stereo are not PCMU.
		spec := mappings[pt]
		if spec == "" || pcmu8k(spec) {
			return true
		}
	}
	return false
}

func pcmu8k(spec string) bool {
	parts := strings.Split(spec, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "PCMU" || parts[1] != "8000" {
		return false
	}
	return len(parts) == 2 || parts[2] == "1"
}

func pinMediaIP(declared, source net.IP) (net.IP, bool) {
	if declared == nil || source == nil || declared.IsMulticast() || declared.Equal(net.IPv4bcast) {
		return nil, false
	}
	source = normalizeIP(source)
	if declared.IsUnspecified() {
		if source == nil {
			return nil, false
		}
		return append(net.IP(nil), source...), true
	}
	if !sameIP(declared, source) {
		return nil, false
	}
	return append(net.IP(nil), source...), true
}

func sameIP(a, b net.IP) bool {
	a = normalizeIP(a)
	b = normalizeIP(b)
	if a == nil || b == nil {
		return false
	}
	return a.Equal(b)
}

func normalizeIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func cloneIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	out := make(net.IP, len(ip))
	copy(out, ip)
	return out
}
