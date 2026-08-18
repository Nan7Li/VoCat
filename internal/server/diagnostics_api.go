package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"

	"vocat/internal/buildinfo"
	"vocat/internal/store"
)

var (
	diagnosticSecretPattern   = regexp.MustCompile(`(?i)(password|passwd|secret|token|authorization|cookie|set-cookie|private[_-]?key|wg[_-]?private|preshared)([^\s:=]*)\s*[=:]\s*\S+`)
	diagnosticIdentityPattern = regexp.MustCompile(`(?i)\b(iccid|imsi|imei|msisdn|imsi-msisdn)\b\s*[=:]\s*\S+`)
	diagnosticUserinfoPattern = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+(?::[^@\s/]+)?@`)
	diagnosticPEMPattern      = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
)

// handleDiagnostics creates a small, intentionally allow-listed support bundle.
// It never serializes application settings, proxy credentials, SIM identities,
// cookies, raw modem responses, or WireGuard private keys.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	manifest := map[string]any{
		"product":      "Halo",
		"version":      buildinfo.Version,
		"build_time":   buildinfo.BuildTime,
		"os":           runtime.GOOS,
		"architecture": runtime.GOARCH,
		"uptime":       formatDuration(time.Since(s.startedAt)),
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	health := "database=unknown\n"
	if err := s.store.Ready(ctx); err == nil {
		health = "database=ok\n"
	} else {
		health = "database=error\n"
	}

	var logs strings.Builder
	if entries, err := s.store.ListLogEvents(ctx, store.LogFilter{Limit: 200}); err == nil {
		for _, entry := range entries {
			fmt.Fprintf(&logs, "%s [%s] %s\n", entry.Time.UTC().Format(time.RFC3339), entry.Level, sanitizeDiagnosticText(entry.Message))
		}
	}

	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tarWriter := tar.NewWriter(gz)
	writeEntry := func(name string, content []byte) error {
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), ModTime: time.Now().UTC()}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		_, err := tarWriter.Write(content)
		return err
	}
	if err := writeEntry("manifest.json", manifestBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "diagnostics_failed", "could not create diagnostics bundle")
		return
	}
	if err := writeEntry("health.txt", []byte(health)); err != nil {
		writeError(w, http.StatusInternalServerError, "diagnostics_failed", "could not create diagnostics bundle")
		return
	}
	if err := writeEntry("logs.txt", []byte(logs.String())); err != nil {
		writeError(w, http.StatusInternalServerError, "diagnostics_failed", "could not create diagnostics bundle")
		return
	}
	if err := tarWriter.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "diagnostics_failed", "could not finalize diagnostics bundle")
		return
	}
	if err := gz.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "diagnostics_failed", "could not finalize diagnostics bundle")
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	s.recordAudit(r.Context(), "admin", "diagnostics.download", "system", "diagnostics", "success", "redacted support bundle")
	w.Header().Set("Content-Disposition", `attachment; filename="halo-diagnostics-`+time.Now().UTC().Format("20060102-150405")+`.tar.gz"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(payload.Bytes())
}

func sanitizeDiagnosticText(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = diagnosticPEMPattern.ReplaceAllString(value, "<redacted-private-key>")
	value = diagnosticSecretPattern.ReplaceAllString(value, "${1}=<redacted>")
	value = diagnosticIdentityPattern.ReplaceAllString(value, "${1}=<redacted>")
	value = diagnosticUserinfoPattern.ReplaceAllString(value, "<redacted>@")
	return value
}
