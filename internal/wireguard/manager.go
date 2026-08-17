package wireguard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vocat/internal/store"
)

var (
	ErrNotFound  = errors.New("wireguard: tunnel not found")
	ErrBusy      = errors.New("wireguard: another operation is already in progress")
	ErrAvailable = errors.New("wireguard: wg-quick is not available on this host")
)

// Status is the operator-facing view of one stored tunnel plus live wg state.
type Status struct {
	store.WireGuardTunnel
	Available        bool   `json:"available"`
	Running          bool   `json:"running"`
	PublicKey        string `json:"public_key,omitempty"`
	ListenPort       int    `json:"listen_port,omitempty"`
	Peers            int    `json:"peers"`
	TransferRX       int64  `json:"transfer_rx"`
	TransferTX       int64  `json:"transfer_tx"`
	Endpoint         string `json:"endpoint,omitempty"`
	LatestHandshake  string `json:"latest_handshake,omitempty"`
	Error            string `json:"error,omitempty"`
	External         bool   `json:"external"`
}

type Snapshot struct {
	Available bool     `json:"available"`
	Hint      string   `json:"hint,omitempty"`
	Tunnels   []Status `json:"tunnels"`
}

type InterfaceInfo struct {
	Running         bool
	PublicKey       string
	ListenPort      int
	Peers           int
	TransferRX      int64
	TransferTX      int64
	Endpoint        string
	LatestHandshake string
}

type Runner interface {
	Available() bool
	List(ctx context.Context) ([]string, error)
	Up(ctx context.Context, iface, configPath string) error
	Down(ctx context.Context, iface, configPath string) error
	Show(ctx context.Context, iface string) (InterfaceInfo, error)
}

type Manager struct {
	store  *store.Store
	dir    string
	logger *slog.Logger
	run    Runner
	mu     sync.Mutex
}

func New(database *store.Store, dir string, logger *slog.Logger) (*Manager, error) {
	return NewWithRunner(database, dir, logger, defaultRunner())
}

func NewWithRunner(database *store.Store, dir string, logger *slog.Logger, runner Runner) (*Manager, error) {
	if database == nil {
		return nil, errors.New("wireguard: store is required")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("wireguard: config directory is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if runner == nil {
		runner = defaultRunner()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("wireguard: create config dir: %w", err)
	}
	return &Manager{store: database, dir: dir, logger: logger, run: runner}, nil
}

func (m *Manager) Available() bool {
	return m != nil && m.run != nil && m.run.Available()
}

func (m *Manager) Snapshot(ctx context.Context) (Snapshot, error) {
	tunnels, err := m.store.ListWireGuardTunnels(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	available := m.Available()
	out := Snapshot{
		Available: available,
		Tunnels:   make([]Status, 0, len(tunnels)),
	}
	if !available {
		out.Hint = "Install WireGuard on this Linux host (kmod-wireguard plus wg and ip). wg-quick is optional."
	}
	known := make(map[string]struct{}, len(tunnels))
	for _, tunnel := range tunnels {
		known[tunnel.Interface] = struct{}{}
		out.Tunnels = append(out.Tunnels, m.statusOf(ctx, tunnel, available))
	}
	live, err := m.run.List(ctx)
	if err != nil && available {
		m.logger.Warn("list live wireguard interfaces", "error", err)
	}
	for _, iface := range live {
		iface = strings.TrimSpace(iface)
		if iface == "" {
			continue
		}
		if _, exists := known[iface]; exists {
			continue
		}
		out.Tunnels = append(out.Tunnels, m.liveStatus(ctx, iface, available))
	}
	return out, nil
}

func isLiveTunnelID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), "live:")
}

func (m *Manager) Get(ctx context.Context, id string) (Status, error) {
	if isLiveTunnelID(id) {
		iface := strings.TrimPrefix(strings.TrimSpace(id), "live:")
		if iface == "" {
			return Status{}, ErrNotFound
		}
		return m.liveStatus(ctx, iface, m.Available()), nil
	}
	tunnel, err := m.store.WireGuardTunnel(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Status{}, ErrNotFound
		}
		return Status{}, err
	}
	return m.statusOf(ctx, tunnel, m.Available()), nil
}

type SaveRequest struct {
	ID        string
	Name      string
	Interface string
	Config    string
	Autostart bool
}

