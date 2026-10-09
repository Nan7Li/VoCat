package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/bcrypt"

	"vocat/internal/auth"
	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
)

type bridgeModem struct {
	fakeDeviceController
	mu       sync.Mutex
	entries  map[string]*device.Device
	commands []string
	clcc     map[string]string
}

func (m *bridgeModem) Get(id string) (device.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil || !entry.Discovered {
		return device.Device{}, device.ErrNotFound
	}
	copied := *entry
	return copied, nil
}

func (m *bridgeModem) List() []device.Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]device.Device, 0, len(m.entries))
	for _, entry := range m.entries {
		if entry != nil && entry.Discovered {
			out = append(out, *entry)
		}
	}
	return out
}

func (m *bridgeModem) Discover(context.Context) ([]device.Device, error) {
	return m.List(), nil
}

func (m *bridgeModem) ExecuteAT(_ context.Context, id, command string) (modem.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = append(m.commands, id+" "+command)
	if strings.HasPrefix(strings.ToUpper(command), "AT+CLCC") {
		var lines []string
		if line := m.clcc[id]; line != "" {
			lines = []string{line}
		}
		return modem.Response{Command: command, Lines: lines, Final: "OK"}, nil
	}
	return modem.Response{Command: command, Final: "OK"}, nil
}

func (m *bridgeModem) snapshotCommands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.commands...)
}

