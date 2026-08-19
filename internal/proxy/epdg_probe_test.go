package proxy

import (
	"context"
	"net"
	"testing"
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
