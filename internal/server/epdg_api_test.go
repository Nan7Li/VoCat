package server

import (
	"context"
	"strings"
	"testing"

	"vocat/internal/device"
	"vocat/internal/store"
)

func openEPDGTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID:         "modem-1",
		Name:       "Test modem",
		DeviceType: store.DeviceTypePCIeEC20EC25,
	}); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestResolveEPDGProbeTargetPrefersCurrentSIMOverStaleRuntime(t *testing.T) {
	ctx := context.Background()
	database := openEPDGTestStore(t)
	if err := database.UpsertVoWiFiRuntime(ctx, store.VoWiFiRuntime{
		DeviceID: "modem-1",
		ICCID:    "old-vodafone-card",
		Tunnel:   []byte(`{"epdg":"epdg.epc.mnc015.mcc234.pub.3gppnetwork.org"}`),
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{store: database}
	host, iccid, _, err := s.resolveEPDGProbeTarget(ctx, store.Device{ID: "modem-1"}, device.Device{
		Snapshot: &device.Snapshot{
			ICCID:     "current-card",
			IMSI:      "310260123456789",
			MNCLength: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if iccid != "current-card" {
		t.Fatalf("ICCID = %q, want current card", iccid)
	}
	if host != "epdg.epc.mnc260.mcc310.pub.3gppnetwork.org" {
		t.Fatalf("host = %q, want current SIM ePDG", host)
	}
}

func TestResolveEPDGProbeTargetRejectsPreviousProbeFromAnotherSIM(t *testing.T) {
	ctx := context.Background()
	database := openEPDGTestStore(t)
	if err := database.SaveEPDGProbeStatus(ctx, store.EPDGProbeStatus{
		DeviceID: "modem-1",
		ICCID:    "old-vodafone-card",
		EPDG:     "epdg.epc.mnc015.mcc234.pub.3gppnetwork.org",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{store: database}
	host, iccid, _, err := s.resolveEPDGProbeTarget(ctx, store.Device{ID: "modem-1"}, device.Device{
		Snapshot: &device.Snapshot{ICCID: "unidentified-current-card"},
	})
	if err == nil {
		t.Fatalf("expected current SIM derivation error, got host %q", host)
	}
	if iccid != "unidentified-current-card" {
		t.Fatalf("ICCID = %q, want current card", iccid)
	}
	if strings.Contains(host, "mcc234") {
		t.Fatalf("reused stale Vodafone host %q", host)
	}
}
