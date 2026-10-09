package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/update"
)

func TestStaleEmptyCLCCDoesNotClearNewDial(t *testing.T) {
	server := &Server{}
	revision := server.captureLegacyRevision()
	server.noteLegacyAccepted("gen", "cfg", "phys")
	server.observeLegacyCLCCAtRevision([]string{"cfg", "phys"}, nil, revision)
	if !server.legacyCallActivity() || !legacySlotActive(server, "cfg") || !legacySlotActive(server, "phys") {
		t.Fatal("stale empty snapshot cleared a dial accepted after the read started")
	}
	fresh := server.captureLegacyRevision()
	server.observeLegacyCLCCAtRevision([]string{"phys"}, nil, fresh)
	if !server.legacyCallActivity() || !legacySlotActive(server, "cfg") {
		t.Fatal("empty snapshot that started after ATD cleared the pending dial")
	}
}

func TestPendingExpiresBeforeEmptyCLCCCanClear(t *testing.T) {
	server := &Server{}
	server.noteLegacyVoiceCall("modem")
	server.legacyCallMu.Lock()
	slot := server.legacySlots["modem"]
	slot.pendingUntil = time.Now().Add(-time.Millisecond)
	server.legacySlots["modem"] = slot
	server.legacyCallMu.Unlock()
	revision := server.captureLegacyRevision()
	server.observeLegacyCLCCAtRevision([]string{"modem"}, nil, revision)
	if server.legacyCallActivity() {
		t.Fatal("empty snapshot still blocked after the protection window")
	}
}

func TestUnknownEnabledModemDefersUntilHealthyCLCC(t *testing.T) {
	database := memoryDB(t)
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "modem", Name: "modem", NetworkEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "idle", Name: "idle", NetworkEnabled: false}); err != nil {
		t.Fatal(err)
	}
	atErr := errors.New("timeout")
	polls := 0
	devices := &pollDevices{
		entries: []device.Device{{ID: "modem", Discovered: true}, {ID: "idle", Discovered: true}},
		at: func(id, command string) (modem.Response, error) {
			polls++
			if id != "modem" || command != "AT+CLCC" {
				t.Errorf("unexpected AT %s %s", id, command)
			}
			if atErr != nil {
				return modem.Response{}, atErr
			}
			return modem.Response{Final: "OK"}, nil
		},
	}
	server := &Server{
		store: database, devices: devices, logger: regionTestLogger(),
		maxRequestBodyBytes: 1 << 20, updateRepository: update.DefaultRepository,
		updateCheck: func(context.Context, string, string, string) (update.CheckResult, error) {
			return update.CheckResult{Available: true, Latest: "9.9.9"}, nil
		},
		updateApply: func(context.Context, *slog.Logger, update.Options, bool) (update.CheckResult, error) {
			t.Fatal("apply ran before the modem was known idle")
			return update.CheckResult{}, errors.New("applied")
		},
	}
	if !server.legacyCallActivity() {
		t.Fatal("enabled modem was treated as idle before the first CLCC")
	}
	recorder := httptest.NewRecorder()
	server.handleUpdateApply(recorder, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "call_in_progress") {
		t.Fatalf("manual check status = %d body = %s", recorder.Code, recorder.Body)
	}
	server.pollCellularCalls(context.Background())
	if polls != 1 || !server.legacyCallActivity() {
		t.Fatalf("failed poll polls=%d activity=%v", polls, server.legacyCallActivity())
	}
	atErr = nil
	server.pollCellularCalls(context.Background())
	if server.legacyCallActivity() {
		t.Fatal("healthy empty CLCC left the modem unknown")
	}
	if err := server.beginUpdate(); err != nil {
		t.Fatal(err)
	}
	server.finishUpdate()
}

func TestTelegramPhysicalCLCCClearsConfigAlias(t *testing.T) {
	server := &Server{
		logger: regionTestLogger(),
		devices: fakeDeviceController{entry: device.Device{ID: "phys", Discovered: true}, atHandler: func(command string) (modem.Response, error) {
			if command != "AT+CLCC" && command != "ATH" {
				t.Errorf("unexpected %s", command)
			}
			return modem.Response{Final: "OK"}, nil
		}},
	}
	server.noteLegacyAccepted("", "cfg", "phys")
	server.noteLegacyHangup("phys")
	if !legacySlotActive(server, "cfg") || !legacySlotActive(server, "phys") {
		t.Fatal("ATH cleared the call before CLCC confirmed it")
	}
	bot := &telegramBot{server: server}
	if _, err := bot.telegramCellularCalls(context.Background(), "phys"); err != nil {
		t.Fatal(err)
	}
	if legacySlotActive(server, "cfg") || legacySlotActive(server, "phys") || server.legacyCallActivity() {
		t.Fatal("physical CLCC did not clear the config alias")
	}
}

