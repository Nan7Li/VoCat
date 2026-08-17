package server

import (
	"errors"
	"net/http"
	"strings"

	"vocat/internal/wireguard"
)

func (s *Server) routeWireGuardAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	if cleanPath != "wireguard-tunnels" && !strings.HasPrefix(cleanPath, "wireguard-tunnels/") {
		return false
	}
	if s.wireguard == nil {
		writeError(w, http.StatusServiceUnavailable, "wireguard_unavailable", "WireGuard support is not configured")
		return true
	}
	segments := splitAPIPath(cleanPath)
	if len(segments) == 1 {
		s.handleWireGuardTunnels(w, r)
		return true
	}
	if len(segments) == 2 && segments[1] != "" {
		s.handleWireGuardTunnel(w, r, segments[1], "")
		return true
	}
	if len(segments) == 3 && segments[1] != "" {
		s.handleWireGuardTunnel(w, r, segments[1], segments[2])
		return true
	}
	writeError(w, http.StatusNotFound, "not_found", "WireGuard endpoint not found")
	return true
}

func (s *Server) handleWireGuardTunnels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snapshot, err := s.wireguard.Snapshot(r.Context())
		if err != nil {
			s.writeWireGuardError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": snapshot})
	case http.MethodPost:
		var request struct {
			Name      string `json:"name"`
			Interface string `json:"interface"`
			Config    string `json:"config"`
			Autostart bool   `json:"autostart"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		created, err := s.wireguard.Create(r.Context(), wireguard.SaveRequest{
			Name:      request.Name,
			Interface: request.Interface,
			Config:    request.Config,
			Autostart: request.Autostart,
		})
		if err != nil {
			s.writeWireGuardError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"data": created})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) handleWireGuardTunnel(w http.ResponseWriter, r *http.Request, id, action string) {
	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			status, err := s.wireguard.Get(r.Context(), id)
			if err != nil {
				s.writeWireGuardError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": status})
		case http.MethodPut:
			var request struct {
				Name      string `json:"name"`
				Interface string `json:"interface"`
				Config    string `json:"config"`
				Autostart bool   `json:"autostart"`
			}
			if err := s.decodeJSON(w, r, &request); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			updated, err := s.wireguard.Update(r.Context(), id, wireguard.SaveRequest{
				Name:      request.Name,
				Interface: request.Interface,
				Config:    request.Config,
				Autostart: request.Autostart,
			})
			if err != nil {
				s.writeWireGuardError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": updated})
		case http.MethodDelete:
			if err := s.wireguard.Delete(r.Context(), id); err != nil {
				s.writeWireGuardError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}})
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	case "up":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		status, err := s.wireguard.Up(r.Context(), id)
		if err != nil {
			s.writeWireGuardError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	case "down":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		status, err := s.wireguard.Down(r.Context(), id)
		if err != nil {
			s.writeWireGuardError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	default:
		writeError(w, http.StatusNotFound, "not_found", "WireGuard endpoint not found")
	}
}

func (s *Server) writeWireGuardError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wireguard.ErrNotFound):
		writeError(w, http.StatusNotFound, "wireguard_not_found", err.Error())
	case errors.Is(err, wireguard.ErrAvailable):
		writeError(w, http.StatusFailedDependency, "wireguard_tools_missing", "Install WireGuard on this Linux host (wg and ip)")
	case errors.Is(err, wireguard.ErrBusy):
		writeError(w, http.StatusConflict, "wireguard_busy", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "wireguard_error", err.Error())
	}
}
