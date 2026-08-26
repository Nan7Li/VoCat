package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"vocat/internal/store"
	"vocat/internal/update"
)

const (
	upstreamVocatSettingKey     = "system.upstream_vocat"
	defaultUpstreamVocatVersion = "0.2.23"
)

type upstreamVocatStatus struct {
	Enabled       bool   `json:"enabled"`
	Repository    string `json:"repository"`
	SyncedVersion string `json:"synced_version"`
	LatestVersion string `json:"latest_version,omitempty"`
	Available     bool   `json:"available"`
	ReleaseNotes  string `json:"release_notes,omitempty"`
	HTMLURL       string `json:"html_url,omitempty"`
	LastCheckAt   string `json:"last_check_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

func defaultUpstreamVocatStatus() upstreamVocatStatus {
	return upstreamVocatStatus{
		Enabled:       true,
		Repository:    update.UpstreamRepository,
		SyncedVersion: defaultUpstreamVocatVersion,
	}
}

func (s *Server) handleUpstreamVocat(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		status, err := s.loadUpstreamVocat(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	case http.MethodPut:
		var request struct {
			Enabled       *bool  `json:"enabled"`
			SyncedVersion string `json:"synced_version"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		status, err := s.loadUpstreamVocat(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if request.Enabled != nil {
			status.Enabled = *request.Enabled
		}
		if strings.TrimSpace(request.SyncedVersion) != "" {
			status.SyncedVersion = strings.TrimPrefix(strings.TrimSpace(request.SyncedVersion), "v")
			if status.LatestVersion != "" {
				newer, cmpErr := update.IsNewerVersion(status.SyncedVersion, status.LatestVersion)
				if cmpErr == nil {
					status.Available = newer
				}
			}
		}
		if err := s.saveUpstreamVocat(r.Context(), status); err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	case http.MethodPost:
		status, err := s.checkUpstreamVocat(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream_check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	default:
		w.Header().Set("Allow", "GET, PUT, POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) checkUpstreamVocat(ctx context.Context) (upstreamVocatStatus, error) {
	status, err := s.loadUpstreamVocat(ctx)
	if err != nil {
		return upstreamVocatStatus{}, err
	}
	if s.updateCheck == nil {
		return status, errors.New("update check is not configured")
	}
	result, err := s.updateCheck(ctx, update.UpstreamRepository, s.updateToken, status.SyncedVersion)
	status.LastCheckAt = time.Now().UTC().Format(time.RFC3339)
	status.Repository = update.UpstreamRepository
	if err != nil {
		status.LastError = err.Error()
		_ = s.saveUpstreamVocat(ctx, status)
		return status, err
	}
	status.LastError = ""
	status.LatestVersion = result.Latest
	status.Available = result.Available
	status.ReleaseNotes = result.ReleaseNotes
	if result.Release != nil {
		status.HTMLURL = "https://github.com/" + update.UpstreamRepository + "/releases/tag/" + result.Release.TagName
	}
	if saveErr := s.saveUpstreamVocat(ctx, status); saveErr != nil {
		s.logger.Warn("save upstream VoCat status", "error", saveErr)
	}
	return status, nil
}

func (s *Server) maybeCheckUpstreamVocat(ctx context.Context) {
	status, err := s.loadUpstreamVocat(ctx)
	if err != nil || !status.Enabled {
		return
	}
	if status.LastCheckAt != "" {
		checkedAt, parseErr := time.Parse(time.RFC3339, status.LastCheckAt)
		if parseErr == nil && time.Since(checkedAt) < time.Hour {
			return
		}
	}
	if _, err := s.checkUpstreamVocat(ctx); err != nil {
		s.logger.Warn("check official VoCat release", "error", err)
	}
}

func (s *Server) loadUpstreamVocat(ctx context.Context) (upstreamVocatStatus, error) {
	status := defaultUpstreamVocatStatus()
	stored, err := s.store.AppSetting(ctx, upstreamVocatSettingKey)
	if errors.Is(err, store.ErrNotFound) {
		return status, nil
	}
	if err != nil {
		return upstreamVocatStatus{}, err
	}
	if err := json.Unmarshal(stored.Value, &status); err != nil {
		return upstreamVocatStatus{}, err
	}
	if strings.TrimSpace(status.Repository) == "" {
		status.Repository = update.UpstreamRepository
	}
	if strings.TrimSpace(status.SyncedVersion) == "" {
		status.SyncedVersion = defaultUpstreamVocatVersion
	}
	return status, nil
}

func (s *Server) saveUpstreamVocat(ctx context.Context, status upstreamVocatStatus) error {
	if strings.TrimSpace(status.SyncedVersion) == "" {
		status.SyncedVersion = defaultUpstreamVocatVersion
	}
	status.Repository = update.UpstreamRepository
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return s.store.UpsertAppSetting(ctx, store.AppSetting{
		Key:   upstreamVocatSettingKey,
		Value: raw,
	})
}
