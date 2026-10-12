//go:build windows

package modem

import "testing"

func TestWindowsWWANBindingRejectsAmbiguousAndUnrelatedSerialPorts(t *testing.T) {
	candidates := []Candidate{{VendorID: "2c7c", ATPort: Port{Role: PortRoleAT}}, {VendorID: "1234", ATPort: Port{Role: PortRoleUnknown}}}
	bindSingleWindowsCellularInterface(candidates, "Cellular")
	if candidates[0].NetworkInterface != "Cellular" || candidates[1].NetworkInterface != "" {
		t.Fatal("WWAN bound to unrelated serial device")
	}
	candidates = []Candidate{{VendorID: "2c7c", ATPort: Port{Role: PortRoleAT}}, {VendorID: "2c7c", ATPort: Port{Role: PortRoleAT}}}
	bindSingleWindowsCellularInterface(candidates, "Cellular")
	if candidates[0].NetworkInterface != "" || candidates[1].NetworkInterface != "" {
		t.Fatal("ambiguous WWAN binding was accepted")
	}
}
