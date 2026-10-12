package desktop

import "testing"

func TestNormalizeDesktopBaseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "address", raw: "127.0.0.1:7575", want: "http://127.0.0.1:7575"},
		{name: "wildcard", raw: "0.0.0.0:7575", want: "http://127.0.0.1:7575"},
		{name: "https path", raw: "https://localhost:8443/vocat/", want: "https://localhost:8443/vocat"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeDesktopBaseURL(test.raw)
			if err != nil {
				t.Fatalf("normalizeDesktopBaseURL(%q): %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("normalizeDesktopBaseURL(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestNormalizeDesktopBaseURLRejectsSecrets(t *testing.T) {
	for _, raw := range []string{
		"http://user:password@example.test:7575",
		"http://127.0.0.1:7575/?token=secret",
		"ftp://127.0.0.1:7575",
	} {
		if _, err := normalizeDesktopBaseURL(raw); err == nil {
			t.Fatalf("normalizeDesktopBaseURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestDesktopBaseURLEnvironmentPrecedence(t *testing.T) {
	env := map[string]string{
		"VOCAT_DESKTOP_URL": "https://127.0.0.1:8443",
		"VOCAT_ADDR":        "0.0.0.0:7575",
	}
	got, err := desktopBaseURLFromEnvironment(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://127.0.0.1:8443" {
		t.Fatalf("desktopBaseURLFromEnvironment() = %q", got)
	}
}
