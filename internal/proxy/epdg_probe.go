package proxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// EPDGProbeResult records an actual IKE-shaped UDP response from both
// IKE/NAT-T ports through the selected SOCKS5 relay. A proxy that can relay
// UDP/53 is not automatically a usable VoWiFi route.
type EPDGProbeResult struct {
	Port500OK  bool  `json:"port_500_ok"`
	Port4500OK bool  `json:"port_4500_ok"`
	RTT500MS   int64 `json:"rtt_500_ms,omitempty"`
	RTT4500MS  int64 `json:"rtt_4500_ms,omitempty"`
}

// ProbeSOCKS5EPDGPorts sends a minimal IKE_SA_INIT-shaped datagram to UDP/500
// and a NAT-T framed copy to UDP/4500 through the SOCKS5 UDP ASSOCIATE. The
// ePDG hostname is placed in the SOCKS datagram so the host resolver never
// sees a country-identifying 3GPP name.
func ProbeSOCKS5EPDGPorts(ctx context.Context, address, username, password, host string, timeout time.Duration) (EPDGProbeResult, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t\r\n/:") || len(host) > 253 {
		return EPDGProbeResult{}, errors.New("ePDG host is invalid")
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	control, relay, err := openUDPRelay(ctx, address, username, password)
	if err != nil {
		return EPDGProbeResult{}, err
	}
	defer control.Close()
	defer relay.Close()

	result := EPDGProbeResult{}
	perAttempt := timeout / 2
	if perAttempt < 2*time.Second {
		perAttempt = 2 * time.Second
	}
	for _, item := range []struct {
		port int
		natt bool
		ok   *bool
		rtt  *int64
	}{
		{500, false, &result.Port500OK, &result.RTT500MS},
		{4500, true, &result.Port4500OK, &result.RTT4500MS},
	} {
		started := time.Now()
		packet, initiatorSPI := ikeSAInitProbe(item.natt)
		datagram, err := socksUDPDatagram(host, item.port, packet)
		if err != nil {
			return result, err
		}
		if _, err := relay.Write(datagram); err != nil {
			continue
		}
		_ = relay.SetReadDeadline(time.Now().Add(perAttempt))
		buffer := make([]byte, 4096)
		for {
			if err := ctx.Err(); err != nil {
				break
			}
			n, readErr := relay.Read(buffer)
			if readErr != nil {
				break
			}
			if n < 4 {
				continue
			}
			payload, parseErr := parseSOCKSUDPDatagram(buffer[:n])
			if parseErr == nil && validIKESAInitResponse(payload, initiatorSPI) {
				*item.ok = true
				*item.rtt = time.Since(started).Milliseconds()
				break
			}
		}
	}
	if !result.Port500OK || !result.Port4500OK {
		return result, errors.New("ePDG UDP/500 and UDP/4500 did not both respond")
	}
	return result, nil
}

// ProbeDirectEPDGPorts sends the same IKE-shaped datagrams to UDP/500 and
// UDP/4500 on the host's default route (WireGuard, policy routing, or
// unproxied WAN). Use this when VoWiFi is not bound to a SOCKS5 upstream.
func ProbeDirectEPDGPorts(ctx context.Context, host string, timeout time.Duration) (EPDGProbeResult, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t\r\n/") || len(host) > 253 {
		return EPDGProbeResult{}, errors.New("ePDG host is invalid")
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return EPDGProbeResult{}, err
	}
	var target net.IP
	for _, item := range ips {
		if item.IP.To4() != nil {
			target = item.IP.To4()
			break
		}
	}
	if target == nil && len(ips) > 0 {
		target = ips[0].IP
	}
	if target == nil {
		return EPDGProbeResult{}, errors.New("ePDG host did not resolve")
	}

	result := EPDGProbeResult{}
	perAttempt := timeout / 2
	if perAttempt < 2*time.Second {
		perAttempt = 2 * time.Second
	}
	for _, item := range []struct {
		port int
		natt bool
		ok   *bool
		rtt  *int64
	}{
		{500, false, &result.Port500OK, &result.RTT500MS},
		{4500, true, &result.Port4500OK, &result.RTT4500MS},
	} {
		started := time.Now()
		dialer := net.Dialer{}
		conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(target.String(), strconv.Itoa(item.port)))
		if err != nil {
			continue
		}
		packet, initiatorSPI := ikeSAInitProbe(item.natt)
		_, _ = conn.Write(packet)
		_ = conn.SetReadDeadline(time.Now().Add(perAttempt))
		buffer := make([]byte, 4096)
		for {
			if err := ctx.Err(); err != nil {
				break
			}
			n, readErr := conn.Read(buffer)
			if readErr != nil {
				break
			}
			if validIKESAInitResponse(buffer[:n], initiatorSPI) {
				*item.ok = true
				*item.rtt = time.Since(started).Milliseconds()
				break
			}
		}
		_ = conn.Close()
	}
	if !result.Port500OK || !result.Port4500OK {
		return result, errors.New("ePDG UDP/500 and UDP/4500 did not both respond")
	}
	return result, nil
}

