package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/buildinfo"
	"vocat/internal/cellbridge/cellular"
	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/update"
	"vocat/internal/vowifi"
)

func TestAutoUpdateNASDefaultDoesNotOverrideSavedPreference(t *testing.T) {
	t.Setenv("VOCAT_AUTO_UPDATE_APPLY_DEFAULT", "true")
	database := memoryDB(t)
	server := &Server{
		store: database, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20,
		updateRepository: update.DefaultRepository,
	}
	settings := getAutoUpdate(t, server)
	if !settings.Enabled || !settings.Apply || settings.IntervalHours != 6 {
		t.Fatalf("nas defaults = %+v", settings)
	}
	if settings.Channel != update.Channel() || settings.CurrentVersion != buildinfo.Version {
		t.Fatalf("display = %+v", settings)
	}
	if settings.Repository != update.DefaultRepository {
		t.Fatalf("repository = %q", settings.Repository)
	}

	status, raw := putAutoUpdate(t, server, `{"enabled":true,"apply":false,"interval_hours":6,"channel":"evil","repository":"other/repo"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("extra fields status = %d body = %s", status, raw)
	}
	status, raw = putAutoUpdate(t, server, `{"enabled":true,"apply":false,"interval_hours":12}`)
	if status != http.StatusOK {
		t.Fatalf("save status = %d body = %s", status, raw)
	}
	saved := getAutoUpdate(t, server)
	if saved.Apply || saved.IntervalHours != 12 || saved.Channel != update.Channel() {
		t.Fatalf("saved preference lost: %+v", saved)
	}
	again := &Server{
		store: database, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20,
		updateRepository: update.DefaultRepository,
	}
	reloaded := getAutoUpdate(t, again)
	if reloaded.Apply || reloaded.IntervalHours != 12 {
		t.Fatalf("reloaded = %+v", reloaded)
	}
}

func TestAutoUpdateDefersDuringCallAndRetriesWhenItEnds(t *testing.T) {
	database := memoryDB(t)
	checks := 0
	applies := 0
	var calls []vowifi.Call
	server := &Server{
		store: database, logger: regionTestLogger(), updateRepository: update.DefaultRepository,
		cellularCallsForUpdate: func() []vowifi.Call { return calls },
		updateCheck: func(context.Context, string, string, string) (update.CheckResult, error) {
			checks++
			return update.CheckResult{Available: true, Current: "1.0.0", Latest: "9.9.9"}, nil
		},
		updateApply: func(context.Context, *slog.Logger, update.Options, bool) (update.CheckResult, error) {
			applies++
			return update.CheckResult{Applied: true, Latest: "9.9.9"}, nil
		},
		updateRestart: func(*slog.Logger) error { return nil },
	}
	if err := server.saveAutoUpdateSettings(context.Background(), autoUpdateSettings{Enabled: true, Apply: true, IntervalHours: 6}); err != nil {
		t.Fatal(err)
	}
	calls = []vowifi.Call{{ID: "live", State: "dialing"}}
	server.maybeAutoUpdate(context.Background(), false)
	if checks != 1 || applies != 0 {
		t.Fatalf("first pass checks=%d applies=%d", checks, applies)
	}
	settings, err := server.loadAutoUpdateSettings(context.Background())
	if err != nil || !autoUpdateWaitingForCall(settings.LastError) || settings.LastVersion != "9.9.9" || !settings.LastAvailable {
		t.Fatalf("deferred settings = %+v, %v", settings, err)
	}
	server.maybeAutoUpdate(context.Background(), false)
	if checks != 1 || applies != 0 {
		t.Fatalf("active call queried GitHub again: checks=%d applies=%d", checks, applies)
	}
	kept, _ := server.loadAutoUpdateSettings(context.Background())
	if kept.LastVersion != "9.9.9" || !autoUpdateWaitingForCall(kept.LastError) {
		t.Fatalf("waiting status cleared: %+v", kept)
	}

	ended := time.Now()
	calls = []vowifi.Call{{ID: "done", State: "ended", EndedAt: &ended}}
	if server.callActivity() {
		t.Fatal("ended cellular call blocked update")
	}
	calls = nil
	server.maybeAutoUpdate(context.Background(), false)
	if checks != 2 || applies != 1 {
		t.Fatalf("retry checks=%d applies=%d", checks, applies)
	}
}

func TestManualApplyReturns409WhileCallActive(t *testing.T) {
	server := testUpdateServer(t)
	server.cellularCallsForUpdate = func() []vowifi.Call {
		return []vowifi.Call{{ID: "ring", State: "ringing"}}
	}
	applied := false
	server.updateApply = func(context.Context, *slog.Logger, update.Options, bool) (update.CheckResult, error) {
		applied = true
		return update.CheckResult{Applied: true}, nil
	}
	recorder := httptest.NewRecorder()
	server.handleUpdateApply(recorder, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "call_in_progress") {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body)
	}
	if applied {
		t.Fatal("apply ran during a call")
	}
}

func TestManualApplyBeforeInstallSeesCallThatStartsDuringDownload(t *testing.T) {
	server := testUpdateServer(t)
	server.updateApply = func(_ context.Context, _ *slog.Logger, opts update.Options, _ bool) (update.CheckResult, error) {
		server.imsCallsForUpdate = func() ([]vowifi.Call, error) {
			return []vowifi.Call{{ID: "ims", State: "active"}}, nil
		}
		if opts.BeforeInstall == nil {
			t.Fatal("missing BeforeInstall")
		}
		return update.CheckResult{}, opts.BeforeInstall()
	}
	recorder := httptest.NewRecorder()
	server.handleUpdateApply(recorder, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "call_in_progress") {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body)
	}
	release, err := server.admitCallMutation()
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestUpdateRestartHandoffKeepsAdmissionUntilRestartFails(t *testing.T) {
	database := memoryDB(t)
	started := make(chan struct{})
	release := make(chan error, 1)
	server := testUpdateServer(t)
	server.store = database
	server.updateRestart = func(*slog.Logger) error {
		close(started)
		return <-release
	}
	t.Cleanup(func() {
		select {
		case release <- errors.New("cleanup"):
		default:
		}
	})
	recorder := httptest.NewRecorder()
	server.handleUpdateApply(recorder, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"applied":true`) {
		t.Fatalf("apply status = %d body = %s", recorder.Code, recorder.Body)
	}
	if _, err := server.admitCallMutation(); !errors.Is(err, errUpdateInProgress) {
		t.Fatalf("web admission after apply = %v", err)
	}
	backend := &cellularSIPBackend{server: server, deviceID: "d1", controller: &cellular.Controller{}}
	if _, err := backend.Dial(context.Background(), "10086"); !errors.Is(err, errUpdateInProgress) {
		t.Fatalf("sip dial during restart = %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("restart was not invoked")
	}
	if _, err := server.admitCallMutation(); !errors.Is(err, errUpdateInProgress) {
		t.Fatal("admission opened after systemctl was only queued")
	}
	release <- errors.New("systemctl failed")
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.updateMu.Lock()
		closed := server.updateApplying
		server.updateMu.Unlock()
		settings, err := server.loadAutoUpdateSettings(context.Background())
		if !closed && err == nil && strings.Contains(settings.LastError, "服务重启失败") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate closed=%v last error=%q load=%v", closed, settings.LastError, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	releaseCall, err := server.admitCallMutation()
	if err != nil {
		t.Fatal(err)
	}
	releaseCall()

	plain := testUpdateServer(t)
	plain.updateRestart = nil
	ok := httptest.NewRecorder()
	plain.handleUpdateApply(ok, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if ok.Code != http.StatusOK {
		t.Fatalf("nil restart status = %d %s", ok.Code, ok.Body)
	}
	if _, err := plain.admitCallMutation(); err != nil {
		t.Fatalf("nil restart kept the gate: %v", err)
	}
}

func TestTelegramCallActionsRespectUpdateGuard(t *testing.T) {
	controller := &updateGuardIMS{
		calls: []vowifi.Call{{ID: "ims-1", Number: "10086", State: "ringing"}},
	}
	commands := []string{}
	server := &Server{
		logger: regionTestLogger(),
		vowifi: controller,
		devices: fakeDeviceController{atHandler: func(command string) (modem.Response, error) {
			commands = append(commands, command)
			return modem.Response{Final: "OK"}, nil
		}},
	}
	server.updateMu.Lock()
	server.updateApplying = true
	server.updateMu.Unlock()
	bot := &telegramBot{server: server}
	_, err := bot.executeTimedVoWiFiCall(context.Background(), telegramRuntimeConfig{}, telegramPendingAction{
		DeviceID: "d1", Argument: "10086", Duration: time.Second,
	}, controller)
	if !errors.Is(err, errUpdateInProgress) || controller.dials != 0 {
		t.Fatalf("timed dial err=%v dials=%d", err, controller.dials)
	}
	_, err = bot.executeSimpleVoWiFiCallAction(context.Background(), "d1", "answer", controller)
	if !errors.Is(err, errUpdateInProgress) || controller.answers != 0 {
		t.Fatalf("answer err=%v answers=%d", err, controller.answers)
	}
	_, err = bot.executeTimedCellularCall(context.Background(), telegramRuntimeConfig{}, telegramPendingAction{
		DeviceID: "d1", Argument: "10086", Duration: time.Second,
	}, "physical")
	if !errors.Is(err, errUpdateInProgress) {
		t.Fatalf("cellular dial err=%v", err)
	}
	_, err = bot.executeSimpleCellularCallAction(context.Background(), "physical", "answer")
	if !errors.Is(err, errUpdateInProgress) {
		t.Fatalf("cellular answer err=%v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("AT ran during update: %v", commands)
	}
	if _, err := bot.executeSimpleVoWiFiCallAction(context.Background(), "d1", "hangup", controller); err != nil || controller.hangups != 1 {
		t.Fatalf("hangup err=%v hangups=%d", err, controller.hangups)
	}
	result, err := bot.executeSimpleCellularCallAction(context.Background(), "physical", "hangup")
	if err != nil || !strings.Contains(strings.Join(commands, ","), "ATH") {
		t.Fatalf("cellular hangup = %q, %v commands=%v", result, err, commands)
	}
}

func TestLegacyCallStaysVisibleAfterDialUntilCLCCEnds(t *testing.T) {
	database := memoryDB(t)
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "modem", Name: "modem", NetworkEnabled: true}); err != nil {
		t.Fatal(err)
	}
	clcc := []string{}
	atErr := error(nil)
	atCount := 0
	devices := &pollDevices{
		entries: []device.Device{{ID: "modem", Discovered: true}},
		at: func(id, command string) (modem.Response, error) {
			atCount++
			if atErr != nil {
				return modem.Response{}, atErr
			}
			return modem.Response{Lines: clcc, Final: "OK"}, nil
		},
	}
	server := &Server{
		store: database, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20,
		devices: devices,
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/dial", strings.NewReader(`{"number":"10086"}`))
	request.Header.Set("Content-Type", "application/json")
	if !server.handleCallAction(recorder, request, store.Device{ID: "modem", NetworkEnabled: true}, "modem", "dial") {
		t.Fatal("dial was not handled")
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("dial status = %d body = %s", recorder.Code, recorder.Body)
	}
	if !server.legacyCallActivity() || !server.callActivity() {
		t.Fatal("legacy dial was invisible after the request returned")
	}
	if err := server.beginUpdate(); !errors.Is(err, errCallActive) {
		t.Fatalf("beginUpdate during legacy call = %v", err)
	}

	hangup := httptest.NewRecorder()
	hangupRequest := httptest.NewRequest(http.MethodPost, "/hangup", strings.NewReader(`{}`))
	hangupRequest.Header.Set("Content-Type", "application/json")
	server.handleCallAction(hangup, hangupRequest, store.Device{ID: "modem", NetworkEnabled: true}, "modem", "hangup")
	if hangup.Code != http.StatusAccepted {
		t.Fatalf("hangup status = %d body = %s", hangup.Code, hangup.Body)
	}
	if !server.legacyCallActivity() {
		t.Fatal("hangup command alone cleared a call that CLCC has not ended")
	}

	atErr = errors.New("disconnected")
	server.pollCellularCalls(context.Background())
	if !server.legacyCallActivity() {
		t.Fatal("failed CLCC cleared an active legacy call")
	}
	atErr = nil
	clcc = []string{`+CLCC: 1,0,0,1,0,"",255`}
	server.pollCellularCalls(context.Background())
	if server.legacyCallActivity() {
		t.Fatal("packet-data CLCC blocked update")
	}
	server.noteLegacyVoiceCall("modem")
	clcc = []string{`+CLCC: 1,0,2,0,0,"10086",129`}
	server.pollCellularCalls(context.Background())
	if !server.callActivity() {
		t.Fatal("voice CLCC state 2 was ignored")
	}
	clcc = nil
	server.pollCellularCalls(context.Background())
	if server.legacyCallActivity() || server.callActivity() {
		t.Fatal("empty CLCC left the update blocked")
	}
	if err := server.beginUpdate(); err != nil {
		t.Fatal(err)
	}
	server.finishUpdate()
	if atCount == 0 {
		t.Fatal("legacy modem was not polled")
	}
}

func TestPollSkipsReaderVoWiFiAndCellBridge(t *testing.T) {
	database := memoryDB(t)
	for _, deviceID := range []string{"reader", "vowifi", "bridge", "modem"} {
		config := store.Device{ID: deviceID, Name: deviceID, NetworkEnabled: true}
		switch deviceID {
		case "reader":
			config.DeviceType = store.DeviceTypeUSBSIMReader
		case "vowifi":
			config.VoWiFiEnabled = true
		}
		if err := database.UpsertDevice(context.Background(), config); err != nil {
			t.Fatal(err)
		}
	}
	seen := []string{}
	devices := &pollDevices{
		entries: []device.Device{
			{ID: "reader", Discovered: true},
			{ID: "vowifi", Discovered: true},
			{ID: "bridge", Discovered: true},
			{ID: "modem", Discovered: true},
		},
		at: func(id, command string) (modem.Response, error) {
			seen = append(seen, id+" "+command)
			return modem.Response{Final: "OK"}, nil
		},
	}
	server := &Server{
		store: database, logger: regionTestLogger(), devices: devices,
		cellBridgeCtrl: &cellBridgeLine{deviceID: "bridge", controller: &cellular.Controller{}},
	}
	server.pollCellularCalls(context.Background())
	if strings.Join(seen, ",") != "modem AT+CLCC" {
		t.Fatalf("polled = %#v", seen)
	}
}

func TestLegacyHookAndMediaLockDoNotDeadlock(t *testing.T) {
	server := &Server{}
	server.legacyCallsForUpdate = func() bool { return false }
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				release, err := server.admitCallMutation()
				if err == nil {
					server.noteLegacyVoiceCall("modem")
					release()
				}
				_ = server.callActivity()
				server.observeLegacyCLCC("modem", []map[string]any{{"mode": 1, "state": 0}}, true)
				if err := server.beginUpdate(); err == nil {
					server.finishUpdate()
				}
				if rel, leaseErr := server.acquireCallMediaLease("modem", "call", "test"); leaseErr == nil {
					rel()
				}
			}
		}()
	}
	wg.Wait()
}

