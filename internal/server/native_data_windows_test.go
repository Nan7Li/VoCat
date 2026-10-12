//go:build windows

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"vocat/internal/developer"

	"vocat/internal/device"
	"vocat/internal/store"
)

func TestNativeWindowsDataWithoutDeveloperMode(t *testing.T) {
	db, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	controller := &monitorDeviceController{status: device.NetworkStatus{Backend: "mbn", Interface: "Cellular", Connected: false}}
	s := &Server{store: db, devices: controller, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20}
	config := store.Device{ID: "test-modem", Name: "test-modem", DeviceBackend: "mbn", Interface: "Cellular", NetworkEnabled: true}
	if err := db.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.handleCellularData(recorder, httptest.NewRequest(http.MethodGet, "/network", nil), config, "physical-1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status %d: %s", recorder.Code, recorder.Body)
	}
	data := decodeData(t, recorder)
	if data["enabled"] != true || data["connected"] != false || data["export_proxy_only"] != false {
		t.Fatalf("disconnected Windows state: %+v", data)
	}
	controller.status.Connected = true
	recorder = httptest.NewRecorder()
	s.handleCellularData(recorder, httptest.NewRequest(http.MethodGet, "/network", nil), config, "physical-1")
	if data := decodeData(t, recorder); data["connected"] != true {
		t.Fatalf("connected Windows state: %+v", data)
	}
	config.VoWiFiEnabled = true
	recorder = httptest.NewRecorder()
	s.handleCellularData(recorder, httptest.NewRequest(http.MethodPatch, "/network", strings.NewReader(`{"enabled":true}`)), config, "physical-1")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "vowifi_owns_radio") {
		t.Fatalf("VoWiFi conflict: %d %s", recorder.Code, recorder.Body)
	}
	config.DeviceBackend = "qmi"
	recorder = httptest.NewRecorder()
	s.handleCellularData(recorder, httptest.NewRequest(http.MethodGet, "/network", nil), config, "physical-1")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("experimental QMI status %d", recorder.Code)
	}
}

type nativeSIMDataController struct{ monitorDeviceController }

func (c *nativeSIMDataController) CellularSIMICCID(context.Context, string) (string, error) {
	return "8901000000000000001", nil
}

func TestNativeDataEnableBindsCurrentSIMAndSurvivesCleanup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	controller := &nativeSIMDataController{}
	controller.entry = device.Device{ID: "physical-1", Snapshot: &device.Snapshot{ICCID: "8901000000000000001"}}
	s := &Server{store: db, devices: controller, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20}
	runtime := s.cellularDataRuntime()
	defer runtime.stopAllWatchdogs()
	config := store.Device{ID: "test-native", Name: "test-native", Interface: "Cellular", DeviceBackend: "mbn"}
	if err := db.UpsertDevice(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCardPolicy(ctx, store.CardPolicy{ICCID: "8901000000000000001", AirplaneEnabled: true, IPVersion: "IPV4V6"}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.handleCellularData(recorder, httptest.NewRequest(http.MethodPatch, "/network", strings.NewReader(`{"enabled":true}`)), config, "physical-1")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("enable status %d: %s", recorder.Code, recorder.Body)
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := runtime.wait(wait, config.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := developer.ResetExperimental(ctx, db); err != nil {
		t.Fatal(err)
	}
	saved, err := db.Device(ctx, config.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := db.CardPolicy(ctx, "8901000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if !saved.NetworkEnabled || store.NativeCellularICCID(saved) != "8901000000000000001" || !policy.NetworkEnabled || policy.AirplaneEnabled || policy.VoWiFiEnabled {
		t.Fatal("native SIM data policy was reset or kept RF-off")
	}
	calls := controller.recordedSetCalls()
	if len(calls) != 1 || calls[0].Backend != "mbn" || !calls[0].Enabled {
		t.Fatalf("data requests: %+v", calls)
	}
}