func TestPolicyChangeStillConfirmsLegacyEndAndUnplugDoesNotBlock(t *testing.T) {
	database := memoryDB(t)
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "modem", Name: "modem", NetworkEnabled: true}); err != nil {
		t.Fatal(err)
	}
	devices := &pollDevices{
		entries: []device.Device{{
			ID: "modem", Discovered: true,
			Candidate: modem.Candidate{USBGeneration: "g1"},
		}},
		at: func(id, command string) (modem.Response, error) {
			return modem.Response{Final: "OK"}, nil
		},
	}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}
	server.noteLegacyAccepted("g1", "modem")
	server.noteLegacyHangup("modem")
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "modem", Name: "modem", NetworkEnabled: true, VoWiFiEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	server.pollCellularCalls(context.Background())
	if server.legacyCallActivity() || legacySlotActive(server, "modem") {
		t.Fatal("VoWiFi policy change left a confirmed-ended legacy call active")
	}

	server.noteLegacyAccepted("g1", "modem")
	devices.entries[0].Discovered = false
	if server.legacyCallActivity() {
		t.Fatal("unplugged modem kept blocking updates")
	}
	if err := server.beginUpdate(); err != nil {
		t.Fatal(err)
	}
	server.finishUpdate()

	devices.entries[0].Discovered = true
	devices.entries[0].Candidate.USBGeneration = "g2"
	if server.legacyCallActivity() {
		t.Fatal("replaced USB generation kept the old legacy identity active")
	}
}

func TestReaderIsNotAnUnknownLegacyModem(t *testing.T) {
	database := memoryDB(t)
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: "reader", Name: "reader", NetworkEnabled: true, DeviceType: store.DeviceTypeUSBSIMReader,
	}); err != nil {
		t.Fatal(err)
	}
	devices := &pollDevices{
		entries: []device.Device{{ID: "reader", Discovered: true}},
		at: func(string, string) (modem.Response, error) {
			t.Fatal("reader was polled")
			return modem.Response{}, nil
		},
	}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}
	server.pollCellularCalls(context.Background())
	if server.legacyCallActivity() {
		t.Fatal("reader was treated as an unknown legacy modem")
	}
}

func legacySlotActive(server *Server, id string) bool {
	server.legacyCallMu.Lock()
	defer server.legacyCallMu.Unlock()
	return server.legacySlots[id].active
}

func TestReplacementModemMustEstablishItsOwnCallState(t *testing.T) {
	database := memoryDB(t)
	config := store.Device{ID: "modem", Name: "modem", NetworkEnabled: true}
	if err := database.UpsertDevice(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	devices := &pollDevices{entries: []device.Device{{
		ID: "modem", Discovered: true, Candidate: modem.Candidate{USBGeneration: "g1"},
	}}}
	server := &Server{store: database, devices: devices, logger: regionTestLogger()}
	server.noteLegacyAccepted("g1", "modem", "old-alias")
	oldRevision := server.captureLegacyRevision()
	devices.entries[0].Candidate.USBGeneration = "g2"
	if !server.legacyCallActivity() {
		t.Fatal("replacement enabled modem was treated as idle before its first CLCC")
	}
	server.commitLegacyCLCC([]string{"modem", "old-alias"}, nil, oldRevision, "g1")
	if !server.legacyCallActivity() {
		t.Fatal("late old-instance CLCC established the replacement as idle")
	}
	server.legacyCallMu.Lock()
	_, oldSlot := server.legacySlots["old-alias"]
	_, oldPeers := server.legacyPeers["old-alias"]
	server.legacyCallMu.Unlock()
	if oldSlot || oldPeers {
		t.Fatal("replacement retained the retired instance's aliases")
	}
	server.pollCellularCalls(context.Background())
	if server.legacyCallActivity() {
		t.Fatal("healthy empty CLCC from the replacement did not permit updates")
	}
}

func TestLateCLCCFromReplacedPhysicalInstanceIsDiscarded(t *testing.T) {
	for _, source := range []string{"web", "telegram", "poll"} {
		t.Run(source, func(t *testing.T) {
			database := memoryDB(t)
			config := store.Device{ID: "modem", Name: "modem", NetworkEnabled: true}
			if err := database.UpsertDevice(context.Background(), config); err != nil {
				t.Fatal(err)
			}
			devices := &pollDevices{entries: []device.Device{{
				ID: "modem", Discovered: true, Candidate: modem.Candidate{USBGeneration: "g1"},
			}}}
			server := &Server{store: database, devices: devices, logger: regionTestLogger()}
			server.commitLegacyCLCC([]string{"modem"}, nil, server.captureLegacyRevision(), "g1")
			devices.at = func(id, command string) (modem.Response, error) {
				if command != "AT+CLCC" {
					t.Fatalf("unexpected AT %s", command)
				}
				devices.entries[0].Candidate.USBGeneration = "g2"
				return modem.Response{Final: "OK"}, nil
			}
			switch source {
			case "web":
				server.handleCalls(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/calls", nil), config, "modem")
			case "telegram":
				bot := &telegramBot{server: server}
				if _, err := bot.telegramCellularCalls(context.Background(), "modem"); err != nil {
					t.Fatal(err)
				}
			case "poll":
				server.pollCellularCalls(context.Background())
			}
			if !server.legacyCallActivity() {
				t.Fatal("old instance's empty CLCC marked the replacement idle")
			}
			devices.at = nil
			server.pollCellularCalls(context.Background())
			if server.legacyCallActivity() {
				t.Fatal("replacement's own healthy empty CLCC did not establish idle")
			}
		})
	}
}
