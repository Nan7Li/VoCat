//go:build windows && (amd64 || arm64)

package ike

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestWindowsChildSAInstallerRejectsIncompleteConfig(t *testing.T) {
	handle, err := defaultChildSAInstaller().Install(context.Background(), ChildSAConfig{})
	if err == nil || handle != nil {
		t.Fatalf("Windows installer returned handle=%v err=%v; want explicit failure", handle, err)
	}
	message := strings.ToLower(err.Error())
	for _, required := range []string{"windows", "child_sa", "outer", "requires"} {
		if !strings.Contains(message, required) {
			t.Fatalf("Windows installer error %q does not mention %q", err, required)
		}
	}
}

func TestWindowsSelectorCIDRsAreCanonical(t *testing.T) {
	selectors := []trafficSelector{{
		StartIP: net.IPv4(192, 0, 2, 1),
		EndIP:   net.IPv4(192, 0, 2, 3),
	}}
	routes, err := windowsSelectorCIDRs(selectors)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.1/32", "192.0.2.2/31"}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}
}

func TestWindowsSelectorCIDRsSupportsIPv6Ranges(t *testing.T) {
	selectors := []trafficSelector{{
		StartIP: net.ParseIP("2001:db8::"),
		EndIP:   net.ParseIP("2001:db8::3"),
	}}
	routes, err := windowsSelectorCIDRs(selectors)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2001:db8::/126"}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}
}
