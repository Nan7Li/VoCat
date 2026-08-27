package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"vocat/internal/device"
	localproxy "vocat/internal/proxy"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func epdgProbePayload(probe store.EPDGProbeStatus) map[string]any {
	return map[string]any{
		"device_id":       probe.DeviceID,
		"iccid":           probe.ICCID,
		"epdg":            probe.EPDG,
		"port_500_ok":     probe.Port500OK,
		"port_4500_ok":    probe.Port4500OK,
		"rtt_500_ms":      probe.RTT500MS,
		"rtt_4500_ms":     probe.RTT4500MS,
		"error":           probe.Error,
		"checked_at":      probe.CheckedAt.Format(time.RFC3339),
		"last_success_at": formatOptionalTime(probe.LastSuccessAt),
		"last_failure_at": formatOptionalTime(probe.LastFailureAt),
		"disabled_vowifi": probe.DisabledVoWiFi,
	}
}

func (s *Server) handleEPDGProbe(w http.ResponseWriter, r *http.Request, config store.Device, entry device.Device) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "store is unavailable")
		return true
	}
	host, iccid, upstream, err := s.resolveEPDGProbeTarget(r.Context(), config, entry)
	if err != nil {
		writeError(w, http.StatusBadRequest, "epdg_probe_unavailable", err.Error())
		return true
	}
	var result localproxy.EPDGProbeResult
	var probeErr error
	if upstream != nil {
		result, probeErr = localproxy.ProbeSOCKS5EPDGPorts(r.Context(), upstream.Addr, upstream.Username, upstream.Password, host, 8*time.Second)
	} else {
		result, probeErr = localproxy.ProbeDirectEPDGPorts(r.Context(), host, 8*time.Second)
	}
	status := store.EPDGProbeStatus{
		DeviceID:   config.ID,
		ICCID:      iccid,
		EPDG:       host,
		Port500OK:  result.Port500OK,
		Port4500OK: result.Port4500OK,
		RTT500MS:   result.RTT500MS,
		RTT4500MS:  result.RTT4500MS,
		CheckedAt:  time.Now().UTC(),
	}
	if probeErr != nil {
		status.Error = probeErr.Error()
	} else if !result.Port500OK || !result.Port4500OK {
		status.Error = "ePDG UDP/500 and UDP/4500 health check did not pass"
	}
	if s.logger != nil {
		s.logger.Info("ePDG probe",
			"device_id", config.ID,
			"epdg", host,
			"via", map[bool]string{true: "socks5", false: "direct"}[upstream != nil],
			"port_500_ok", result.Port500OK,
			"port_4500_ok", result.Port4500OK,
			"rtt_500_ms", result.RTT500MS,
			"rtt_4500_ms", result.RTT4500MS,
			"error", status.Error,
		)
	}
	if saveErr := s.store.SaveEPDGProbeStatus(context.Background(), status); saveErr != nil {
		if s.logger != nil {
			s.logger.Error("save ePDG probe status", "device_id", config.ID, "error", saveErr)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": epdgProbePayload(status)})
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": epdgProbePayload(status)})
	return true
}

func (s *Server) resolveEPDGProbeTarget(ctx context.Context, config store.Device, entry device.Device) (host, iccid string, upstream *store.UpstreamProxy, err error) {
	snapshot := entry.Snapshot
	var deriveErr error
	if snapshot != nil {
		iccid = strings.TrimSpace(snapshot.ICCID)
		mcc, mnc := device.CardMCCMNCWithLength(snapshot.IMSI, snapshot.MNCLength)
		host, deriveErr = vowifi.DeriveEPDG(vowifi.SIMIdentity{
			ICCID:   iccid,
			IMSI:    strings.TrimSpace(snapshot.IMSI),
			IMEI:    strings.TrimSpace(snapshot.IMEI),
			HomeMCC: mcc,
			HomeMNC: mnc,
			SPN:     strings.TrimSpace(snapshot.SPN),
			GID1:    strings.TrimSpace(snapshot.GID1),
			GID2:    strings.TrimSpace(snapshot.GID2),
		})
	}
	if host == "" {
		if runtime, runtimeErr := s.store.VoWiFiRuntime(ctx, config.ID); runtimeErr == nil {
			runtimeICCID := strings.TrimSpace(runtime.ICCID)
			// A runtime tunnel belongs to the SIM that created it. Never let a
			// stale tunnel from a previous card override the current snapshot.
			if iccid == "" || (runtimeICCID != "" && runtimeICCID == iccid) {
				if tunnel, _ := rawJSONObject(runtime.Tunnel).(map[string]any); tunnel != nil {
					if epdg, _ := tunnel["epdg"].(string); strings.TrimSpace(epdg) != "" {
						host = strings.TrimSpace(epdg)
					}
				}
			}
		}
	}
	if host == "" {
		if previous, prevErr := s.store.EPDGProbeStatus(ctx, config.ID); prevErr == nil {
			previousICCID := strings.TrimSpace(previous.ICCID)
			if iccid == "" || (previousICCID != "" && previousICCID == iccid) {
				host = strings.TrimSpace(previous.EPDG)
			}
		}
	}
	if host == "" {
		if deriveErr != nil {
			return "", iccid, nil, deriveErr
		}
		return "", iccid, nil, errors.New("无法推导 ePDG 主机：请确认 SIM 已识别或先开启 VoWiFi")
	}
	upstream = s.lookupEPDGUpstream(ctx, config.ID, iccid, snapshot)
	return host, iccid, upstream, nil
}

func (s *Server) lookupEPDGUpstream(ctx context.Context, deviceID, iccid string, snapshot *device.Snapshot) *store.UpstreamProxy {
	if s.store == nil {
		return nil
	}
	upstreamID := ""
	if iccid != "" {
		if binding, err := s.store.DeviceProxyBinding(ctx, iccid); err == nil {
			upstreamID = binding.UpstreamProxyID
		}
	}
	if upstreamID == "" && snapshot != nil {
		mcc, _ := device.CardMCCMNCWithLength(snapshot.IMSI, snapshot.MNCLength)
		if country, found := device.CountryForMCC(mcc); found {
			if rule, err := s.store.CountryRule(ctx, country); err == nil && rule.Enabled {
				upstreamID = rule.UpstreamProxyID
			}
		}
	}
	if upstreamID == "" {
		return nil
	}
	upstream, err := s.store.UpstreamProxy(ctx, upstreamID)
	if err != nil || !upstream.Enabled {
		return nil
	}
	_ = deviceID
	return &upstream
}
