package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CallRecord is one persisted IMS/CS call in the phone history. A record is
// created when a call appears and finalised when it reaches a terminal state.
type CallRecord struct {
	ID              int64
	DeviceID        string
	CallID          string // IMS dialog id (stable across polls); "" for CS calls
	Number          string
	Direction       string // incoming | outgoing
	State           string // active | answered | missed | failed | ended
	StartedAt       time.Time
	AnsweredAt      *time.Time
	EndedAt         *time.Time
	DurationSeconds int
	Transport       string // vowifi | cellular
	Recording       string // relative filename under the recordings dir
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveCallRecord inserts a new record or updates the existing row for the
// (device_id, call_id) pair. The pair is unique so the 2s call watcher can
// re-report the same dialog without duplicating history entries.
func (s *Store) SaveCallRecord(ctx context.Context, value CallRecord) (CallRecord, error) {
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.CallID = strings.TrimSpace(value.CallID)
	value.Number = strings.TrimSpace(value.Number)
	value.Direction = strings.ToLower(strings.TrimSpace(value.Direction))
	value.State = strings.ToLower(strings.TrimSpace(value.State))
	if value.DeviceID == "" {
		return CallRecord{}, errors.New("call record device id is required")
	}
	if value.CallID == "" {
		return CallRecord{}, errors.New("call record call id is required")
	}
	switch value.Direction {
	case "incoming", "outgoing":
	default:
		return CallRecord{}, fmt.Errorf("unsupported call direction %q", value.Direction)
	}
	switch value.State {
	case "active", "answered", "missed", "failed", "ended":
	default:
		return CallRecord{}, fmt.Errorf("unsupported call state %q", value.State)
	}
	if value.Transport == "" {
		value.Transport = "vowifi"
	}
	now := time.Now().UTC()
	value.UpdatedAt = now
	value.DurationSeconds = callRecordDuration(value)

	_, err := s.db.ExecContext(ctx, `
INSERT INTO call_records (
	device_id, call_id, number, direction, state,
	started_at, answered_at, ended_at, duration_seconds, transport, recording,
	created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id, call_id) DO UPDATE SET
	number = COALESCE(NULLIF(excluded.number, ''), number),
	state = excluded.state,
	answered_at = excluded.answered_at,
	ended_at = excluded.ended_at,
	duration_seconds = excluded.duration_seconds,
	recording = excluded.recording,
	updated_at = excluded.updated_at`,
		value.DeviceID, value.CallID, value.Number, value.Direction, value.State,
		value.StartedAt.Unix(), nullTime(value.AnsweredAt), nullTime(value.EndedAt),
		value.DurationSeconds, value.Transport, value.Recording,
		value.StartedAt.Unix(), value.UpdatedAt.Unix(),
	)
	if err != nil {
		return CallRecord{}, fmt.Errorf("save call record: %w", err)
	}
	if value.ID == 0 {
		var id int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT id FROM call_records WHERE device_id = ? AND call_id = ?`,
			value.DeviceID, value.CallID,
		).Scan(&id); err == nil {
			value.ID = id
		}
	}
	return value, nil
}

// ListCallRecords returns the most recent records, newest first. A non-empty
// deviceID restricts the result to one device.
func (s *Store) ListCallRecords(ctx context.Context, deviceID string, limit int) ([]CallRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
SELECT id, device_id, call_id, number, direction, state,
	started_at, answered_at, ended_at, duration_seconds, transport, recording,
	created_at, updated_at
FROM call_records`
	args := make([]any, 0, 2)
	if strings.TrimSpace(deviceID) != "" {
		query += ` WHERE device_id = ?`
		args = append(args, strings.TrimSpace(deviceID))
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list call records: %w", err)
	}
	defer rows.Close()
	result := make([]CallRecord, 0, limit)
	for rows.Next() {
		var record CallRecord
		var started, created, updated int64
		var answered, ended sql.NullInt64
		if err := rows.Scan(
			&record.ID, &record.DeviceID, &record.CallID, &record.Number, &record.Direction, &record.State,
			&started, &answered, &ended, &record.DurationSeconds, &record.Transport, &record.Recording,
			&created, &updated,
		); err != nil {
			return nil, fmt.Errorf("scan call record: %w", err)
		}
		record.StartedAt = time.Unix(started, 0).UTC()
		if answered.Valid {
			t := time.Unix(answered.Int64, 0).UTC()
			record.AnsweredAt = &t
		}
		if ended.Valid {
			t := time.Unix(ended.Int64, 0).UTC()
			record.EndedAt = &t
		}
		record.CreatedAt = time.Unix(created, 0).UTC()
		record.UpdatedAt = time.Unix(updated, 0).UTC()
		result = append(result, record)
	}
	return result, rows.Err()
}

// CallRecord returns one record by its numeric id.
func (s *Store) CallRecord(ctx context.Context, id int64) (CallRecord, error) {
	var record CallRecord
	var started, created, updated int64
	var answered, ended sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT id, device_id, call_id, number, direction, state,
	started_at, answered_at, ended_at, duration_seconds, transport, recording,
	created_at, updated_at
FROM call_records WHERE id = ?`, id).Scan(
		&record.ID, &record.DeviceID, &record.CallID, &record.Number, &record.Direction, &record.State,
		&started, &answered, &ended, &record.DurationSeconds, &record.Transport, &record.Recording,
		&created, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CallRecord{}, ErrNotFound
	}
	if err != nil {
		return CallRecord{}, fmt.Errorf("load call record: %w", err)
	}
	record.StartedAt = time.Unix(started, 0).UTC()
	if answered.Valid {
		t := time.Unix(answered.Int64, 0).UTC()
		record.AnsweredAt = &t
	}
	if ended.Valid {
		t := time.Unix(ended.Int64, 0).UTC()
		record.EndedAt = &t
	}
	record.CreatedAt = time.Unix(created, 0).UTC()
	record.UpdatedAt = time.Unix(updated, 0).UTC()
	return record, nil
}

func callRecordDuration(value CallRecord) int {
	start := value.StartedAt
	end := value.EndedAt
	if end == nil {
		end = &value.UpdatedAt
	}
	if end.Before(start) {
		end = &start
	}
	if value.AnsweredAt != nil && value.AnsweredAt.After(start) {
		// Talk time: answer -> end when the call was picked up.
		start = *value.AnsweredAt
	}
	seconds := int(end.Sub(start) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	return seconds
}

func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Unix()
}
