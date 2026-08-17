package wireguard

import (
	"strings"
	"testing"

	"vocat/internal/store"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const samplePeer = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
const samplePSK = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="

func sampleConfig() string {
	return `[Interface]
PrivateKey = ` + sampleKey + `
Address = 10.8.0.2/32
DNS = 1.1.1.1

[Peer]
PublicKey = ` + samplePeer + `
PresharedKey = ` + samplePSK + `
Endpoint = 203.0.113.10:51820
AllowedIPs = 0.0.0.0/0
`
}

func TestParseConfigAcceptsValidTunnel(t *testing.T) {
	parsed, err := parseConfig(sampleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Address != "10.8.0.2/32" || parsed.PeerCount != 1 || parsed.Preshared[samplePeer] != samplePSK {
		t.Fatalf("parsed = %#v", parsed)
	}
	if len(parsed.Peers) != 1 || parsed.Peers[0].Endpoint != "203.0.113.10:51820" {
		t.Fatalf("peer = %#v", parsed.Peers)
	}
}

func TestWGSetConfOmitsQuickOnlyKeys(t *testing.T) {
	parsed, err := parseConfig(sampleConfig())
	if err != nil {
		t.Fatal(err)
	}
	conf := wgSetConf(parsed)
	if strings.Contains(conf, "Address") || strings.Contains(conf, "DNS") {
		t.Fatalf("setconf leaked wg-quick keys: %s", conf)
	}
	if !strings.Contains(conf, samplePeer) || !strings.Contains(conf, "203.0.113.10:51820") {
		t.Fatalf("setconf missing peer: %s", conf)
	}
}

func TestTableForIsStable(t *testing.T) {
	if TableFor("halo0") != TableFor("halo0") || TableFor("halo0") == TableFor("halo1") {
		t.Fatalf("table ids = %d %d", TableFor("halo0"), TableFor("halo1"))
	}
}

func TestParseConfigRejectsMissingPeer(t *testing.T) {
	_, err := parseConfig("[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.0.2/32\n")
	if err == nil {
		t.Fatal("expected missing peer to fail")
	}
}

func TestValidateInterfaceName(t *testing.T) {
	if err := validateInterfaceName("halo0"); err != nil {
		t.Fatal(err)
	}
	if err := validateInterfaceName("lo"); err == nil {
		t.Fatal("reserved name accepted")
	}
	if err := validateInterfaceName("WG0"); err == nil {
		t.Fatal("uppercase name accepted")
	}
}

func TestMergeMaskedConfigRestoresSecrets(t *testing.T) {
	submitted := strings.ReplaceAll(sampleConfig(), sampleKey, store.SecretMask)
	submitted = strings.ReplaceAll(submitted, samplePSK, store.SecretMask)
	merged, err := mergeMaskedConfig(submitted, sampleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, sampleKey) || !strings.Contains(merged, samplePSK) {
		t.Fatalf("merged secrets were not restored: %s", merged)
	}
}

func TestRedactConfigHidesKeys(t *testing.T) {
	redacted := redactConfig(sampleConfig())
	if strings.Contains(redacted, sampleKey) || strings.Contains(redacted, samplePSK) {
		t.Fatalf("secrets leaked: %s", redacted)
	}
	if !strings.Contains(redacted, store.SecretMask) {
		t.Fatal("expected masked secrets")
	}
}

func TestNextInterfaceNameSkipsUsed(t *testing.T) {
	if got := nextInterfaceName([]string{"halo0", "halo1"}); got != "halo2" {
		t.Fatalf("got %q", got)
	}
}
