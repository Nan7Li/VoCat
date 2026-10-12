//go:build windows

package developer

import (
	"context"
	"testing"
	"vocat/internal/store"
)

func TestDeveloperCleanupPreservesNativeWindowsDataOnly(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, config := range []store.Device{
		{ID: "native", Name: "native", DeviceBackend: "mbn", Interface: "Cellular", NetworkEnabled: true},
		{ID: "experimental", Name: "experimental", DeviceBackend: "qmi", Interface: "wwan0", NetworkEnabled: true},
	} {
		if err := db.UpsertDevice(ctx, config); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertDeviceRuntime(ctx, store.DeviceRuntime{DeviceID: "native", ICCID: "8901000000000000001"}); err != nil {
		t.Fatal(err)
	}
	for _, iccid := range []string{"8901000000000000001", "8901000000000000002"} {
		if err := db.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: iccid, NetworkEnabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ResetExperimental(ctx, db); err != nil {
		t.Fatal(err)
	}
	native, _ := db.Device(ctx, "native")
	experimental, _ := db.Device(ctx, "experimental")
	nativePolicy, _ := db.CardPolicy(ctx, "8901000000000000001")
	otherPolicy, _ := db.CardPolicy(ctx, "8901000000000000002")
	if !native.NetworkEnabled || experimental.NetworkEnabled || !nativePolicy.NetworkEnabled || otherPolicy.NetworkEnabled {
		t.Fatal("cleanup did not distinguish native WWAN from developer data")
	}
}
