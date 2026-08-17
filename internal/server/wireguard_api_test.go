package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vocat/internal/store"
	"vocat/internal/wireguard"
)

const testWGConfig = `[Interface]
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.8.0.2/32

[Peer]
PublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=
Endpoint = 203.0.113.10:51820
AllowedIPs = 0.0.0.0/0
`

type recordingWGRunner struct {
	up map[string]bool
}

func (r *recordingWGRunner) Available() bool { return true }
func (r *recordingWGRunner) List(context.Context) ([]string, error) { return nil, nil }
func (r *recordingWGRunner) Up(_ context.Context, iface, _ string) error {
	if r.up == nil {
		r.up = map[string]bool{}
	}
	r.up[iface] = true
	return nil
}
func (r *recordingWGRunner) Down(_ context.Context, iface, _ string) error {
	if r.up == nil {
		r.up = map[string]bool{}
	}
	r.up[iface] = false
	return nil
}
func (r *recordingWGRunner) Show(_ context.Context, iface string) (wireguard.InterfaceInfo, error) {
	return wireguard.InterfaceInfo{Running: r.up[iface]}, nil
}

func TestWireGuardAPICreateAndUp(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager, err := wireguard.NewWithRunner(
		database,
		filepath.Join(t.TempDir(), "wg"),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		&recordingWGRunner{up: map[string]bool{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store:               database,
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRequestBodyBytes: 1 << 20,
		wireguard:           manager,
	}

	create := httptest.NewRecorder()
	body := `{"name":"Home","config":` + jsonString(testWGConfig) + `,"autostart":true}`
	request := httptest.NewRequest(http.MethodPost, "/api/wireguard-tunnels", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if !server.routeWireGuardAPI(create, request, "wireguard-tunnels") {
		t.Fatal("route missed")
	}
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body)
	}
	var created struct {
		Data struct {
			ID        string `json:"id"`
			Interface string `json:"interface"`
			Config    string `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Interface != "halo0" || strings.Contains(created.Data.Config, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") {
		t.Fatalf("created leaked or wrong iface: %#v", created.Data)
	}

	up := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/wireguard-tunnels/"+created.Data.ID+"/up", nil)
	if !server.routeWireGuardAPI(up, request, "wireguard-tunnels/"+created.Data.ID+"/up") {
		t.Fatal("up route missed")
	}
	if up.Code != http.StatusOK {
		t.Fatalf("up status = %d, body = %s", up.Code, up.Body)
	}
}

func jsonString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
