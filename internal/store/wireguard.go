package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const wireGuardTunnelSelect = `
	SELECT id, name, interface, config_text, autostart, created_at, updated_at
	FROM wireguard_tunnels`

var wireGuardSecretLine = regexp.MustCompile(`(?im)^\s*(PrivateKey|PresharedKey)\s*=\s*(\S+)\s*$`)

func (s *Store) SaveWireGuardTunnel(ctx context.Context, value WireGuardTunnel) (WireGuardTunnel, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.Name = strings.TrimSpace(value.Name)
	value.Interface = strings.TrimSpace(value.Interface)
	value.Config = strings.ReplaceAll(value.Config, "\r\n", "\n")
	if value.ID == "" {
		return WireGuardTunnel{}, errors.New("wireguard tunnel id is required")
	}
	if value.Name == "" {
		return WireGuardTunnel{}, errors.New("wireguard tunnel name is required")
	}
	if value.Interface == "" {
		return WireGuardTunnel{}, errors.New("wireguard tunnel interface is required")
	}
	if strings.TrimSpace(value.Config) == "" {
		return WireGuardTunnel{}, errors.New("wireguard tunnel config is required")
	}
	now := time.Now().UTC()
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO wireguard_tunnels (
			id, name, interface, config_text, autostart, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			interface = excluded.interface,
			config_text = excluded.config_text,
			autostart = excluded.autostart,
			updated_at = excluded.updated_at
	`, value.ID, value.Name, value.Interface, value.Config, boolInt(value.Autostart),
		value.CreatedAt.Unix(), value.UpdatedAt.Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return WireGuardTunnel{}, fmt.Errorf("wireguard interface %q is already in use", value.Interface)
		}
		return WireGuardTunnel{}, fmt.Errorf("save wireguard tunnel %q: %w", value.ID, err)
	}
	return s.WireGuardTunnel(ctx, value.ID)
}

func (s *Store) WireGuardTunnel(ctx context.Context, id string) (WireGuardTunnel, error) {
	return scanWireGuardTunnel(s.db.QueryRowContext(
		ctx,
		wireGuardTunnelSelect+` WHERE id = ?`,
		strings.TrimSpace(id),
	))
}

func (s *Store) WireGuardTunnelByInterface(ctx context.Context, iface string) (WireGuardTunnel, error) {
	return scanWireGuardTunnel(s.db.QueryRowContext(
		ctx,
		wireGuardTunnelSelect+` WHERE interface = ?`,
		strings.TrimSpace(iface),
	))
}

func (s *Store) ListWireGuardTunnels(ctx context.Context) ([]WireGuardTunnel, error) {
	rows, err := s.db.QueryContext(ctx, wireGuardTunnelSelect+` ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list wireguard tunnels: %w", err)
	}
	defer rows.Close()
	values := make([]WireGuardTunnel, 0)
	for rows.Next() {
		value, err := scanWireGuardTunnel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan wireguard tunnel: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wireguard tunnels: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteWireGuardTunnel(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM wireguard_tunnels WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("delete wireguard tunnel %q: %w", id, err)
	}
	return requireAffected(result)
}

func scanWireGuardTunnel(row rowScanner) (WireGuardTunnel, error) {
	var value WireGuardTunnel
	var autostart int
	var createdAt, updatedAt int64
	err := row.Scan(
		&value.ID, &value.Name, &value.Interface, &value.Config,
		&autostart, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return WireGuardTunnel{}, ErrNotFound
	}
	if err != nil {
		return WireGuardTunnel{}, err
	}
	value.Autostart = autostart != 0
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func wireGuardSecrets(config string) []string {
	matches := wireGuardSecretLine.FindAllStringSubmatch(config, -1)
	if len(matches) == 0 {
		return nil
	}
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		secret := strings.TrimSpace(match[2])
		if secret != "" && secret != SecretMask {
			values = append(values, secret)
		}
	}
	return uniqueNonemptyStrings(values)
}