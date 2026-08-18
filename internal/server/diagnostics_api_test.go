package server

import (
	"strings"
	"testing"
)

func TestSanitizeDiagnosticTextRedactsSecretsAndIdentities(t *testing.T) {
	input := "password=hunter2 token=abc cookie=sid authorization: Bearer xyz iccid=890123 imsi=310150 private_key=AAAA user:secret@proxy.example"
	got := sanitizeDiagnosticText(input)
	for _, leak := range []string{"hunter2", "abc", "sid", "Bearer xyz", "890123", "310150", "AAAA", "user:secret@"} {
		if strings.Contains(got, leak) {
			t.Fatalf("sanitizeDiagnosticText leaked %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("sanitizeDiagnosticText did not mark redactions: %s", got)
	}
}

func TestRadioModeForSummaryTriad(t *testing.T) {
	if got := radioModeForSummary(map[string]any{"running": false}); got != "offline" {
		t.Fatalf("offline = %q", got)
	}
	if got := radioModeForSummary(map[string]any{"running": true, "vowifi_active": true, "flight_mode": true, "network_enabled": true}); got != "vowifi" {
		t.Fatalf("vowifi = %q", got)
	}
	if got := radioModeForSummary(map[string]any{"running": true, "network_enabled": true, "flight_mode": false}); got != "cellular" {
		t.Fatalf("cellular = %q", got)
	}
	if got := radioModeForSummary(map[string]any{"running": true, "flight_mode": true, "network_enabled": true}); got != "airplane" {
		t.Fatalf("airplane = %q", got)
	}
	if got := radioModeForSummary(map[string]any{"running": true, "lifecycle_phase": "rebooting"}); got != "transition" {
		t.Fatalf("transition = %q", got)
	}
}
