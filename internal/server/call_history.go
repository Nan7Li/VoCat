package server

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

// cellularCallIDs maps a device to the synthetic call id of its current
// circuit-switched call so dial/answer/hangup actions can update one record.
var cellularCallIDs sync.Map

// RunCallHistoryWatcher persists VoWiFi IMS call lifecycles into the call
// history table. It polls the IMS call controller on a short cadence and
// upserts by (device_id, call_id), so the same dialog never duplicates.
// Records reach a terminal state (answered/missed/failed) when the IMS call
// does; the controller retains terminal calls briefly, which lets the final
// transition land here.
func (s *Server) RunCallHistoryWatcher(ctx context.Context) {
	if s.vowifi == nil {
		return
	}
	controller, ok := s.vowifi.(VoWiFiCallController)
	if !ok {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncCallRecords(ctx, controller)
		}
	}
}

func (s *Server) syncCallRecords(ctx context.Context, controller VoWiFiCallController) {
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return
	}
	for _, config := range devices {
		state, stateErr := s.vowifi.State(config.ID)
		if stateErr != nil || !state.IMSReady {
			continue
		}
		calls, callsErr := controller.Calls(config.ID)
		if callsErr != nil {
			continue
		}
		for _, call := range calls {
			s.upsertCallRecord(ctx, config.ID, call)
		}
	}
}

// upsertCallRecord normalises an IMS call snapshot into a persisted history
// row. Terminal states are derived from the IMS state machine: an incoming
// call that ends without an answer is missed; an outgoing call that ends
// without an answer is failed; anything answered is answered.
func (s *Server) upsertCallRecord(ctx context.Context, deviceID string, call vowifi.Call) {
	state := "active"
	if call.EndedAt != nil {
		switch {
		case call.AnsweredAt != nil:
			state = "answered"
		case call.Direction == "incoming":
			state = "missed"
		default:
			state = "failed"
		}
	} else if call.State == "failed" {
		state = "failed"
	}
	record := store.CallRecord{
		DeviceID:   deviceID,
		CallID:     call.ID,
		Number:     call.Number,
		Direction:  call.Direction,
		State:      state,
		StartedAt:  call.StartedAt,
		AnsweredAt: call.AnsweredAt,
		EndedAt:    call.EndedAt,
		Transport:  "vowifi",
		Recording:  call.Recording,
	}
	if _, err := s.store.SaveCallRecord(ctx, record); err != nil {
		s.logger.Warn("persist call record failed", "device_id", deviceID, "call_id", call.ID, "error", err)
	}
}

// upsertCellularCallRecord tracks circuit-switched calls by a synthetic id
// so the dial/answer/hangup actions update one history row.
func (s *Server) upsertCellularCallRecord(ctx context.Context, deviceID, number, action string) {
	var callID any
	if action == "dial" {
		callID = fmt.Sprintf("cellular-%s-%d", deviceID, time.Now().UnixNano())
		cellularCallIDs.Store(deviceID, callID)
	} else {
		callID, _ = cellularCallIDs.Load(deviceID)
		if callID == nil {
			return
		}
		if action == "hangup" {
			cellularCallIDs.Delete(deviceID)
		}
	}
	id, _ := callID.(string)
	now := time.Now().UTC()
	record := store.CallRecord{
		DeviceID:  deviceID,
		CallID:    id,
		Number:    number,
		Direction: "outgoing",
		Transport: "cellular",
		StartedAt: now,
	}
	switch action {
	case "dial":
		record.State = "active"
	case "answer":
		record.State = "active"
		record.AnsweredAt = &now
	case "hangup":
		record.State = "ended"
		record.EndedAt = &now
	}
	if _, err := s.store.SaveCallRecord(ctx, record); err != nil {
		s.logger.Warn("persist cellular call record failed", "device_id", deviceID, "error", err)
	}
}

// routeCallRecordsAPI serves the phone page's call history and recordings.
func (s *Server) routeCallRecordsAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	segments := splitAPIPath(cleanPath)
	if len(segments) == 0 || segments[0] != "calls" {
		return false
	}
	if len(segments) == 2 && segments[1] == "history" {
		s.handleCallHistoryList(w, r)
		return true
	}
	if len(segments) == 4 && segments[1] == "history" && segments[3] == "recording" {
		s.handleCallRecording(w, r, segments[2])
		return true
	}
	return false
}

func (s *Server) handleCallHistoryList(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device_id"))
	records, err := s.store.ListCallRecords(r.Context(), deviceID, limit)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, callRecordWire(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"records": items}})
}

func callRecordWire(record store.CallRecord) map[string]any {
	return map[string]any{
		"id":               record.ID,
		"device_id":        record.DeviceID,
		"number":           record.Number,
		"direction":        record.Direction,
		"state":            record.State,
		"started_at":       record.StartedAt.Format(time.RFC3339),
		"answered_at":      formatOptionalTime(record.AnsweredAt),
		"ended_at":         formatOptionalTime(record.EndedAt),
		"duration_seconds": record.DurationSeconds,
		"transport":        record.Transport,
		"recording":        record.Recording,
	}
}

func formatOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339)
}

func (s *Server) handleCallRecording(w http.ResponseWriter, r *http.Request, idText string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(idText), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_record_id", "call record id is invalid")
		return
	}
	record, err := s.store.CallRecord(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if strings.TrimSpace(record.Recording) == "" {
		writeError(w, http.StatusNotFound, "recording_missing", "this call has no recording")
		return
	}
	if s.recordingsDir == "" {
		writeError(w, http.StatusNotFound, "recording_missing", "call recording is not enabled")
		return
	}
	relative := filepath.Clean(strings.ReplaceAll(record.Recording, "\\", "/"))
	if relative == "." || strings.HasPrefix(relative, "../") || filepath.IsAbs(relative) {
		writeError(w, http.StatusBadRequest, "invalid_recording_path", "recording path is invalid")
		return
	}
	fullPath := filepath.Join(s.recordingsDir, relative)
	if !strings.HasPrefix(fullPath, filepath.Clean(s.recordingsDir)+string(os.PathSeparator)) {
		writeError(w, http.StatusBadRequest, "invalid_recording_path", "recording path is invalid")
		return
	}
	info, statErr := os.Stat(fullPath)
	if statErr != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "recording_missing", "recording file is not available")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	if name := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(fullPath)}); name != "" {
		w.Header().Set("Content-Disposition", name)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, fullPath)
}
