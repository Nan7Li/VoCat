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

func TestUpstreamVocatCheckUsesOfficialRepository(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var repo string
	server := &Server{
		store:               database,
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRequestBodyBytes: 1 << 20,
		updateCheck: func(_ context.Context, repository, _, current string) (update.CheckResult, error) {
			repo = repository
			if current != "0.3.15" {
				t.Fatalf("synced version = %q", current)
			}
			return update.CheckResult{
				Available:    true,
				Current:      current,
				Latest:       "0.3.16",
				ReleaseNotes: "IMS fix",
				Release:      &update.Release{TagName: "v0.3.16"},
			}, nil
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/settings/upstream-vocat", nil)
	server.handleUpstreamVocat(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body)
	}
	if repo != update.UpstreamRepository {
		t.Fatalf("checked repo = %q", repo)
	}
	var envelope struct {
		Data upstreamVocatStatus `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Available || envelope.Data.LatestVersion != "0.3.16" || envelope.Data.SyncedVersion != "0.3.15" {
		t.Fatalf("status = %#v", envelope.Data)
	}

	put := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, "/api/settings/upstream-vocat", strings.NewReader(`{"synced_version":"0.3.16"}`))
	request.Header.Set("Content-Type", "application/json")
	server.handleUpstreamVocat(put, request)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s", put.Code, put.Body)
	}
	if err := json.Unmarshal(put.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Available || envelope.Data.SyncedVersion != "0.3.16" {
		t.Fatalf("marked synced = %#v", envelope.Data)
	}
}

func TestUpstreamVocatRestoredDatabaseUsesCompiledBaseline(t *testing.T) {
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.UpsertAppSetting(context.Background(), store.AppSetting{
		Key:   upstreamVocatSettingKey,
		Value: json.RawMessage(`{"enabled":false,"synced_version":"0.2.23","latest_version":"0.2.24","available":true,"last_check_at":"2026-10-08T00:00:00Z"}`),
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database}
	status, err := server.loadUpstreamVocat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.SyncedVersion != defaultUpstreamVocatVersion || status.Available || status.LastCheckAt != "" {
		t.Fatalf("restored status = %#v", status)
	}
}
