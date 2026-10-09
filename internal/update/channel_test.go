package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUpdateChannel(t *testing.T) {
	channel, err := resolveUpdateChannel("", "", "", "1.1.14-cellbridge")
	if err != nil || channel != ChannelCellbridge {
		t.Fatalf("auto cellbridge = %q, %v", channel, err)
	}
	channel, err = resolveUpdateChannel("", "", "", "1.1.14")
	if err != nil || channel != ChannelStable {
		t.Fatalf("auto stable = %q, %v", channel, err)
	}
	channel, err = resolveUpdateChannel("stable", "", "", "1.1.14-cellbridge")
	if err != nil || channel != ChannelStable {
		t.Fatalf("explicit stable = %q, %v", channel, err)
	}
	channel, err = resolveUpdateChannel("", "cellbridge", "stable", "1.1.14")
	if err != nil || channel != ChannelCellbridge {
		t.Fatalf("context before env = %q, %v", channel, err)
	}
	if _, err = resolveUpdateChannel("nightly", "", "", "1.1.14"); err == nil {
		t.Fatal("garbage channel was accepted")
	}
}

func TestResolveInstallTargetPrefersExplicitPath(t *testing.T) {
	if got := resolveInstallTarget("/opt/halo/bin/halo", "/opt/vocat/bin/vocat"); got != "/opt/halo/bin/halo" {
		t.Fatalf("explicit target = %q", got)
	}
	if got := resolveInstallTarget("", "/usr/local/bin/halo"); got != "/usr/local/bin/halo" {
		t.Fatalf("executable target = %q", got)
	}
	if got := resolveInstallTarget("  ", ""); got != "/opt/vocat/bin/vocat" {
		t.Fatalf("fallback target = %q", got)
	}
}

func TestCellbridgeSelectsNewestPreviewNotStable(t *testing.T) {
	sawLatest := false
	pages := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "per_page=100") {
			t.Errorf("page query = %q", r.URL.RawQuery)
		}
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			sawLatest = true
			http.NotFound(w, r)
			return
		}
		page := r.URL.Query().Get("page")
		pages[page]++
		var releases []Release
		if page == "1" {
			releases = append(releases, Release{TagName: "halo-1.1.14-cellbridge-preview.2", Prerelease: true})
			for i := 0; i < 99; i++ {
				releases = append(releases, Release{TagName: fmt.Sprintf("halo-nightly-%d", i)})
			}
		} else {
			releases = []Release{
				{TagName: "halo-1.1.14-cellbridge-preview.99", Draft: true, Prerelease: true},
				{TagName: "v1.1.14"},
				{TagName: "halo-1.1.14-cellbridge-preview.10", Prerelease: true, Body: "fusion"},
				{TagName: "halo-nightly"},
				{TagName: "halo-1.1.14-cellbridge-preview"},
				{TagName: "v1.2.0-rc.1", Prerelease: true},
				{TagName: "not a tag"},
			}
		}
		_ = json.NewEncoder(w).Encode(releases)
	}))
	defer server.Close()

	ctx := WithGitHubForTest(WithChannel(context.Background(), ChannelCellbridge), server.URL, server.Client())
	result, err := CheckLatest(ctx, "Nan7Li/VoCat", "", "1.1.14-cellbridge")
	if err != nil {
		t.Fatal(err)
	}
	if sawLatest {
		t.Fatal("cellbridge channel requested /releases/latest")
	}
	if pages["1"] != 1 || pages["2"] != 1 {
		t.Fatalf("pages = %#v", pages)
	}
	if !result.Available || result.Latest != "1.1.14-cellbridge-preview.10" || result.Channel != ChannelCellbridge {
		t.Fatalf("result = %+v", result)
	}
	if result.Latest == "1.1.14" || result.Release == nil || result.Release.TagName != "halo-1.1.14-cellbridge-preview.10" {
		t.Fatalf("selected release %+v", result.Release)
	}
}

func TestCellbridgeRefusesDowngradeToOlderPreviewOrStable(t *testing.T) {
	server := cellbridgeListServer(t, []Release{
		{TagName: "v1.1.14"},
		{TagName: "halo-1.1.14-cellbridge-preview.2", Prerelease: true},
		{TagName: "halo-1.1.14-cellbridge-preview.99", Draft: true, Prerelease: true},
	})
	defer server.Close()
	ctx := WithGitHubForTest(WithChannel(context.Background(), ChannelCellbridge), server.URL, server.Client())
	result, err := CheckLatest(ctx, "Nan7Li/VoCat", "", "1.1.14-cellbridge-preview.10")
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Latest != "1.1.14-cellbridge-preview.2" || result.Channel != ChannelCellbridge {
		t.Fatalf("result = %+v", result)
	}

	onlyStable := cellbridgeListServer(t, []Release{{TagName: "v1.1.14"}})
	defer onlyStable.Close()
	ctx = WithGitHubForTest(WithChannel(context.Background(), ChannelCellbridge), onlyStable.URL, onlyStable.Client())
	if _, err := CheckLatest(ctx, "Nan7Li/VoCat", "", "1.1.14-cellbridge"); err == nil || !strings.Contains(err.Error(), "no published cellbridge release") {
		t.Fatalf("stable-only list error = %v", err)
	}
}

func TestStableChannelIgnoresCellbridgePreview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			t.Errorf("stable channel requested %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(Release{TagName: "v1.1.15", Body: "stable"})
	}))
	defer server.Close()
	ctx := WithGitHubForTest(WithChannel(context.Background(), ChannelStable), server.URL, server.Client())
	result, err := CheckLatest(ctx, "Nan7Li/VoCat", "", "1.1.14")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Latest != "1.1.15" || result.Channel != ChannelStable {
		t.Fatalf("result = %+v", result)
	}

	prerelease := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Release{TagName: "halo-1.1.14-cellbridge-preview.10", Prerelease: true})
	}))
	defer prerelease.Close()
	ctx = WithGitHubForTest(WithChannel(context.Background(), ChannelStable), prerelease.URL, prerelease.Client())
	if _, err := CheckLatest(ctx, "Nan7Li/VoCat", "", "1.1.14"); err == nil {
		t.Fatal("stable channel accepted a prerelease")
	}

	fusion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Release{TagName: "v1.1.14-cellbridge"})
	}))
	defer fusion.Close()
	ctx = WithGitHubForTest(WithChannel(context.Background(), ChannelStable), fusion.URL, fusion.Client())
	if _, err := CheckLatest(ctx, "Nan7Li/VoCat", "", "1.1.14"); err == nil || !strings.Contains(err.Error(), "cellbridge") {
		t.Fatalf("stable cellbridge latest error = %v", err)
	}
}

func TestForceRefusesDowngradeWithoutReplacingTarget(t *testing.T) {
	server := cellbridgeListServer(t, []Release{{TagName: "halo-1.1.13-cellbridge", Prerelease: true}})
	defer server.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "halo")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := WithGitHubForTest(WithChannel(context.Background(), ChannelCellbridge), server.URL, server.Client())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := ApplyLatest(ctx, logger, Options{Repo: "Nan7Li/VoCat", Target: target, Force: true}, false)
	if err == nil || !strings.Contains(err.Error(), "refusing downgrade") {
		t.Fatalf("downgrade error = %v", err)
	}
	body, readErr := os.ReadFile(target)
	if readErr != nil || string(body) != "old-binary" {
		t.Fatalf("target = %q, %v", body, readErr)
	}
}

func cellbridgeListServer(t *testing.T, releases []Release) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			t.Errorf("cellbridge check used %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(releases)
	}))
}