func openUDPRelay(ctx context.Context, address, username, password string) (net.Conn, *net.UDPConn, error) {
	control, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (net.Conn, *net.UDPConn, error) { control.Close(); return nil, nil, err }
	methods := []byte{0}
	if username != "" {
		methods = append(methods, 2)
	}
	if _, err := control.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return fail(err)
	}
	choice := []byte{0, 0}
	if _, err := io.ReadFull(control, choice); err != nil {
		return fail(err)
	}
	if choice[1] == 2 {
		if len(username) > 255 || len(password) > 255 {
			return fail(errors.New("proxy credentials are too long"))
		}
		auth := []byte{1, byte(len(username))}
		auth = append(auth, username...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if _, err := control.Write(auth); err != nil {
			return fail(err)
		}
		response := []byte{0, 0}
		if _, err := io.ReadFull(control, response); err != nil || response[1] != 0 {
			return fail(errors.New("proxy authentication failed"))
		}
	} else if choice[1] != 0 {
		return fail(errors.New("proxy does not support required SOCKS5 authentication"))
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return fail(err)
	}
	reader := bufio.NewReader(control)
	header := []byte{0, 0, 0, 0}
	if _, err := io.ReadFull(reader, header); err != nil || header[1] != 0 {
		return fail(errors.New("SOCKS5 UDP ASSOCIATE rejected"))
	}
	host, err := readSOCKSAddress(reader, header[3])
	if err != nil {
		return fail(err)
	}
	portBytes := []byte{0, 0}
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return fail(err)
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	if host == "0.0.0.0" || host == "::" {
		host, _, _ = net.SplitHostPort(control.RemoteAddr().String())
	}
	relayIP := net.ParseIP(host)
	if relayIP == nil {
		resolved, resolveErr := net.ResolveIPAddr("ip", host)
		if resolveErr != nil {
			return fail(resolveErr)
		}
		relayIP = resolved.IP
	}
	relay, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: relayIP, Port: port})
	if err != nil {
		return fail(err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = relay.SetDeadline(deadline)
	}
	return control, relay, nil
}

func ikeSAInitProbe(natt bool) ([]byte, [8]byte) {
	packet := make([]byte, 28)
	var initiatorSPI [8]byte
	_, _ = rand.Read(initiatorSPI[:])
	copy(packet[:8], initiatorSPI[:])
	packet[17] = 0x20
	packet[18] = 34
	packet[19] = 0x08
	binary.BigEndian.PutUint32(packet[24:], 28)
	if natt {
		framed := make([]byte, 4+len(packet))
		copy(framed[4:], packet)
		return framed, initiatorSPI
	}
	return packet, initiatorSPI
}

func socksUDPDatagram(host string, port int, payload []byte) ([]byte, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("ePDG UDP port is invalid")
	}
	if ip := net.ParseIP(host); ip != nil {
		return buildSOCKSUDPDatagram(&net.UDPAddr{IP: ip, Port: port}, payload)
	}
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 255 {
		return nil, errors.New("ePDG host is invalid")
	}
	packet := []byte{0, 0, 0, 3, byte(len(host))}
	packet = append(packet, host...)
	packet = append(packet, byte(port>>8), byte(port))
	return append(packet, payload...), nil
}

func validIKESAInitResponse(payload []byte, initiatorSPI [8]byte) bool {
	if len(payload) >= 4 && payload[0] == 0 && payload[1] == 0 && payload[2] == 0 && payload[3] == 0 {
		payload = payload[4:]
	}
	if len(payload) < 28 || string(payload[:8]) != string(initiatorSPI[:]) {
		return false
	}
	// IKEv2 response: version 2.x, IKE_SA_INIT exchange, response flag,
	// and a non-zero responder SPI. This rejects unrelated UDP traffic.
	if payload[17]>>4 != 2 || payload[18] != 34 || payload[19]&0x20 == 0 {
		return false
	}
	for _, value := range payload[8:16] {
		if value != 0 {
			return true
		}
	}
	return false
}
