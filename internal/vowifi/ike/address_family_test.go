package ike

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"
)

func TestConfigurationAndSelectorsUseSameAddressFamily(t *testing.T) {
	for _, test := range []struct {
		family     string
		attributes []uint16
		ipLengths  []int
	}{
		{"ipv4", []uint16{1, 3, 20, 7}, []int{4}},
		{"ipv6", []uint16{8, 10, 21, 7}, []int{16}},
		{"dual", []uint16{1, 8, 3, 10, 20, 21, 7}, []int{4, 16}},
	} {
		t.Run(test.family, func(t *testing.T) {
			request := buildInitialEAPAuthForFamily(payload{}, payload{}, nil,
				trafficSelectorsForFamily(payloadTSi, test.family), trafficSelectorsForFamily(payloadTSr, test.family), true, test.family)
			cp, err := onePayload(request, payloadCP)
			if err != nil {
				t.Fatal(err)
			}
			var attrs []uint16
			for offset := 4; offset < len(cp.Body); offset += 4 {
				attrs = append(attrs, binary.BigEndian.Uint16(cp.Body[offset:offset+2]))
			}
			if !reflect.DeepEqual(attrs, test.attributes) {
				t.Fatalf("configuration attributes = %v, want %v", attrs, test.attributes)
			}
			for _, kind := range []uint8{payloadTSi, payloadTSr} {
				item, err := onePayload(request, kind)
				if err != nil {
					t.Fatal(err)
				}
				selectors, err := parseTrafficSelectors(item)
				if err != nil {
					t.Fatal(err)
				}
				var lengths []int
				for _, selector := range selectors {
					lengths = append(lengths, len(selector.StartIP))
				}
				if !reflect.DeepEqual(lengths, test.ipLengths) {
					t.Fatalf("selector families = %v, want %v", lengths, test.ipLengths)
				}
			}
		})
	}
}

// Public NIST FIPS 186-2 PRF vector, also used by Android's EAP implementation.
func TestFIPS1862PublishedVector(t *testing.T) {
	seed, _ := hex.DecodeString("bd029bbe7f51960bcf9edb2b61f06f0feb5a38b6")
	expected, _ := hex.DecodeString("2070b3223dba372fde1c0ffc7b2e3b498b2606143c6c18bacb0f6c55babb13788e20d737a3275116")
	if result := fips1862PRF(seed, len(expected)); !bytes.Equal(result, expected) {
		t.Fatalf("FIPS 186-2 output does not match published vector")
	}
}

func TestMinimalSWuRequestOmitsOptionalCFGAndMOBIKE(t *testing.T) {
	request := buildInitialEAPAuthForProfile(payload{Type: payloadIDi}, payload{Type: payloadIDr}, nil,
		trafficSelectorsForFamily(payloadTSi, "ipv4"), trafficSelectorsForFamily(payloadTSr, "ipv4"), true, "ipv4", "minimal")
	var types []uint8
	for _, item := range request {
		types = append(types, item.Type)
	}
	if !reflect.DeepEqual(types, []uint8{payloadIDi, payloadIDr, payloadCP, payloadSA, payloadTSi, payloadTSr, payloadNotify, payloadNotify}) {
		t.Fatalf("minimal SWu payload order = %v", types)
	}
	cp, err := onePayload(request, payloadCP)
	if err != nil {
		t.Fatal(err)
	}
	var attrs []uint16
	for offset := 4; offset < len(cp.Body); offset += 4 {
		attrs = append(attrs, binary.BigEndian.Uint16(cp.Body[offset:offset+2]))
	}
	if !reflect.DeepEqual(attrs, []uint16{1, 3, 20}) {
		t.Fatalf("minimal CFG = %v", attrs)
	}
	if hasNotifyType(request, notifyMOBIKESupported) || !hasNotifyType(request, notifyEAPOnlyAuth) || !hasNotifyType(request, notifyInitialContact) {
		t.Fatal("minimal request lost required capabilities or advertised MOBIKE")
	}
}
