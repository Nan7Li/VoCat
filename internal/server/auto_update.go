package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"vocat/internal/buildinfo"
	"vocat/internal/store"
	"vocat/internal/update"
)

const (
	autoUpdateSettingKey     = "system.auto_update"
	autoUpdateMinInterval    = 1
	autoUpdateMaxInterval    = 168
	autoUpdateDefaultHours   = 6
	autoUpdateStartupDelay   = 90 * time.Second
	autoUpdatePollInterval   = time.Minute
)

type autoUpdateSettings struct {
	Enabled       bool   `json:"enabled"`
	Apply         bool   `json:"apply"`
	IntervalHours int    `json:"interval_hours"`
	LastCheckAt   string `json:"last_check_at,omitempty"`
	LastAvailable bool   `json:"last_available"`
	LastVersion   string `json:"last_version,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Repository    string `json:"repository,omitempty"`
	IsDocker      bool   `json:"is_docker"`
}

func defaultAutoUpdateSettings() autoUpdateSettings {
	return autoUpdateSettings{
		Enabled:       true,
		Apply:         false,
		IntervalHours: autoUpdateDefaultHours,
	}
}

func (s *Server) StartAutoUpdate(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	go s.runAutoUpdate(ctx)
}

func (s *Server) runAutoUpdate(ctx context.Context) {
	timer := time.NewTimer(autoUpdateStartupDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.maybeAutoUpdate(ctx, false)
	s.maybeCheckUpstreamVocat(ctx)
	ticker := time.NewTicker(autoUpdatePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.maybeAutoUpdate(ctx, false)
			s.maybeCheckUpstreamVocat(ctx)
		}
	}
}

func (s *Server) maybeAutoUpdate(ctx context.Context, force bool) {
	settings, err := s.loadAutoUpdateSettings(ctx)
	if err != nil {
		s.logger.Warn("read auto-update settings", "error", err)
		return
	}
	if !settings.Enabled && !force {
		return
	}
	if !force && settings.LastCheckAt != "" {
		checkedAt, parseErr := time.Parse(time.RFC3339, settings.LastCheckAt)
		if parseErr == nil && time.Since(checkedAt) < time.Duration(settings.IntervalHours)*time.Hour {
			return
		}
	}
	if strings.TrimSpace(s.updateRepository) == "" || s.updateCheck == nil {
		settings.LastCheckAt = time.Now().UTC().Format(time.RFC3339)
		settings.LastError = "no trusted update repository is configured"
		_ = s.saveAutoUpdateSettings(ctx, settings)
		return
	}

	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	result, checkErr := s.updateCheck(checkCtx, s.updateRepository, s.updateToken, buildinfo.Version)
	cancel()
	settings.LastCheckAt = time.Now().UTC().Format(time.RFC3339)
	settings.Repository = s.updateRepository
	if checkErr != nil {
		settings.LastError = checkErr.Error()
		s.logger.Warn("automatic update check failed", "repository", s.updateRepository, "error", checkErr)
		_ = s.saveAutoUpdateSettings(ctx, settings)
		return
	}
	settings.LastError = ""
	settings.LastAvailable = result.Available
	settings.LastVersion = result.Latest
	if !result.Available {
		s.logger.Info("automatic update check: already current", "version", result.Latest)
		_ = s.saveAutoUpdateSettings(ctx, settings)
		return
	}
	s.logger.Info("automatic update check: newer release available", "current", result.Current, "latest", result.Latest)
	if !settings.Apply {
		_ = s.saveAutoUpdateSettings(ctx, settings)
		return
	}
	if runningInDocker() {
		settings.LastError = "automatic apply is disabled inside Docker; pull a new image instead"
		_ = s.saveAutoUpdateSettings(ctx, settings)
		return
	}
	if err := s.applyAutoUpdate(ctx, result.Latest); err != nil {
		settings.LastError = err.Error()
		s.logger.Error("automatic update apply failed", "error", err)
		_ = s.saveAutoUpdateSettings(ctx, settings)
	}
}

func (s *Server) applyAutoUpdate(ctx context.Context, latest string) error {
	s.updateMu.Lock()
	if s.updateApplying {
		s.updateMu.Unlock()
		return errors.New("another update is already in progress")
	}
	s.updateApplying = true
	s.updateMu.Unlock()
	defer func() {
		s.updateMu.Lock()
		s.updateApplying = false
		s.updateMu.Unlock()
	}()
	if s.updateApply == nil {
		return errors.New("update apply is not configured")
	}
	applyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	result, err := s.updateApply(applyCtx, s.logger, update.Options{
		Repo:  s.updateRepository,
		Token: s.updateToken,
	}, false)
	cancel()
	if err != nil {
		return err
	}
	if !result.Applied {
		return nil
	}
	s.logger.Info("automatic update installed", "version", latest)
	if err := s.store.DeleteAllSessions(ctx); err != nil {
		s.logger.Error("revoke sessions after automatic update failed", "error", err)
	}
	if s.updateRestart != nil {
		restart := s.updateRestart
		logger := s.logger
		go func() {
			time.Sleep(time.Second)
			if err := restart(logger); err != nil {
				logger.Error("restart after automatic update failed", "error", err)
			}
		}()
	}
	return nil
}

func (s *Server) handleAutoUpdateSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.loadAutoUpdateSettings(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		settings.Repository = s.updateRepository
		settings.IsDocker = runningInDocker()
		writeJSON(w, http.StatusOK, map[string]any{"data": settings})
	case http.MethodPut:
		var request struct {
			Enabled       *bool `json:"enabled"`
			Apply         *bool `json:"apply"`
			IntervalHours *int  `json:"interval_hours"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		settings, err := s.loadAutoUpdateSettings(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if request.Enabled != nil {
			settings.Enabled = *request.Enabled
		}
		if request.Apply != nil {
			settings.Apply = *request.Apply
		}
		if request.IntervalHours != nil {
			if *request.IntervalHours < autoUpdateMinInterval || *request.IntervalHours > autoUpdateMaxInterval {
				writeError(w, http.StatusBadRequest, "invalid_interval", "interval_hours must be between 1 and 168")
				return
			}
			settings.IntervalHours = *request.IntervalHours
		}
		if err := s.saveAutoUpdateSettings(r.Context(), settings); err != nil {
			s.writeStoreError(w, err)
			return
		}
		settings.Repository = s.updateRepository
		settings.IsDocker = runningInDocker()
		writeJSON(w, http.StatusOK, map[string]any{"data": settings})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) loadAutoUpdateSettings(ctx context.Context) (autoUpdateSettings, error) {
	settings := defaultAutoUpdateSettings()
	stored, err := s.store.AppSetting(ctx, autoUpdateSettingKey)
	if errors.Is(err, store.ErrNotFound) {
		return settings, nil
	}
	if err != nil {
		return autoUpdateSettings{}, err
	}
	if err := json.Unmarshal(stored.Value, &settings); err != nil {
		return autoUpdateSettings{}, err
	}
	if settings.IntervalHours < autoUpdateMinInterval || settings.IntervalHours > autoUpdateMaxInterval {
		settings.IntervalHours = autoUpdateDefaultHours
	}
	return settings, nil
}

func (s *Server) saveAutoUpdateSettings(ctx context.Context, settings autoUpdateSettings) error {
	if settings.IntervalHours < autoUpdateMinInterval || settings.IntervalHours > autoUpdateMaxInterval {
		settings.IntervalHours = autoUpdateDefaultHours
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.store.UpsertAppSetting(ctx, store.AppSetting{
		Key:   autoUpdateSettingKey,
		Value: raw,
	})
}
