package sip

import (
	"net"
	"testing"
)

func TestProxyTUNSDPUsesAuthenticatedTailscalePeer(t *testing.T) {
	for _, tc := range []struct {
		name, declared, peer string
		accept               bool
	}{
		{"observed iPhone offer", "198.18.0.1", "100.100.100.2", true},
		{"upper TUN boundary", "198.19.255.255", "100.64.0.1", true},
		{"mapped IPv4 peer", "198.18.0.1", "::ffff:100.100.100.2", true},
		{"public third party", "203.0.113.9", "100.100.100.2", false},
		{"another tailnet peer", "100.100.100.100", "100.100.100.2", false},
		{"LAN third party", "192.168.5.16", "100.100.100.2", false},
		{"non-tailnet source", "198.18.0.1", "192.168.5.16", false},
		{"below tailnet range", "198.18.0.1", "100.63.255.255", false},
		{"above tailnet range", "198.18.0.1", "100.128.0.1", false},
		{"below TUN range", "198.17.255.255", "100.100.100.2", false},
		{"above TUN range", "198.20.0.1", "100.100.100.2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := sdpOffer(9224, tc.declared, "0 8 9 101", "a=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:9 G722/8000\r\na=rtpmap:101 telephone-event/8000\r\n")
			source := net.ParseIP(tc.peer)
			ip, port, err := validateAudioSDP(body, source)
			if !tc.accept {
				if err == nil {
					t.Fatal("accepted media redirection outside supported proxy combination")
				}
				return
			}
			if err != nil || port != 9224 || !ip.Equal(source) {
				t.Fatalf("media destination = %v:%d, %v; want authenticated source %v:9224", ip, port, err, source)
			}
		})
	}
}
