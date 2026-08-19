package proxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
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

// ProbeSOCKS5EPDGPorts sends a real IKE_SA_INIT to UDP/500 and a NAT-T
// framed copy to UDP/4500 through the SOCKS5 UDP ASSOCIATE. The ePDG
// hostname is placed in the SOCKS datagram so the host resolver never
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
		deadline := time.Now().Add(perAttempt)
		_ = relay.SetReadDeadline(deadline)
		if _, err := relay.Write(datagram); err != nil {
			continue
		}
		buffer := make([]byte, 4096)
		retransmitted := false
		for {
			if err := ctx.Err(); err != nil {
				break
			}
			n, readErr := relay.Read(buffer)
			if readErr != nil {
				if !retransmitted && time.Now().Before(deadline) && ctx.Err() == nil {
					_, _ = relay.Write(datagram)
					retransmitted = true
					continue
				}
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

// ProbeDirectEPDGPorts sends the same IKE_SA_INIT datagrams to UDP/500 and
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
	targets, err := resolveEPDGProbeIPs(ctx, host)
	if err != nil {
		return EPDGProbeResult{}, err
	}

	result := EPDGProbeResult{}
	perAttempt := timeout / 2
	if perAttempt < 2*time.Second {
		perAttempt = 2 * time.Second
	}
	if ok, rtt := probeDirectPort(ctx, targets, 500, false, perAttempt); ok {
		result.Port500OK = true
		result.RTT500MS = rtt
	}
	if ok, rtt := probeDirectPort(ctx, targets, 4500, true, perAttempt); ok {
		result.Port4500OK = true
		result.RTT4500MS = rtt
	}
	if !result.Port500OK || !result.Port4500OK {
		return result, errors.New("ePDG UDP/500 and UDP/4500 did not both respond")
	}
	return result, nil
}

func resolveEPDGProbeIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return []net.IP{v4}, nil
		}
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var v4, v6 []net.IP
	for _, item := range ips {
		if item.IP == nil {
			continue
		}
		if ip := item.IP.To4(); ip != nil {
			v4 = appendUniqueIP(v4, ip)
			continue
		}
		v6 = appendUniqueIP(v6, item.IP)
	}
	targets := append(v4, v6...)
	if len(targets) == 0 {
		return nil, errors.New("ePDG host did not resolve")
	}
	return targets, nil
}

func appendUniqueIP(dst []net.IP, ip net.IP) []net.IP {
	for _, existing := range dst {
		if existing.Equal(ip) {
			return dst
		}
	}
	return append(dst, append(net.IP(nil), ip...))
}

func probeDirectPort(ctx context.Context, targets []net.IP, port int, natt bool, timeout time.Duration) (bool, int64) {
	if len(targets) == 0 {
		return false, 0
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		conn, err = net.ListenUDP("udp", &net.UDPAddr{})
	}
	if err != nil {
		return false, 0
	}
	defer conn.Close()

	packet, initiatorSPI := ikeSAInitProbe(natt)
	started := time.Now()
	deadline := started.Add(timeout)
	send := func() {
		for _, ip := range targets {
			_, _ = conn.WriteToUDP(packet, &net.UDPAddr{IP: ip, Port: port})
		}
	}
	send()
	buffer := make([]byte, 4096)
	retransmitted := false
	for {
		if err := ctx.Err(); err != nil {
			return false, 0
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, 0
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		n, _, readErr := conn.ReadFromUDP(buffer)
		if readErr != nil {
			if !retransmitted && time.Now().Before(deadline) && ctx.Err() == nil {
				send()
				retransmitted = true
				continue
			}
			return false, 0
		}
		if validIKESAInitResponse(buffer[:n], initiatorSPI) {
			return true, time.Since(started).Milliseconds()
		}
	}
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

// RFC 2409 Group 2 (1024-bit MODP). Vodafone ePDG negotiates this group.
const modp1024PrimeHex = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
	"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
	"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381" +
	"FFFFFFFFFFFFFFFF"

func ikeSAInitProbe(natt bool) ([]byte, [8]byte) {
	var initiatorSPI [8]byte
	_, _ = rand.Read(initiatorSPI[:])
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		nonce = bytesRepeat(0x11, 32)
	}
	public := dhGroup2Public()
	sa := ikeProbeSA()
	ke := make([]byte, 4+len(public))
	binary.BigEndian.PutUint16(ke[0:2], 2) // DH group 2
	copy(ke[4:], public)

	// SA (33) -> KE (34) -> Nonce (40)
	body := appendIKEPayload(34, sa)
	body = append(body, appendIKEPayload(40, ke)...)
	body = append(body, appendIKEPayload(0, nonce)...)

	packet := make([]byte, 28+len(body))
	copy(packet[:8], initiatorSPI[:])
	packet[16] = 33
	packet[17] = 0x20
	packet[18] = 34
	packet[19] = 0x08
	binary.BigEndian.PutUint32(packet[24:], uint32(len(packet)))
	copy(packet[28:], body)
	if natt {
		framed := make([]byte, 4+len(packet))
		copy(framed[4:], packet)
		return framed, initiatorSPI
	}
	return packet, initiatorSPI
}

func bytesRepeat(value byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = value
	}
	return out
}

func appendIKEPayload(next uint8, body []byte) []byte {
	packet := make([]byte, 4+len(body))
	packet[0] = next
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[4:], body)
	return packet
}

func ikeProbeSA() []byte {
	// One IKE proposal: AES-CBC-128, PRF-HMAC-SHA1, AUTH-HMAC-SHA1-96, DH2.
	// Transform attribute 0x800e = key length, value 128.
	encr := []byte{3, 0, 0, 12, 1, 0, 0, 12, 0x80, 0x0e, 0x00, 0x80}
	prf := []byte{3, 0, 0, 8, 2, 0, 0, 2}
	integ := []byte{3, 0, 0, 8, 3, 0, 0, 2}
	dh := []byte{0, 0, 0, 8, 4, 0, 0, 2}
	transforms := append(append(append(encr, prf...), integ...), dh...)
	proposal := make([]byte, 8+len(transforms))
	binary.BigEndian.PutUint16(proposal[2:4], uint16(len(proposal)))
	proposal[4] = 1
	proposal[5] = 1
	proposal[7] = 4
	copy(proposal[8:], transforms)
	return proposal
}

func dhGroup2Public() []byte {
	primeBytes, err := hex.DecodeString(modp1024PrimeHex)
	if err != nil || len(primeBytes) != 128 {
		return append(bytesRepeat(0, 127), 4)
	}
	prime := new(big.Int).SetBytes(primeBytes)
	sample := make([]byte, 128)
	if _, err := io.ReadFull(rand.Reader, sample); err != nil {
		return append(bytesRepeat(0, 127), 4)
	}
	private := new(big.Int).SetBytes(sample)
	private.Mod(private, new(big.Int).Sub(prime, big.NewInt(3)))
	private.Add(private, big.NewInt(2))
	public := new(big.Int).Exp(big.NewInt(2), private, prime)
	return public.FillBytes(make([]byte, 128))
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
	// IKEv2 IKE_SA_INIT reply. A zero responder SPI is still a reply: COOKIE
	// and some error notifies prove the ePDG is reachable.
	if payload[17]>>4 != 2 || payload[18] != 34 || payload[19]&0x20 == 0 {
		return false
	}
	encoded := binary.BigEndian.Uint32(payload[24:28])
	if encoded != 0 && (encoded < 28 || int(encoded) > len(payload)) {
		return false
	}
	return true
}