func TestBackupFailureBlocksApply(t *testing.T) {
	dir := t.TempDir()
	database, err := store.Open(context.Background(), filepath.Join(dir, "halo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := os.WriteFile(filepath.Join(dir, "update-backups"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	applied := false
	server := &Server{
		store: database, logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20,
		updateRepository: update.DefaultRepository,
		updateCheck: func(context.Context, string, string, string) (update.CheckResult, error) {
			return update.CheckResult{Available: true, Latest: "9.9.9"}, nil
		},
		updateApply: func(context.Context, *slog.Logger, update.Options, bool) (update.CheckResult, error) {
			applied = true
			return update.CheckResult{Applied: true, Latest: "9.9.9"}, nil
		},
	}
	recorder := httptest.NewRecorder()
	server.handleUpdateApply(recorder, httptest.NewRequest(http.MethodPost, "/apply", nil))
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "backup_failed") {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body)
	}
	if applied {
		t.Fatal("apply ran after backup failure")
	}
	if _, err := server.admitCallMutation(); err != nil {
		t.Fatalf("failed backup left the update gate closed: %v", err)
	}
}

type pollDevices struct {
	fakeDeviceController
	entries []device.Device
	at      func(id, command string) (modem.Response, error)
}

func (p *pollDevices) Get(id string) (device.Device, error) {
	for _, entry := range p.entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return device.Device{}, device.ErrNotFound
}

