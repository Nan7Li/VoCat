package store

import (
	"context"
	"errors"
	"testing"
)

func TestWireGuardTunnelRoundTrip(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	saved, err := database.SaveWireGuardTunnel(ctx, WireGuardTunnel{
		ID:        "tun1",
		Name:      "Home",
		Interface: "halo0",
		Config:    "[Interface]\nPrivateKey = abcdef\nAddress = 10.0.0.2/32\n",
		Autostart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "Home" || saved.Interface != "halo0" || !saved.Autostart {
		t.Fatalf("saved = %#v", saved)
	}

	listed, err := database.ListWireGuardTunnels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "tun1" {
		t.Fatalf("list = %#v", listed)
	}

	if _, err := database.SaveWireGuardTunnel(ctx, WireGuardTunnel{
		ID:        "tun2",
		Name:      "Office",
		Interface: "halo0",
		Config:    "[Interface]\nPrivateKey = other\nAddress = 10.0.0.3/32\n",
	}); err == nil {
		t.Fatal("duplicate interface was accepted")
	}

	if err := database.DeleteWireGuardTunnel(ctx, "tun1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.WireGuardTunnel(ctx, "tun1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted tunnel still readable: %v", err)
	}
}

func TestWireGuardSecretsExtractPrivateAndPresharedKeys(t *testing.T) {
	got := wireGuardSecrets(`
[Interface]
PrivateKey = interface-secret
Address = 10.8.0.2/32

[Peer]
PublicKey = peer-public
PresharedKey = peer-psk
`)
	if len(got) != 2 {
		t.Fatalf("secrets = %#v", got)
	}
}