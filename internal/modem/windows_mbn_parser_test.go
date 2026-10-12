package modem

import "testing"

func TestParseWindowsMBNInterfaceNames(t *testing.T) {
	output := `There are 2 interfaces on the system:
	Name                   : Cellular
	Description            : Quectel Mobile Broadband Adapter

	名称                   : Cellular 2
	Description            : another modem
	Name                   : Cellular`

	got := parseWindowsMBNInterfaceNames(output)
	if len(got) != 2 || got[0] != "Cellular" || got[1] != "Cellular 2" {
		t.Fatalf("interface names = %#v", got)
	}
}

func TestParseWindowsMBNInterfaceNamesIgnoresUnrelatedFields(t *testing.T) {
	output := `Name: Cellular
	Interface name: Cellular
	GUID: {00000000-0000-0000-0000-000000000000}
	State: Connected`
	got := parseWindowsMBNInterfaceNames(output)
	if len(got) != 1 || got[0] != "Cellular" {
		t.Fatalf("interface names = %#v", got)
	}
}

func TestParseWindowsMBNInterfaceNamesSupportsLocalizedDisconnectedOutput(t *testing.T) {
	output := `There is 1 interface on the system:

    Name                   : 手机网络
    Description            : Quectel Wireless Ethernet Adapter
    State                  : Not connected`

	got := parseWindowsMBNInterfaceNames(output)
	if len(got) != 1 || got[0] != "手机网络" {
		t.Fatalf("interface names = %#v", got)
	}
}
