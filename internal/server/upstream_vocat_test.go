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
			if current != "0.2.22" {
				t.Fatalf("synced version = %q", current)
			}
			return update.CheckResult{
				Available:    true,
				Current:      current,
				Latest:       "0.2.23",
				ReleaseNotes: "IMS fix",
				Release:      &update.Release{TagName: "v0.2.23"},
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
	if !envelope.Data.Available || envelope.Data.LatestVersion != "0.2.23" || envelope.Data.SyncedVersion != "0.2.22" {
		t.Fatalf("status = %#v", envelope.Data)
	}

	put := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, "/api/settings/upstream-vocat", strings.NewReader(`{"synced_version":"0.2.23"}`))
	request.Header.Set("Content-Type", "application/json")
	server.handleUpstreamVocat(put, request)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s", put.Code, put.Body)
	}
	if err := json.Unmarshal(put.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Available || envelope.Data.SyncedVersion != "0.2.23" {
		t.Fatalf("marked synced = %#v", envelope.Data)
	}
}