func (m *Manager) Create(ctx context.Context, request SaveRequest) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, err := m.store.ListWireGuardTunnels(ctx)
	if err != nil {
		return Status{}, err
	}
	used := make([]string, 0, len(existing))
	for _, tunnel := range existing {
		used = append(used, tunnel.Interface)
	}
	iface := strings.TrimSpace(request.Interface)
	if iface == "" {
		iface = nextInterfaceName(used)
	}
	if err := validateInterfaceName(iface); err != nil {
		return Status{}, err
	}
	if _, err := m.store.WireGuardTunnelByInterface(ctx, iface); err == nil {
		return Status{}, fmt.Errorf("interface %q is already used by another tunnel", iface)
	} else if !errors.Is(err, store.ErrNotFound) {
		return Status{}, err
	}
	if _, err := parseConfig(request.Config); err != nil {
		return Status{}, err
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		id = newID()
	}
	saved, err := m.store.SaveWireGuardTunnel(ctx, store.WireGuardTunnel{
		ID:        id,
		Name:      strings.TrimSpace(request.Name),
		Interface: iface,
		Config:    request.Config,
		Autostart: request.Autostart,
	})
	if err != nil {
		return Status{}, err
	}
	if err := m.writeConfigFile(saved); err != nil {
		return Status{}, err
	}
	return m.statusOf(ctx, saved, m.Available()), nil
}

