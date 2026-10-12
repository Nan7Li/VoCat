package desktop

import (
	"fmt"
	"net/url"
	"strings"
)

// normalizeDesktopBaseURL turns VOCAT_DESKTOP_URL/VOCAT_ADDR into the origin
// used by the native client.  It intentionally rejects credentials, queries,
// and fragments so an accidental secret in an environment value can never be
// copied into API requests or the optional browser hand-off.
func normalizeDesktopBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "127.0.0.1:7575"
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid desktop service URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("desktop service URL must use http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("desktop service URL must contain only a host and optional path")
	}
	// VOCAT_ADDR commonly binds to all interfaces (0.0.0.0 or ::).  The
	// desktop process is local, so connect through loopback instead of trying
	// to dial the unspecified address.
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		port := parsed.Port()
		if port == "" {
			return "", fmt.Errorf("desktop service URL is missing a port")
		}
		parsed.Host = "127.0.0.1:" + port
	}
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func desktopBaseURLFromEnvironment(getenv func(string) string) (string, error) {
	if value := strings.TrimSpace(getenv("VOCAT_DESKTOP_URL")); value != "" {
		return normalizeDesktopBaseURL(value)
	}
	if value := strings.TrimSpace(getenv("VOCAT_ADDR")); value != "" {
		return normalizeDesktopBaseURL(value)
	}
	return normalizeDesktopBaseURL("127.0.0.1:7575")
}