func (m *bridgeModem) setSnapshot(id string, mutate func(*device.Snapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry.Snapshot == nil {
		entry.Snapshot = &device.Snapshot{}
	}
	mutate(entry.Snapshot)
}

func (m *bridgeModem) setGeneration(id, generation string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[id].Candidate.USBGeneration = generation
}

func (m *bridgeModem) setCLCC(id, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clcc == nil {
		m.clcc = make(map[string]string)
	}
	m.clcc[id] = line
}

type mockBridgeAudio struct {
	mu        sync.Mutex
	ready     bool
	reason    string
	down      []int16
	written   []int16
	stopped   chan struct{}
	stopOnce  sync.Once
	entered   chan struct{}
	enterOnce sync.Once
	block     chan struct{}
}

func newMockBridgeAudio() *mockBridgeAudio {
	down := make([]int16, 160)
	for index := range down {
		down[index] = 1000
	}
	return &mockBridgeAudio{
		ready:   true,
		down:    down,
		stopped: make(chan struct{}),
		entered: make(chan struct{}),
	}
}

func (audio *mockBridgeAudio) Status(context.Context) (bool, string) {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	return audio.ready, audio.reason
}

func (audio *mockBridgeAudio) Prepare(ctx context.Context, _ string) error {
	audio.enterOnce.Do(func() { close(audio.entered) })
	if audio.block != nil {
		select {
		case <-audio.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (audio *mockBridgeAudio) Start(context.Context, string) error { return nil }

func (audio *mockBridgeAudio) ReadPCM(buf []int16) (int, error) {
	timer := time.NewTimer(15 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-audio.stopped:
		return 0, io.EOF
	case <-timer.C:
	}
	audio.mu.Lock()
	defer audio.mu.Unlock()
	return copy(buf, audio.down), nil
}

func (audio *mockBridgeAudio) WritePCM(samples []int16) error {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	audio.written = append(audio.written, samples...)
	return nil
}

func (audio *mockBridgeAudio) Stop(context.Context) error {
	audio.stopOnce.Do(func() { close(audio.stopped) })
	return nil
}

func (audio *mockBridgeAudio) wrote() int {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	return len(audio.written)
}

type bridgeApp struct {
	handler   *Server
	server    *httptest.Server
	client    *http.Client
	modem     *bridgeModem
	audio     *mockBridgeAudio
	recordDir string
	csrf      string
}

func newBridgeApp(t *testing.T, withAudio bool) *bridgeApp {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authService, err := auth.New(database, auth.Options{SessionTTL: time.Hour, BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatal(err)
	}
	if err := authService.EnsureAdmin(context.Background(), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	recordDir := t.TempDir()
	modem := &bridgeModem{entries: map[string]*device.Device{}, clcc: map[string]string{}}
	handler, err := New(Options{
		Store:         database,
		Auth:          authService,
		Assets:        fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
		Devices:       modem,
		RecordingsDir: recordDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &bridgeApp{handler: handler, modem: modem, recordDir: recordDir}
	if withAudio {
		app.audio = newMockBridgeAudio()
		handler.cellBridgeAudioFactory = func(spec cellBridgeAudioSpec) cellularAudio {
			if spec.DeviceID == "dji-1" {
				return app.audio
			}
			return nil
		}
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	app.server = httpServer
	app.client = &http.Client{Jar: jar}
	return app
}

func (app *bridgeApp) login(t *testing.T) {
	t.Helper()
	response, err := app.client.Post(app.server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"correct-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || payload.Data.CSRFToken == "" {
		t.Fatalf("login status %d", response.StatusCode)
	}
	app.csrf = payload.Data.CSRFToken
}

func (app *bridgeApp) do(t *testing.T, method, path string, body any, csrf bool) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, app.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", app.csrf)
	}
	response, err := app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, payload
}

func upsertBridgeDevice(t *testing.T, database *store.Store, id, kind string, vowifi bool) {
	t.Helper()
	if err := database.UpsertDevice(context.Background(), store.Device{
		ID: id, Name: id, DeviceType: kind, VoWiFiEnabled: vowifi, NetworkEnabled: false,
	}); err != nil {
		t.Fatal(err)
	}
}

func djiEntry(id string) *device.Device {
	return &device.Device{
		ID: id, Discovered: true,
		Candidate: modem.Candidate{USBPath: "/sys/bus/usb/devices/1-1.2", USBGeneration: "1:8"},
		Snapshot: &device.Snapshot{
			ICCID: "89860012345678901234", IMSI: "310260000000001", Responsive: true,
		},
	}
}

func TestCellBridgeSettingsAuthPasswordAndFailure(t *testing.T) {
	app := newBridgeApp(t, false)
	status, body := app.do(t, http.MethodGet, "/api/settings/cellbridge", nil, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d body %s", status, body)
	}

	app.login(t)
	status, body = app.do(t, http.MethodGet, "/api/settings/cellbridge", nil, false)
	if status != http.StatusOK || !strings.Contains(string(body), `"phase":"disabled"`) {
		t.Fatalf("default status %d body %s", status, body)
	}
	if strings.Contains(string(body), "password") && strings.Contains(string(body), "secret") {
		t.Fatalf("default response leaked a password: %s", body)
	}

	status, _ = app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
		"sip_enabled": false, "password": "secret-1",
	}, false)
	if status != http.StatusForbidden {
		t.Fatalf("missing CSRF status %d", status)
	}

	status, body = app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
		"sip_enabled": false, "password": "secret-1", "listen_addr": "bad",
	}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("bad listen status %d body %s", status, body)
	}

	status, body = app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
		"sip_enabled": true, "sip_device_id": "missing", "listen_addr": "0.0.0.0:5060",
		"advertised_ip": "not-an-ip", "rtp_listen_addr": "0.0.0.0:40000",
		"username": "halo", "password": "secret-1",
	}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("bad ip status %d body %s", status, body)
	}

	upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
	status, body = app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
		"sip_enabled": false, "password": "secret-1",
		"cellular_enabled": true, "cellular_device_id": "dji-1",
	}, true)
	if status != http.StatusOK || strings.Contains(string(body), "secret-1") || !strings.Contains(string(body), `"has_password":true`) {
		t.Fatalf("save status %d body %s", status, body)
	}
	status, body = app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
		"sip_enabled": false, "password": "",
		"cellular_enabled": true, "cellular_device_id": "dji-1",
	}, true)
	if status != http.StatusOK || strings.Contains(string(body), "secret-1") {
		t.Fatalf("blank password status %d body %s", status, body)
	}
	if got := app.handler.loadCellBridgeConfig(context.Background()).Password; got != "secret-1" {
		t.Fatal("blank password did not keep the stored password")
	}
	status, _ = app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
		"sip_enabled": false, "password": store.SecretMask,
		"cellular_enabled": true, "cellular_device_id": "dji-1",
	}, true)
	if status != http.StatusOK {
		t.Fatalf("mask password status %d", status)
	}
	if got := app.handler.loadCellBridgeConfig(context.Background()).Password; got != "secret-1" {
		t.Fatal("masked password replaced the stored password")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.handler.RunCellBridge(ctx)
	deadline := time.Now().Add(3 * time.Second)
	var failed cellBridgeStatus
	for time.Now().Before(deadline) {
		failed = app.handler.cellBridgeStatusSnapshot()
		if failed.Phase == "failed" && failed.Reason != "" && !failed.Cellular.Ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if failed.Phase != "failed" || failed.Reason == "" || failed.Cellular.Ready {
		t.Fatalf("offline module status = %+v", failed)
	}
	if strings.Contains(failed.Reason, "secret-1") {
		t.Fatal("failure reason contains the SIP password")
	}
	status, body = app.do(t, http.MethodGet, "/api/devices", nil, false)
	if status != http.StatusOK || !strings.Contains(string(body), `"call_audio_ready":false`) || !strings.Contains(string(body), `"call_audio_transport":"cellular"`) {
		t.Fatalf("device audio status %d body %s", status, body)
	}
}

func TestCellBridgeCellularBrowserCallRecordingAndContention(t *testing.T) {
	app := newBridgeApp(t, true)
	app.login(t)
	upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
	upsertBridgeDevice(t, app.handler.store, "reader-1", store.DeviceTypeUSBSIMReader, false)
	upsertBridgeDevice(t, app.handler.store, "vowifi-1", store.DeviceTypeDJI4G, true)
	app.modem.entries["dji-1"] = djiEntry("dji-1")
	app.modem.entries["reader-1"] = &device.Device{ID: "reader-1", Discovered: true, Candidate: modem.Candidate{HardwareKind: "pcsc", ReaderName: "reader"}}
	app.modem.entries["vowifi-1"] = djiEntry("vowifi-1")

	status, body := app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
		"sip_enabled": false, "cellular_enabled": true, "cellular_device_id": "dji-1",
		"capture_device": "plughw:1,0", "playback_device": "plughw:1,0",
	}, true)
	if status != http.StatusOK {
		t.Fatalf("save %d %s", status, body)
	}
	app.handler.applyCellBridge(context.Background())
	if ready, reason, transport := app.handler.callAudioCapability(store.Device{ID: "dji-1", DeviceType: store.DeviceTypeDJI4G}); !ready || transport != "cellular" || reason != "" {
		t.Fatalf("audio capability ready=%v transport=%s reason=%s", ready, transport, reason)
	}

	status, body = app.do(t, http.MethodPost, "/api/devices/reader-1/calls/dial", map[string]any{"number": "+15551212"}, true)
	if status != http.StatusConflict || !strings.Contains(string(body), "读卡器") {
		t.Fatalf("reader dial %d %s", status, body)
	}
	status, body = app.do(t, http.MethodPost, "/api/devices/vowifi-1/calls/dial", map[string]any{"number": "+15551212"}, true)
	if status != http.StatusConflict || !strings.Contains(string(body), "VoWiFi") {
		t.Fatalf("vowifi fallback dial %d %s", status, body)
	}
	for _, command := range app.modem.snapshotCommands() {
		if strings.Contains(command, "ATD") || strings.Contains(command, "ATH") || strings.Contains(command, "ATA") {
			t.Fatalf("reader or VoWiFi dial signalled a modem: %s", command)
		}
	}

	status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551212"}, true)
	if status != http.StatusAccepted {
		t.Fatalf("dial %d %s", status, body)
	}
	var dialed struct {
		Data struct {
			CallID string `json:"call_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &dialed); err != nil || dialed.Data.CallID == "" {
		t.Fatalf("dial body %s", body)
	}
	if !commandHas(app.modem.snapshotCommands(), "ATD+15551212;") {
		t.Fatalf("dial commands %v", app.modem.snapshotCommands())
	}
	app.modem.setCLCC("dji-1", `+CLCC: 1,0,0,0,0,"+15551212",145`)
	app.handler.pollCellBridge(context.Background())
	status, body = app.do(t, http.MethodGet, "/api/devices/dji-1/calls", nil, false)
	if status != http.StatusOK || !strings.Contains(string(body), `"state":"active"`) || !strings.Contains(string(body), `"media_ready":true`) {
		t.Fatalf("calls %d %s", status, body)
	}

	dialURL := "ws" + strings.TrimPrefix(app.server.URL, "http") + "/api/devices/dji-1/calls/media?call_id=" + dialed.Data.CallID
	mediaCtx, mediaCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer mediaCancel()
	conn, response, err := websocket.Dial(mediaCtx, dialURL, &websocket.DialOptions{HTTPClient: app.client})
	if err != nil {
		var extra string
		if response != nil {
			raw, _ := io.ReadAll(response.Body)
			extra = string(raw)
		}
		t.Fatalf("media dial: %v %s", err, extra)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, payload, err := conn.Read(readCtx)
	readCancel()
	if err != nil || len(payload) < 2 {
		t.Fatalf("downlink %v %d", err, len(payload))
	}
	uplink := make([]byte, 320)
	sample := int16(-2000)
	for index := 0; index < 160; index++ {
		binary.LittleEndian.PutUint16(uplink[index*2:], uint16(sample))
	}
	if err := conn.Write(mediaCtx, websocket.MessageBinary, uplink); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for app.audio.wrote() == 0 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	if app.audio.wrote() == 0 {
		t.Fatal("browser uplink did not reach the cellular audio")
	}

	backend := &cellularSIPBackend{server: app.handler, deviceID: "dji-1", controller: app.handler.cellularControllerFor("dji-1")}
	if _, _, err := backend.OpenMedia(context.Background(), dialed.Data.CallID, "handset"); !errors.Is(err, errCallMediaBusy) {
		t.Fatalf("SIP media while browser holds the call: %v", err)
	}
	status, body = app.do(t, http.MethodGet, "/api/devices/dji-1/calls/media?call_id="+dialed.Data.CallID, nil, false)
	if status != http.StatusConflict {
		t.Fatalf("second browser media %d %s", status, body)
	}

	status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/hangup", map[string]any{"call_id": dialed.Data.CallID}, true)
	if status != http.StatusAccepted {
		t.Fatalf("hangup %d %s", status, body)
	}
	if !commandHas(app.modem.snapshotCommands(), "ATH") {
		t.Fatalf("hangup did not reach the original module: %v", app.modem.snapshotCommands())
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")

	status, body = app.do(t, http.MethodGet, "/api/calls/history?device_id=dji-1", nil, false)
	if status != http.StatusOK {
		t.Fatalf("history %d %s", status, body)
	}
	var history struct {
		Data struct {
			Records []struct {
				ID        int64  `json:"id"`
				State     string `json:"state"`
				Transport string `json:"transport"`
				Recording string `json:"recording"`
			} `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatal(err)
	}
	var recordID int64
	for _, record := range history.Data.Records {
		if record.Recording != "" && record.State == "answered" && record.Transport == "cellular" {
			recordID = record.ID
			break
		}
	}
	if recordID == 0 {
		t.Fatalf("history missing answered recording: %s", body)
	}
	status, wav := app.do(t, http.MethodGet, "/api/calls/history/"+strconv.FormatInt(recordID, 10)+"/recording", nil, false)
	if status != http.StatusOK {
		t.Fatalf("recording %d %s", status, wav)
	}
	if !wavHasStereoPair(wav, 1000, -2000) {
		t.Fatalf("recording is not an 8 kHz stereo WAV with both call directions, %d bytes", len(wav))
	}
}