func (m *Manager) Update(ctx context.Context, id string, request SaveRequest) (Status, error) {
	if isLiveTunnelID(id) {
		return Status{}, fmt.Errorf("tunnel %q is managed by the host and cannot be edited here", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.store.WireGuardTunnel(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Status{}, ErrNotFound
		}
		return Status{}, err
	}
	iface := strings.TrimSpace(request.Interface)
	if iface == "" {
		iface = current.Interface
	}
	if err := validateInterfaceName(iface); err != nil {
		return Status{}, err
	}
	if iface != current.Interface {
		if other, err := m.store.WireGuardTunnelByInterface(ctx, iface); err == nil && other.ID != current.ID {
			return Status{}, fmt.Errorf("interface %q is already used by another tunnel", iface)
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Status{}, err
		}
	}
	config, err := mergeMaskedConfig(request.Config, current.Config)
	if err != nil {
		return Status{}, err
	}
	if _, err := parseConfig(config); err != nil {
		return Status{}, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = current.Name
	}
	wasRunning := m.interfaceRunning(ctx, current.Interface)
	if wasRunning && (iface != current.Interface || config != current.Config) {
		if err := m.bringDownLocked(ctx, current); err != nil {
			return Status{}, err
		}
	}
	saved, err := m.store.SaveWireGuardTunnel(ctx, store.WireGuardTunnel{
		ID:        current.ID,
		Name:      name,
		Interface: iface,
		Config:    config,
		Autostart: request.Autostart,
		CreatedAt: current.CreatedAt,
	})
	if err != nil {
		return Status{}, err
	}
	if iface != current.Interface {
		_ = os.Remove(m.configPath(current.Interface))
	}
	if err := m.writeConfigFile(saved); err != nil {
		return Status{}, err
	}
	if wasRunning && (iface != current.Interface || config != current.Config) {
		if err := m.bringUpLocked(ctx, saved); err != nil {
			return Status{}, err
		}
	}
	return m.statusOf(ctx, saved, m.Available()), nil
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	if isLiveTunnelID(id) {
		return fmt.Errorf("tunnel %q is managed by the host and cannot be deleted here", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.store.WireGuardTunnel(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if m.interfaceRunning(ctx, current.Interface) {
		if err := m.bringDownLocked(ctx, current); err != nil {
			return err
		}
	}
	if err := m.store.DeleteWireGuardTunnel(ctx, current.ID); err != nil {
		return err
	}
	_ = os.Remove(m.configPath(current.Interface))
	return nil
}

func (m *Manager) Up(ctx context.Context, id string) (Status, error) {
	if isLiveTunnelID(id) {
		return Status{}, fmt.Errorf("tunnel %q is managed by the host; start it from the router network config", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.store.WireGuardTunnel(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Status{}, ErrNotFound
		}
		return Status{}, err
	}
	if err := m.bringUpLocked(ctx, current); err != nil {
		return Status{}, err
	}
	return m.statusOf(ctx, current, true), nil
}

func (m *Manager) Down(ctx context.Context, id string) (Status, error) {
	if isLiveTunnelID(id) {
		return Status{}, fmt.Errorf("tunnel %q is managed by the host; stop it from the router network config", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.store.WireGuardTunnel(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Status{}, ErrNotFound
		}
		return Status{}, err
	}
	if err := m.bringDownLocked(ctx, current); err != nil {
		return Status{}, err
	}
	return m.statusOf(ctx, current, m.Available()), nil
}

func (m *Manager) Restore(ctx context.Context) {
	if m == nil {
		return
	}
	tunnels, err := m.store.ListWireGuardTunnels(ctx)
	if err != nil {
		m.logger.Warn("list wireguard tunnels for restore", "error", err)
		return
	}
	if !m.Available() {
		if len(tunnels) > 0 {
			m.logger.Info("wireguard tools are not installed; stored tunnels were not started")
		}
		return
	}
	for _, tunnel := range tunnels {
		if ctx.Err() != nil {
			return
		}
		if !tunnel.Autostart {
			continue
		}
		m.mu.Lock()
		err := m.bringUpLocked(ctx, tunnel)
		m.mu.Unlock()
		if err != nil {
			m.logger.Warn("autostart wireguard tunnel failed", "id", tunnel.ID, "interface", tunnel.Interface, "error", err)
			continue
		}
		m.logger.Info("started wireguard tunnel", "id", tunnel.ID, "interface", tunnel.Interface)
	}
}

func (m *Manager) liveStatus(ctx context.Context, iface string, available bool) Status {
	status := Status{
		WireGuardTunnel: store.WireGuardTunnel{
			ID:        "live:" + iface,
			Name:      iface,
			Interface: iface,
		},
		Available: available,
		External:  true,
	}
	if !available {
		return status
	}
	info, err := m.run.Show(ctx, iface)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Running = info.Running
	status.PublicKey = info.PublicKey
	status.ListenPort = info.ListenPort
	status.Peers = info.Peers
	status.TransferRX = info.TransferRX
	status.TransferTX = info.TransferTX
	status.Endpoint = info.Endpoint
	status.LatestHandshake = info.LatestHandshake
	return status
}

func (m *Manager) statusOf(ctx context.Context, tunnel store.WireGuardTunnel, available bool) Status {
	status := Status{
		WireGuardTunnel: tunnel,
		Available:       available,
	}
	status.Config = redactConfig(tunnel.Config)
	if !available {
		return status
	}
	info, err := m.run.Show(ctx, tunnel.Interface)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Running = info.Running
	status.PublicKey = info.PublicKey
	status.ListenPort = info.ListenPort
	status.Peers = info.Peers
	status.TransferRX = info.TransferRX
	status.TransferTX = info.TransferTX
	status.Endpoint = info.Endpoint
	status.LatestHandshake = info.LatestHandshake
	return status
}

func (m *Manager) interfaceRunning(ctx context.Context, iface string) bool {
	if !m.Available() {
		return false
	}
	info, err := m.run.Show(ctx, iface)
	return err == nil && info.Running
}

func (m *Manager) bringUpLocked(ctx context.Context, tunnel store.WireGuardTunnel) error {
	if !m.Available() {
		return ErrAvailable
	}
	if err := m.writeConfigFile(tunnel); err != nil {
		return err
	}
	if m.interfaceRunning(ctx, tunnel.Interface) {
		return nil
	}
	if err := m.run.Up(ctx, tunnel.Interface, m.configPath(tunnel.Interface)); err != nil {
		return err
	}
	return nil
}

func (m *Manager) bringDownLocked(ctx context.Context, tunnel store.WireGuardTunnel) error {
	if !m.Available() {
		return ErrAvailable
	}
	if !m.interfaceRunning(ctx, tunnel.Interface) {
		return nil
	}
	path := m.configPath(tunnel.Interface)
	if _, err := os.Stat(path); err != nil {
		if err := m.writeConfigFile(tunnel); err != nil {
			return err
		}
	}
	return m.run.Down(ctx, tunnel.Interface, path)
}

func (m *Manager) writeConfigFile(tunnel store.WireGuardTunnel) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return fmt.Errorf("wireguard: ensure config dir: %w", err)
	}
	path := m.configPath(tunnel.Interface)
	tmp, err := os.CreateTemp(m.dir, ".halo-wg-*")
	if err != nil {
		return fmt.Errorf("wireguard: create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(strings.TrimSpace(tunnel.Config) + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("wireguard: write temp config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("wireguard: chmod temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("wireguard: close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("wireguard: install config: %w", err)
	}
	return nil
}

func (m *Manager) configPath(iface string) string {
	return filepath.Join(m.dir, iface+".conf")
}

func newID() string {
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("wg%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}
