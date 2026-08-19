package proxy

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestValidIKESAInitResponseRequiresMatchingResponse(t *testing.T) {
	packet, spi := ikeSAInitProbe(false)
	response := append([]byte(nil), packet...)
	response[8] = 1
	response[18] = 34
	response[19] = 0x20
	if !validIKESAInitResponse(response, spi) {
		t.Fatal("valid IKE_SA_INIT response was rejected")
	}
	natt, nattSPI := ikeSAInitProbe(true)
	natt[4+8] = 1
	natt[4+18] = 34
	natt[4+19] = 0x20
	if !validIKESAInitResponse(natt, nattSPI) {
		t.Fatal("NAT-T framed IKE_SA_INIT response was rejected")
	}
	response[0] ^= 1
	if validIKESAInitResponse(response, spi) {
		t.Fatal("response with mismatched initiator SPI was accepted")
	}
}

func TestSocksUDPDatagramKeepsHostname(t *testing.T) {
	packet, err := socksUDPDatagram("epdg.example.test", 500, []byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if packet[3] != 3 {
		t.Fatalf("expected domain ATYP, got %#v", packet)
	}
	host := string(packet[5 : 5+int(packet[4])])
	if host != "epdg.example.test" {
		t.Fatalf("host = %q", host)
	}
	v4, err := socksUDPDatagram("192.0.2.1", 500, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if v4[3] != 1 || len(v4) != 3+1+4+2+1 {
		t.Fatalf("IPv4 SOCKS UDP packet = %#v", v4)
	}
	v6, err := socksUDPDatagram("2001:db8::1", 4500, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if v6[3] != 4 || len(v6) != 3+1+16+2+1 {
		t.Fatalf("IPv6 SOCKS UDP packet = %#v", v6)
	}
	_ = net.IPv4len
}

func TestProbeSOCKS5EPDGPortsRejectsEmptyHost(t *testing.T) {
	_, err := ProbeSOCKS5EPDGPorts(context.Background(), "127.0.0.1:1080", "", "", "", 0)
	if err == nil {
		t.Fatal("empty ePDG host was accepted")
	}
}

func TestProbeDirectEPDGPortsRejectsEmptyHost(t *testing.T) {
	_, err := ProbeDirectEPDGPorts(context.Background(), "", 0)
	if err == nil {
		t.Fatal("empty ePDG host was accepted")
	}
}

func TestIKESAInitProbeIsARealInitRequest(t *testing.T) {
	packet, spi := ikeSAInitProbe(false)
	if len(packet) < 28+4+128 {
		t.Fatalf("IKE_SA_INIT probe is still a header stub: %d bytes", len(packet))
	}
	if string(packet[:8]) != string(spi[:]) {
		t.Fatal("initiator SPI was not placed in the header")
	}
	if packet[16] != 33 || packet[17] != 0x20 || packet[18] != 34 || packet[19] != 0x08 {
		t.Fatalf("IKE header = %x", packet[16:20])
	}
	if int(binary.BigEndian.Uint32(packet[24:28])) != len(packet) {
		t.Fatalf("encoded length %d != %d", binary.BigEndian.Uint32(packet[24:28]), len(packet))
	}
	natt, _ := ikeSAInitProbe(true)
	if len(natt) != len(packet)+4 || natt[0] != 0 || natt[1] != 0 || natt[2] != 0 || natt[3] != 0 {
		t.Fatalf("NAT-T framing is invalid: %d bytes", len(natt))
	}
}

func TestValidIKESAInitResponseAcceptsZeroResponderSPI(t *testing.T) {
	packet, spi := ikeSAInitProbe(false)
	response := append([]byte(nil), packet...)
	response[19] = 0x20
	if !validIKESAInitResponse(response, spi) {
		t.Fatal("IKE_SA_INIT error/cookie reply with a zero responder SPI was rejected")
	}
}

func TestProbeDirectEPDGPortsReceivesIKEResponse(t *testing.T) {
	responders := map[int]*net.UDPConn{}
	for _, port := range []int{500, 4500} {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			for _, open := range responders {
				_ = open.Close()
			}
			t.Skipf("cannot bind UDP/%d: %v", port, err)
		}
		responders[port] = conn
	}
	defer func() {
		for _, conn := range responders {
			_ = conn.Close()
		}
	}()
	for port, conn := range responders {
		go func(port int, conn *net.UDPConn) {
			_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
			buffer := make([]byte, 4096)
			n, addr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			payload := buffer[:n]
			if port == 4500 && len(payload) >= 4 {
				payload = payload[4:]
			}
			if len(payload) < 28 || payload[18] != 34 {
				return
			}
			reply := append([]byte(nil), payload...)
			reply[8] = 1
			reply[19] = 0x20
			if port == 4500 {
				framed := make([]byte, 4+len(reply))
				copy(framed[4:], reply)
				reply = framed
			}
			_, _ = conn.WriteToUDP(reply, addr)
		}(port, conn)
	}
	result, err := ProbeDirectEPDGPorts(context.Background(), "127.0.0.1", 3*time.Second)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if !result.Port500OK || !result.Port4500OK {
		t.Fatalf("probe result = %+v", result)
	}
}
