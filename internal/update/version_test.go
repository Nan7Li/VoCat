package update

import "testing"

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.0.3", "v0.0.4", true},
		{"0.0.4", "v0.0.4", false},
		{"0.1.0-dev", "v0.0.4", false},
		{"0.1.0-dev", "v0.1.0", true},
		{"1.2.3-rc.1", "v1.2.3-rc.2", true},
		{"1.2.3", "v1.2.3-rc.2", false},
		{"1.1.14-cellbridge", "1.1.14-cellbridge-preview.2", true},
		{"1.1.14-cellbridge-preview.2", "1.1.14-cellbridge-preview.10", true},
		{"1.1.14-cellbridge-preview.10", "1.1.14-cellbridge-preview.2", false},
		{"1.1.14", "1.1.14-cellbridge-preview.10", false},
		{"1.1.14-cellbridge-preview.10", "1.1.14", true},
	}
	for _, item := range tests {
		got, err := IsNewerVersion(item.current, item.latest)
		if err != nil {
			t.Errorf("IsNewerVersion(%q, %q): %v", item.current, item.latest, err)
			continue
		}
		if got != item.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", item.current, item.latest, got, item.want)
		}
	}
}

func TestIsNewerVersionRejectsInvalidRelease(t *testing.T) {
	if _, err := IsNewerVersion("0.1.0", "nightly"); err == nil {
		t.Fatal("invalid latest version was accepted")
	}
}