func TestCellBridgeIdentityChangeDoesNotSignalReplacement(t *testing.T) {
	t.Run("hangup", func(t *testing.T) {
		app := newBridgeApp(t, true)
		app.login(t)
		upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
		app.modem.entries["dji-1"] = djiEntry("dji-1")
		status, body := app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
			"cellular_enabled": true, "cellular_device_id": "dji-1",
		}, true)
		if status != http.StatusOK {
			t.Fatalf("save %d %s", status, body)
		}
		app.handler.applyCellBridge(context.Background())
		status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551212"}, true)
		if status != http.StatusAccepted {
			t.Fatalf("dial %d %s", status, body)
		}
		var dialed struct {
			Data struct {
				CallID string `json:"call_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &dialed); err != nil {
			t.Fatal(err)
		}
		app.modem.setSnapshot("dji-1", func(snapshot *device.Snapshot) { snapshot.ICCID = "89860099999999999999" })
		app.modem.setGeneration("dji-1", "1:9")
		status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/hangup", map[string]any{"call_id": dialed.Data.CallID}, true)
		if status != http.StatusAccepted && status != http.StatusConflict {
			t.Fatalf("hangup status %d %s", status, body)
		}
		controller := app.handler.cellularControllerFor("dji-1")
		if controller == nil {
			t.Fatal("original controller disappeared before the session could end")
		}
		ended := false
		for _, call := range controller.Calls() {
			if call.ID == dialed.Data.CallID && call.EndedAt != nil {
				ended = true
			}
		}
		if !ended {
			t.Fatal("original session was not ended after the module identity changed")
		}
		for _, command := range app.modem.snapshotCommands() {
			if strings.Contains(command, "ATH") || strings.Contains(command, "ATA") {
				t.Fatalf("replacement module received %s", command)
			}
		}
	})

	t.Run("prepare", func(t *testing.T) {
		app := newBridgeApp(t, true)
		app.login(t)
		app.audio.block = make(chan struct{})
		upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
		app.modem.entries["dji-1"] = djiEntry("dji-1")
		status, body := app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
			"cellular_enabled": true, "cellular_device_id": "dji-1",
		}, true)
		if status != http.StatusOK {
			t.Fatalf("save %d %s", status, body)
		}
		app.handler.applyCellBridge(context.Background())
		done := make(chan int, 1)
		go func() {
			request, err := http.NewRequest(http.MethodPost, app.server.URL+"/api/devices/dji-1/calls/dial", strings.NewReader(`{"number":"+15551212"}`))
			if err != nil {
				done <- -1
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", app.csrf)
			response, err := app.client.Do(request)
			if err != nil {
				done <- -1
				return
			}
			response.Body.Close()
			done <- response.StatusCode
		}()
		select {
		case <-app.audio.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("dial did not reach audio prepare")
		}
		app.modem.setSnapshot("dji-1", func(snapshot *device.Snapshot) { snapshot.ICCID = "89860099999999999999" })
		close(app.audio.block)
		select {
		case status := <-done:
			if status == http.StatusAccepted || status < 0 {
				t.Fatalf("dial status after SIM change = %d", status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("dial did not finish after prepare")
		}
		for _, command := range app.modem.snapshotCommands() {
			if strings.Contains(command, "ATD") || strings.Contains(command, "ATH") {
				t.Fatalf("changed SIM was signalled: %s", command)
			}
		}
	})
}

func TestCanonicalUSBBusPortAndALSAOwnership(t *testing.T) {
	port, err := canonicalUSBBusPort("/sys/bus/usb/devices/1-1.2")
	if err != nil || port != "1-1.2" {
		t.Fatalf("canonical sysfs path = %q %v", port, err)
	}
	port, err = canonicalUSBBusPort("/sys/bus/usb/devices/1-1.2:1.0")
	if err != nil || port != "1-1.2" {
		t.Fatalf("canonical interface path = %q %v", port, err)
	}
	if _, err := canonicalUSBBusPort("/sys/bus/usb/devices/not-a-port"); err == nil {
		t.Fatal("invalid USB path was accepted")
	}
	if _, _, err := parseALSACardRef("default"); err == nil || !strings.Contains(err.Error(), "虚拟") {
		t.Fatalf("default device error = %v", err)
	}

	root := t.TempDir()
	for _, name := range []string{"1-1.2", "2-1"} {
		if err := os.MkdirAll(filepath.Join(root, "bus", "usb", "devices", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for card, usb := range map[string]string{"card1": "1-1.2", "card2": "2-1"} {
		cardDir := filepath.Join(root, "class", "sound", card)
		if err := os.MkdirAll(cardDir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "bus", "usb", "devices", usb)
		if err := os.Symlink(target, filepath.Join(cardDir, "device")); err != nil {
			t.Fatal(err)
		}
	}
	if err := matchALSAUSB(root, "plughw:1,0", "plughw:1,0", "1-1.2"); err != nil {
		t.Fatal(err)
	}
	if err := matchALSAUSB(root, "plughw:1,0", "plughw:2,0", "1-1.2"); err == nil {
		t.Fatal("playback from another USB device was accepted")
	}
}

func TestCellBridgeHotplugGenerationAndDisabledDial(t *testing.T) {
	t.Run("same sim new generation", func(t *testing.T) {
		app := newBridgeApp(t, true)
		app.login(t)
		upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
		app.modem.entries["dji-1"] = djiEntry("dji-1")
		status, body := app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
			"cellular_enabled": true, "cellular_device_id": "dji-1",
		}, true)
		if status != http.StatusOK {
			t.Fatalf("save %d %s", status, body)
		}
		app.handler.applyCellBridge(context.Background())
		status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551212"}, true)
		if status != http.StatusAccepted {
			t.Fatalf("dial %d %s", status, body)
		}
		app.modem.setGeneration("dji-1", "1:9")
		app.handler.refreshCellBridge(context.Background())
		for _, command := range app.modem.snapshotCommands() {
			if strings.Contains(command, "ATH") || strings.Contains(command, "ATA") {
				t.Fatalf("replacement generation received %s", command)
			}
		}
		app.handler.cellBridgeMu.Lock()
		lineGen := ""
		if app.handler.cellBridgeCtrl != nil {
			lineGen = app.handler.cellBridgeCtrl.usbGeneration
		}
		app.handler.cellBridgeMu.Unlock()
		if lineGen != "1:9" {
			t.Fatalf("future line stayed on generation %q", lineGen)
		}
		status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551212"}, true)
		if status != http.StatusAccepted {
			t.Fatalf("future dial %d %s", status, body)
		}
		var dialed struct {
			Data struct {
				CallID string `json:"call_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &dialed); err != nil || dialed.Data.CallID == "" {
			t.Fatalf("future dial body %s", body)
		}
		app.handler.cellBridgeMu.Lock()
		binding := app.handler.cellBridgeBindings[callBindingKey("dji-1", dialed.Data.CallID)]
		app.handler.cellBridgeMu.Unlock()
		if binding.usbGeneration != "1:9" || binding.transport != "cellular" {
			t.Fatalf("future call binding = %+v", binding)
		}
		if !commandHas(app.modem.snapshotCommands(), "ATD+15551212;") {
			t.Fatal("future call was not placed on the new generation")
		}
	})

	t.Run("known generation becomes empty", func(t *testing.T) {
		app := newBridgeApp(t, true)
		app.login(t)
		upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
		app.modem.entries["dji-1"] = djiEntry("dji-1")
		status, body := app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
			"cellular_enabled": true, "cellular_device_id": "dji-1",
		}, true)
		if status != http.StatusOK {
			t.Fatalf("save %d %s", status, body)
		}
		app.handler.applyCellBridge(context.Background())
		status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551212"}, true)
		if status != http.StatusAccepted {
			t.Fatalf("dial %d %s", status, body)
		}
		app.modem.setGeneration("dji-1", "")
		app.handler.refreshCellBridge(context.Background())
		for _, command := range app.modem.snapshotCommands() {
			if strings.Contains(command, "ATH") || strings.Contains(command, "ATA") {
				t.Fatalf("missing generation received %s", command)
			}
		}
		app.modem.setGeneration("dji-1", "1:10")
		app.handler.refreshCellBridge(context.Background())
		app.handler.cellBridgeMu.Lock()
		lineGen := ""
		if app.handler.cellBridgeCtrl != nil {
			lineGen = app.handler.cellBridgeCtrl.usbGeneration
		}
		app.handler.cellBridgeMu.Unlock()
		if lineGen != "1:10" {
			t.Fatalf("line after replug = %q", lineGen)
		}
		status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551213"}, true)
		if status != http.StatusAccepted {
			t.Fatalf("replug dial %d %s", status, body)
		}
	})

	t.Run("disable rejects new dial and cleanup still hangs up", func(t *testing.T) {
		app := newBridgeApp(t, true)
		app.login(t)
		upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
		app.modem.entries["dji-1"] = djiEntry("dji-1")
		status, body := app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
			"sip_enabled": true, "sip_device_id": "dji-1",
			"listen_addr": "127.0.0.1:0", "advertised_ip": "127.0.0.1",
			"rtp_listen_addr": "127.0.0.1:0", "username": "halo", "password": "secret-1",
			"cellular_enabled": true, "cellular_device_id": "dji-1",
		}, true)
		if status != http.StatusOK {
			t.Fatalf("save %d %s", status, body)
		}
		app.handler.applyCellBridge(context.Background())
		t.Cleanup(app.handler.stopCellBridge)
		status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551212"}, true)
		if status != http.StatusAccepted {
			t.Fatalf("dial %d %s", status, body)
		}
		app.handler.cellBridgeMu.Lock()
		backend, _ := app.handler.cellBridgeSIP.backend.(*cellularSIPBackend)
		app.handler.cellBridgeMu.Unlock()
		if backend == nil {
			t.Fatal("cellular SIP backend was not installed")
		}
		dials := countCommands(app.modem.snapshotCommands(), "ATD")
		status, body = app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
			"sip_enabled": false, "password": store.SecretMask,
			"cellular_enabled": false, "cellular_device_id": "dji-1",
		}, true)
		if status != http.StatusOK {
			t.Fatalf("disable %d %s", status, body)
		}
		if _, err := backend.Dial(context.Background(), "+15559999"); err == nil {
			t.Fatal("saved disable still accepted a new dial")
		}
		if countCommands(app.modem.snapshotCommands(), "ATD") != dials {
			t.Fatal("dial after disable signalled the modem")
		}
		app.handler.applyCellBridge(context.Background())
		if !commandHas(app.modem.snapshotCommands(), "ATH") {
			t.Fatalf("cleanup did not hang up the original call: %v", app.modem.snapshotCommands())
		}
		if countCommands(app.modem.snapshotCommands(), "ATD") != dials {
			t.Fatal("cleanup placed a new call")
		}
	})
}

