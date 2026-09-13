//go:build windows && (amd64 || arm64)

package ims

import (
	"encoding/binary"
	"net"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsWFPABISizes(t *testing.T) {
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"FWP_BYTE_BLOB", unsafe.Sizeof(wfpByteBlob{}), 16},
		{"FWPM_FILTER_CONDITION0", unsafe.Sizeof(wfpFilterCondition0{}), 40},
		{"FWPM_FILTER0", unsafe.Sizeof(wfpFilter0{}), 200},
		{"FWPM_SESSION0", unsafe.Sizeof(wfpSession0{}), 72},
		{"IPSEC_TRAFFIC1", unsafe.Sizeof(ipsecTraffic1{}), 72},
		{"IPSEC_GETSPI1", unsafe.Sizeof(ipsecGetSPI1{}), 96},
		{"IPSEC_SA_AUTH_INFORMATION0", unsafe.Sizeof(ipsecSAAuthInformation0{}), 32},
		{"IPSEC_SA_AUTH_AND_CIPHER_INFORMATION0", unsafe.Sizeof(ipsecSAAuthAndCipherInformation0{}), 64},
		{"IPSEC_SA_BUNDLE0", unsafe.Sizeof(ipsecSABundle0{}), 88},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("%s size = %d, want %d", test.name, test.got, test.want)
		}
	}
}

func TestWindowsWFPIPv4AddressUsesInetAddrLayout(t *testing.T) {
	version, value, address6, err := wfpAddressValue(net.ParseIP("192.0.2.10"))
	if err != nil {
		t.Fatal(err)
	}
	if version != fwpIPVersionV4 || value.type_ != wfpUint32 || address6 != nil {
		t.Fatalf("unexpected WFP address result: version=%d type=%d v6=%v", version, value.type_, address6)
	}
	want := binary.LittleEndian.Uint32([]byte{192, 0, 2, 10})
	if uint32(value.value) != want {
		t.Fatalf("WFP IPv4 value = %#x, want %#x", value.value, want)
	}
}

func TestWindowsWFPTrafficPreservesIPv6Bytes(t *testing.T) {
	local := net.ParseIP("2001:db8::10")
	remote := net.ParseIP("2001:db8::20")
	traffic, version, err := makeIPSecTraffic(local, remote, 42)
	if err != nil {
		t.Fatal(err)
	}
	if version != fwpIPVersionV6 || traffic.filterOrPolicy != 42 {
		t.Fatalf("unexpected traffic header: version=%d filter=%d", version, traffic.filterOrPolicy)
	}
	if got := net.IP(traffic.localAddress[:]).String(); got != "2001:db8::10" {
		t.Fatalf("local address = %s", got)
	}
	if got := net.IP(traffic.remoteAddress[:]).String(); got != "2001:db8::20" {
		t.Fatalf("remote address = %s", got)
	}
}

func TestWindowsWFPManualSAProceduresAreAvailable(t *testing.T) {
	for _, procedure := range []*windows.LazyProc{
		procFwpmEngineOpen0,
		procFwpmEngineClose0,
		procFwpmFilterAdd0,
		procFwpmFilterDeleteByID0,
		procIPSecSaContextCreate1,
		procIPSecSaContextSetSPI0,
		procIPSecSaContextAddInbound0,
		procIPSecSaContextAddOutbound0,
		procIPSecSaContextDeleteByID0,
	} {
		if err := procedure.Find(); err != nil {
			t.Errorf("%s unavailable: %v", procedure.Name, err)
		}
	}
}
