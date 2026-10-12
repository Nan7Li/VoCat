//go:build windows && (amd64 || arm64)

package ike

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestWindowsAddressReadinessWaitsForUsableAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	probes := 0
	err := waitForWindowsAddressReadiness(ctx, func() (bool, error) { probes++; return probes >= 2, nil })
	if err != nil || probes != 2 {
		t.Fatalf("address readiness returned before usable address: probes=%d err=%v", probes, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := waitForWindowsAddressReadiness(ctx, func() (bool, error) { return false, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing address did not respect deadline: %v", err)
	}
	duplicate := errors.New("duplicate address")
	if err := waitForWindowsAddressReadiness(context.Background(), func() (bool, error) { return false, duplicate }); !errors.Is(err, duplicate) {
		t.Fatalf("duplicate address was ignored: %v", err)
	}
}

func TestWindowsAddressReadinessFindsOnlyRequestedInterface(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range interfaces {
		if item.Flags&net.FlagLoopback == 0 {
			continue
		}
		ready, err := windowsInnerAddressesReady(uint32(item.Index), []net.IP{net.ParseIP("127.0.0.1")})
		if err != nil || !ready {
			t.Fatalf("native address lookup failed for loopback: ready=%v err=%v", ready, err)
		}
		ready, err = windowsInnerAddressesReady(uint32(item.Index), []net.IP{net.ParseIP("192.0.2.254")})
		if err != nil || ready {
			t.Fatalf("native lookup accepted an unassigned address: ready=%v err=%v", ready, err)
		}
		return
	}
	t.Fatal("no Windows loopback interface found")
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

func TestWindowsIMSServiceRoutesOverrideCompetingDefaultRoutes(t *testing.T) {
	config := ChildSAConfig{InnerLocalIPv4: net.ParseIP("192.0.2.10"),
		PCSCF:              []net.IP{net.ParseIP("198.51.100.10"), net.ParseIP("198.51.100.10"), net.ParseIP("2001:db8::10")},
		DNS:                []net.IP{net.ParseIP("198.51.100.53"), net.ParseIP("203.0.113.53")},
		ResponderSelectors: []trafficSelector{{StartIP: net.ParseIP("198.51.100.0"), EndIP: net.ParseIP("198.51.100.255")}}}
	routes := windowsIMSServiceRoutes(config, []string{"0.0.0.0/0"})
	want := []string{"0.0.0.0/0", "198.51.100.10/32", "198.51.100.53/32"}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("IMS service routes = %v, want %v", routes, want)
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
