//go:build windows && (amd64 || arm64)

package ims

import (
	"context"
	"net"
	"os"
	"testing"
)

// This test is intentionally opt-in. Installing a manual WFP SA changes the
// host firewall/IPsec state and requires an elevated Windows process, so it
// must never run in CI or as part of an ordinary developer test run.
func TestWindowsWFPManualSAIntegration(t *testing.T) {
	if os.Getenv("VOCAT_WFP_INTEGRATION") != "1" {
		t.Skip("set VOCAT_WFP_INTEGRATION=1 on an elevated Windows host to exercise WFP")
	}
	local := parseWindowsIntegrationIP(t, "VOCAT_WFP_LOCAL_IP")
	remote := parseWindowsIntegrationIP(t, "VOCAT_WFP_REMOTE_IP")
	if (local.To4() != nil) != (remote.To4() != nil) {
		t.Fatal("VOCAT_WFP_LOCAL_IP and VOCAT_WFP_REMOTE_IP must use the same IP family")
	}

	cases := []struct {
		name        string
		encryption  string
		integrity   string
		encryptionN int
		integrityN  int
	}{
		{name: "null-sha1", encryption: "null", integrity: "hmac-sha-1-96", encryptionN: 0, integrityN: 20},
		{name: "aes-sha1", encryption: "aes-cbc", integrity: "hmac-sha-1-96", encryptionN: 16, integrityN: 20},
		{name: "3des-md5", encryption: "des-ede3-cbc", integrity: "hmac-md5-96", encryptionN: 24, integrityN: 16},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := IPSecSAConfig{
				LocalIP:             local,
				RemoteIP:            remote,
				EncryptionAlgorithm: testCase.encryption,
				IntegrityAlgorithm:  testCase.integrity,
				UEClientSPI:         uint32(0x20000010 + index*4),
				UEServerSPI:         uint32(0x20000011 + index*4),
				PCSCFClientSPI:      uint32(0x20000012 + index*4),
				PCSCFServerSPI:      uint32(0x20000013 + index*4),
				UEClientPort:        21000 + index*4,
				UEServerPort:        21001 + index*4,
				PCSCFClientPort:     22000 + index*4,
				PCSCFServerPort:     22001 + index*4,
				EncryptionKey:       make([]byte, testCase.encryptionN),
				IntegrityKey:        make([]byte, testCase.integrityN),
			}
			handle, err := (windowsIPSecInstaller{}).Install(context.Background(), config)
			if err != nil {
				t.Fatalf("WFP %s install failed: %v", testCase.name, err)
			}
			if handle == nil {
				t.Fatal("WFP installer returned a nil handle")
			}
			if err := handle.Close(context.Background()); err != nil {
				t.Fatalf("WFP %s cleanup failed: %v", testCase.name, err)
			}
		})
	}
}

func parseWindowsIntegrationIP(t *testing.T, variable string) net.IP {
	t.Helper()
	value := net.ParseIP(os.Getenv(variable))
	if value == nil || value.IsUnspecified() || value.IsMulticast() {
		t.Fatalf("%s must contain a unicast IP address", variable)
	}
	return value
}
