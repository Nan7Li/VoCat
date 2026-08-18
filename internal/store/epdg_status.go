package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EPDGProbeStatus is the last ePDG UDP/500+4500 health-check outcome for a
// device. It powers the device page's "why is VoWiFi off" explanation.
type EPDGProbeStatus struct {
	DeviceID       string
	ICCID          string
	EPDG           string
	Port500OK      bool
	Port4500OK     bool
	RTT500MS       int64
	RTT4500MS      int64
	Error          string
	CheckedAt      time.Time
	LastSuccessAt  *time.Time
	LastFailureAt  *time.Time
	DisabledVoWiFi bool // probe failure caused an automatic VoWiFi disable
}

// SaveEPDGProbeStatus upserts the latest probe outcome for a device.
func (s *Store) SaveEPDGProbeStatus(ctx context.Context, value EPDGProbeStatus) error {
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.ICCID = strings.TrimSpace(value.ICCID)
	value.EPDG = strings.TrimSpace(value.EPDG)
	if value.DeviceID == "" {
		return errors.New("epdg probe status device id is required")
	}
	now := value.CheckedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ok := value.Port500OK && value.Port4500OK
	_, err := s.db.ExecContext(ctx, `
INSERT INTO epdg_probe_status (
	device_id, iccid, epdg, port_500_ok, port_4500_ok, rtt_500_ms, rtt_4500_ms,
	error, checked_at, last_success_at, last_failure_at, disabled_vowifi
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
	iccid = excluded.iccid,
	epdg = excluded.epdg,
	port_500_ok = excluded.port_500_ok,
	port_4500_ok = excluded.port_4500_ok,
	rtt_500_ms = excluded.rtt_500_ms,
	rtt_4500_ms = excluded.rtt_4500_ms,
	error = excluded.error,
	checked_at = excluded.checked_at,
	last_success_at = excluded.last_success_at,
	last_failure_at = excluded.last_failure_at,
	disabled_vowifi = excluded.disabled_vowifi`,
		value.DeviceID, value.ICCID, value.EPDG, value.Port500OK, value.Port4500OK,
		value.RTT500MS, value.RTT4500MS, value.Error,
		now.Unix(),
		probeTime(value.LastSuccessAt, ok, now),
		probeTime(value.LastFailureAt, !ok, now),
		value.DisabledVoWiFi,
	)
	if err != nil {
		return fmt.Errorf("save epdg probe status: %w", err)
	}
	return nil
}

func probeTime(value *time.Time, matches bool, fallback time.Time) any {
	if matches {
		if value != nil {
			return value.Unix()
		}
		return fallback.Unix()
	}
	return nil
}

// EPDGProbeStatus returns the latest probe outcome for a device.
func (s *Store) EPDGProbeStatus(ctx context.Context, deviceID string) (EPDGProbeStatus, error) {
	var value EPDGProbeStatus
	var checked int64
	var success, failure sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT device_id, iccid, epdg, port_500_ok, port_4500_ok, rtt_500_ms, rtt_4500_ms,
	error, checked_at, last_success_at, last_failure_at, disabled_vowifi
FROM epdg_probe_status WHERE device_id = ?`, strings.TrimSpace(deviceID)).Scan(
		&value.DeviceID, &value.ICCID, &value.EPDG,
		&value.Port500OK, &value.Port4500OK, &value.RTT500MS, &value.RTT4500MS,
		&value.Error, &checked, &success, &failure, &value.DisabledVoWiFi,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return EPDGProbeStatus{}, ErrNotFound
	}
	if err != nil {
		return EPDGProbeStatus{}, fmt.Errorf("load epdg probe status: %w", err)
	}
	value.CheckedAt = time.Unix(checked, 0).UTC()
	if success.Valid {
		t := time.Unix(success.Int64, 0).UTC()
		value.LastSuccessAt = &t
	}
	if failure.Valid {
		t := time.Unix(failure.Int64, 0).UTC()
		value.LastFailureAt = &t
	}
	return value, nil
}