func TestCellBridgeSIPTargetAndListenerRecovery(t *testing.T) {
	app := newBridgeApp(t, false)
	app.login(t)
	upsertBridgeDevice(t, app.handler.store, "reader-a", store.DeviceTypeUSBSIMReader, false)
	upsertBridgeDevice(t, app.handler.store, "reader-b", store.DeviceTypeUSBSIMReader, false)
	save := func(deviceID string) {
		t.Helper()
		status, body := app.do(t, http.MethodPut, "/api/settings/cellbridge", map[string]any{
			"sip_enabled": true, "sip_device_id": deviceID,
			"listen_addr": "127.0.0.1:0", "advertised_ip": "127.0.0.1",
			"rtp_listen_addr": "127.0.0.1:0", "username": "halo", "password": "secret-1",
		}, true)
		if status != http.StatusOK {
			t.Fatalf("save %s %d %s", deviceID, status, body)
		}
	}
	save("reader-a")
	app.handler.applyCellBridge(context.Background())
	t.Cleanup(app.handler.stopCellBridge)
	app.handler.cellBridgeMu.Lock()
	first := app.handler.cellBridgeSIP
	app.handler.cellBridgeMu.Unlock()
	backend, _ := first.backend.(*vowifiSIPBackend)
	if first.server == nil || backend == nil || backend.deviceID != "reader-a" {
		t.Fatalf("first SIP backend = %T %+v", first.backend, backend)
	}
	save("reader-b")
	app.handler.applyCellBridge(context.Background())
	app.handler.cellBridgeMu.Lock()
	second := app.handler.cellBridgeSIP
	app.handler.cellBridgeMu.Unlock()
	next, _ := second.backend.(*vowifiSIPBackend)
	if second.server == nil || second.server == first.server || next == nil || next.deviceID != "reader-b" {
		t.Fatalf("SIP target change kept the old backend: %+v", next)
	}
	if !app.handler.sipBackendStale(cellBridgeSIP{backend: backend}, app.handler.loadCellBridgeConfig(context.Background()), nil) {
		t.Fatal("same SIP options on a different device were not stale")
	}

	_ = second.server.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !second.listenerStopped() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !second.listenerStopped() {
		t.Fatal("closed SIP listener was still reported running")
	}
	app.handler.refreshCellBridge(context.Background())
	app.handler.cellBridgeMu.Lock()
	recovered := app.handler.cellBridgeSIP
	app.handler.cellBridgeMu.Unlock()
	if recovered.server == nil || recovered.server == second.server {
		t.Fatal("failed listener was not restarted")
	}

	config := app.handler.loadCellBridgeConfig(context.Background())
	config.SIPDeviceID = "missing-reader"
	if err := app.handler.saveCellBridgeConfig(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	app.handler.refreshCellBridge(context.Background())
	app.handler.cellBridgeMu.Lock()
	missing := app.handler.cellBridgeSIP.server
	app.handler.cellBridgeMu.Unlock()
	if missing != nil {
		t.Fatal("SIP stayed up for a device that is not in the store")
	}
	config.SIPDeviceID = "reader-b"
	if err := app.handler.saveCellBridgeConfig(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	app.handler.refreshCellBridge(context.Background())
	app.handler.cellBridgeMu.Lock()
	restored := app.handler.cellBridgeSIP
	app.handler.cellBridgeMu.Unlock()
	restoredBackend, _ := restored.backend.(*vowifiSIPBackend)
	if restored.server == nil || restoredBackend == nil || restoredBackend.deviceID != "reader-b" {
		t.Fatal("SIP did not come back when the reader returned")
	}
}

func TestLegacyBindingFreezesUSBGeneration(t *testing.T) {
	app := newBridgeApp(t, false)
	app.login(t)
	upsertBridgeDevice(t, app.handler.store, "dji-1", store.DeviceTypeDJI4G, false)
	app.modem.entries["dji-1"] = djiEntry("dji-1")
	status, body := app.do(t, http.MethodPost, "/api/devices/dji-1/calls/dial", map[string]any{"number": "+15551212"}, true)
	if status != http.StatusAccepted {
		t.Fatalf("legacy dial %d %s", status, body)
	}
	if !commandHas(app.modem.snapshotCommands(), "ATD+15551212;") {
		t.Fatalf("legacy commands %v", app.modem.snapshotCommands())
	}
	app.handler.cellBridgeMu.Lock()
	frozen := false
	for _, binding := range app.handler.cellBridgeBindings {
		if binding.deviceID == "dji-1" && binding.transport == "legacy" && binding.usbGeneration == "1:8" && !binding.ended {
			frozen = true
		}
	}
	app.handler.cellBridgeMu.Unlock()
	if !frozen {
		t.Fatal("legacy call did not freeze the USB generation")
	}
	app.modem.setGeneration("dji-1", "1:9")
	status, body = app.do(t, http.MethodPost, "/api/devices/dji-1/calls/hangup", map[string]any{}, true)
	if status != http.StatusConflict {
		t.Fatalf("legacy hangup %d %s", status, body)
	}
	for _, command := range app.modem.snapshotCommands() {
		if strings.Contains(command, "ATH") || strings.Contains(command, "ATA") {
			t.Fatalf("reused physical id received %s", command)
		}
	}

	app.handler.rememberCall(callBinding{deviceID: "dji-1", callID: "ims-1", transport: "vowifi"})
	app.handler.rememberCall(callBinding{deviceID: "dji-1", callID: "ims-1", transport: "legacy", physicalID: "dji-1"})
	binding, ok := app.handler.bindingForAction("dji-1", "ims-1")
	if !ok || binding.transport != "vowifi" {
		t.Fatalf("IMS binding changed transport: %+v", binding)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/hangup", nil)
	app.handler.serveLegacyBoundAction(recorder, request, store.Device{ID: "dji-1"}, binding, "hangup", "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("IMS hangup via legacy status %d", recorder.Code)
	}
	for _, command := range app.modem.snapshotCommands() {
		if strings.Contains(command, "ATH") || strings.Contains(command, "ATA") {
			t.Fatalf("IMS binding sent %s", command)
		}
	}
}

func countCommands(commands []string, fragment string) int {
	count := 0
	for _, command := range commands {
		if strings.Contains(command, fragment) {
			count++
		}
	}
	return count
}

func commandHas(commands []string, fragment string) bool {
	for _, command := range commands {
		if strings.Contains(command, fragment) {
			return true
		}
	}
	return false
}

func wavHasStereoPair(raw []byte, downlink, uplink int16) bool {
	if len(raw) < 44 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return false
	}
	if binary.LittleEndian.Uint16(raw[22:24]) != 2 || binary.LittleEndian.Uint32(raw[24:28]) != 8000 || binary.LittleEndian.Uint16(raw[34:36]) != 16 {
		return false
	}
	data := raw[44:]
	found := false
	for offset := 0; offset+4 <= len(data); offset += 4 {
		left := int16(binary.LittleEndian.Uint16(data[offset:]))
		right := int16(binary.LittleEndian.Uint16(data[offset+2:]))
		if left == downlink && right == uplink {
			found = true
			break
		}
	}
	return found
}
