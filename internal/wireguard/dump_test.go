package wireguard

import "testing"

func TestParseWGDump(t *testing.T) {
	raw := "priv\tpub\t51820\toff\n" +
		"peer\t(none)\t203.0.113.10:51820\t0.0.0.0/0\t1700000000\t100\t200\t25\n"
	info := parseWGDump(raw)
	if !info.Running || info.PublicKey != "pub" || info.ListenPort != 51820 {
		t.Fatalf("iface = %#v", info)
	}
	if info.Peers != 1 || info.TransferRX != 100 || info.TransferTX != 200 || info.Endpoint != "203.0.113.10:51820" {
		t.Fatalf("peer = %#v", info)
	}
	if info.LatestHandshake != "2023-11-14T22:13:20Z" {
		t.Fatalf("handshake = %q", info.LatestHandshake)
	}
}
