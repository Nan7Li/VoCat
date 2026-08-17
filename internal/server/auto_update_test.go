package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/store"
	"vocat/internal/update"
)

func TestAutoUpdateSettingsRoundTrip(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{
		store:               database,
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRequestBodyBytes: 1 << 20,
		updateRepository:    update.DefaultRepository,
	}

	get := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/settings/auto-update", nil)
	server.handleAutoUpdateSettings(get, request)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", get.Code, get.Body)
	}
	var envelope struct {
		Data autoUpdateSettings `json:"data"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Enabled || envelope.Data.Apply || envelope.Data.IntervalHours != 6 {
		t.Fatalf("defaults = %#v", envelope.Data)
	}
	if envelope.Data.Repository != update.DefaultRepository {
		t.Fatalf("repository = %q", envelope.Data.Repository)
	}

	put := httptest.NewRecorder()
	body := `{"enabled":true,"apply":true,"interval_hours":12}`
	request = httptest.NewRequest(http.MethodPut, "/api/settings/auto-update", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.handleAutoUpdateSettings(put, request)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body)
	}
	if err := json.Unmarshal(put.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Apply || envelope.Data.IntervalHours != 12 {
		t.Fatalf("saved = %#v", envelope.Data)
	}
}

func TestMaybeAutoUpdateAppliesWhenEnabled(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	applied := false
	server := &Server{
		store:            database,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		updateRepository: update.DefaultRepository,
		updateCheck: func(context.Context, string, string, string) (update.CheckResult, error) {
			return update.CheckResult{Available: true, Current: "1.0.0", Latest: "1.2.0"}, nil
		},
		updateApply: func(context.Context, *slog.Logger, update.Options, bool) (update.CheckResult, error) {
			applied = true
			return update.CheckResult{Applied: true, Latest: "1.2.0"}, nil
		},
	}
	if err := server.saveAutoUpdateSettings(context.Background(), autoUpdateSettings{
		Enabled: true, Apply: true, IntervalHours: 6,
	}); err != nil {
		t.Fatal(err)
	}
	server.maybeAutoUpdate(context.Background(), true)
	if !applied {
		t.Fatal("expected automatic apply")
	}
}
