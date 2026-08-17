package wireguard

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"vocat/internal/store"
)

type fakeRunner struct {
	mu      sync.Mutex
	up      map[string]bool
	ups     int
	downs   int
}

func (f *fakeRunner) Available() bool { return true }

func (f *fakeRunner) List(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.up))
	for iface, running := range f.up {
		if running {
			names = append(names, iface)
		}
	}
	return names, nil
}

func (f *fakeRunner) Up(_ context.Context, iface, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.up == nil {
		f.up = map[string]bool{}
	}
	f.up[iface] = true
	f.ups++
	return nil
}

func (f *fakeRunner) Down(_ context.Context, iface, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.up == nil {
		f.up = map[string]bool{}
	}
	f.up[iface] = false
	f.downs++
	return nil
}

func (f *fakeRunner) Show(_ context.Context, iface string) (InterfaceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return InterfaceInfo{Running: f.up[iface], PublicKey: "pub", Peers: 1}, nil
}

func testManager(t *testing.T) (*Manager, *fakeRunner) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &fakeRunner{up: map[string]bool{}}
	manager, err := NewWithRunner(database, filepath.Join(t.TempDir(), "wg"), nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	return manager, runner
}

func TestManagerCreateUpDown(t *testing.T) {
	manager, runner := testManager(t)
	ctx := context.Background()
	created, err := manager.Create(ctx, SaveRequest{
		Name:      "Home",
		Config:    sampleConfig(),
		Autostart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Interface != "halo0" || created.Running {
		t.Fatalf("created = %#v", created)
	}
	up, err := manager.Up(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !up.Running || runner.ups != 1 {
		t.Fatalf("up = %#v ups=%d", up, runner.ups)
	}
	down, err := manager.Down(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if down.Running || runner.downs != 1 {
		t.Fatalf("down = %#v downs=%d", down, runner.downs)
	}
}

func TestManagerSnapshotIncludesLiveHostTunnels(t *testing.T) {
	manager, runner := testManager(t)
	runner.up["vocatwg"] = true
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tunnels) != 1 || snapshot.Tunnels[0].ID != "live:vocatwg" || !snapshot.Tunnels[0].External || !snapshot.Tunnels[0].Running {
		t.Fatalf("snapshot = %#v", snapshot.Tunnels)
	}
}

func TestManagerRestoreAutostart(t *testing.T) {
	manager, runner := testManager(t)
	ctx := context.Background()
	if _, err := manager.Create(ctx, SaveRequest{Name: "Home", Config: sampleConfig(), Autostart: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(ctx, SaveRequest{Name: "Office", Interface: "halo1", Config: sampleConfig(), Autostart: false}); err != nil {
		t.Fatal(err)
	}
	manager.Restore(ctx)
	if runner.ups != 1 || !runner.up["halo0"] || runner.up["halo1"] {
		t.Fatalf("restore ups=%d state=%v", runner.ups, runner.up)
	}
}