func (p *pollDevices) List() []device.Device { return p.entries }

func (p *pollDevices) ExecuteAT(_ context.Context, id string, command string) (modem.Response, error) {
	if p.at != nil {
		return p.at(id, command)
	}
	return modem.Response{Final: "OK"}, nil
}

type updateGuardIMS struct {
	calls   []vowifi.Call
	dials   int
	answers int
	hangups int
}

func (c *updateGuardIMS) State(string) (vowifi.State, error) {
	return vowifi.State{IMSReady: true, Phase: vowifi.PhaseIMSReady}, nil
}
func (c *updateGuardIMS) RequestEnabled(string, bool) (vowifi.State, error) {
	return vowifi.State{}, nil
}
func (c *updateGuardIMS) RequestReconnect(string) (vowifi.State, error) {
	return vowifi.State{}, nil
}
func (c *updateGuardIMS) Calls(string) ([]vowifi.Call, error) {
	return append([]vowifi.Call(nil), c.calls...), nil
}
func (c *updateGuardIMS) DialCall(context.Context, string, string) (vowifi.Call, error) {
	c.dials++
	return vowifi.Call{}, errors.New("dial should not run")
}
func (c *updateGuardIMS) AnswerCall(context.Context, string, string) (vowifi.Call, error) {
	c.answers++
	return vowifi.Call{}, errors.New("answer should not run")
}
func (c *updateGuardIMS) HangupCall(context.Context, string, string) error {
	c.hangups++
	return nil
}

func memoryDB(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func testUpdateServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		store: memoryDB(t), logger: regionTestLogger(), maxRequestBodyBytes: 1 << 20,
		updateRepository: update.DefaultRepository,
		updateCheck: func(context.Context, string, string, string) (update.CheckResult, error) {
			return update.CheckResult{Available: true, Current: "1.0.0", Latest: "9.9.9"}, nil
		},
		updateApply: func(context.Context, *slog.Logger, update.Options, bool) (update.CheckResult, error) {
			return update.CheckResult{Applied: true, Latest: "9.9.9"}, nil
		},
	}
}

func getAutoUpdate(t *testing.T, server *Server) autoUpdateSettings {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.handleAutoUpdateSettings(recorder, httptest.NewRequest(http.MethodGet, "/api/settings/auto-update", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body = %s", recorder.Code, recorder.Body)
	}
	var envelope struct {
		Data autoUpdateSettings `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func putAutoUpdate(t *testing.T, server *Server, body string) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/settings/auto-update", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.handleAutoUpdateSettings(recorder, request)
	return recorder.Code, recorder.Body.String()
}
