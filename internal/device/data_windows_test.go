//go:build windows

package device

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestWindowsMBNProfileXMLUsesSIMAndIPSettings(t *testing.T) {
	raw, err := windowsMBNProfileXML(
		"internet.example",
		"IPV4V6",
		"user",
		"secret",
		"PAP_OR_CHAP",
		"8901000000000000001",
	)
	if err != nil {
		t.Fatalf("windowsMBNProfileXML() error = %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`xmlns="` + windowsMBNProfileNamespace + `"`,
		"<SimIccID>8901000000000000001</SimIccID>",
		"<AccessString>internet.example</AccessString>",
		"<AuthProtocol>AutoSelection</AuthProtocol>",
		"<IPType>IPv4v6</IPType>",
		"<UserName>user</UserName>",
		"<Password>secret</Password>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("profile XML does not contain %q: %s", want, text)
		}
	}
	var profile windowsMBNProfile
	if err := xml.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("unmarshal profile XML: %v", err)
	}
	if profile.SimIccID != "8901000000000000001" || profile.Context.IPType != "IPv4v6" {
		t.Fatalf("decoded profile = %#v", profile)
	}
}

func TestWindowsMBNArgumentDoesNotAddShellQuotes(t *testing.T) {
	if got := windowsMBNArgument(`Cellular 2`); got != `Cellular 2` {
		t.Fatalf("windowsMBNArgument() = %q", got)
	}
}

