package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyCellbridgeRejectsBadPayloadAndKeepsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script binary")
	}
	version := "1.1.15-cellbridge-preview.1"
	good := []byte("#!/bin/sh\necho vocat " + version + "\n")
	cases := []struct {
		name    string
		body    []byte
		size    int64
		sum     string
		before  func() error
		wantErr string
	}{
		{
			name:    "bad checksum",
			body:    good,
			size:    int64(len(good)),
			sum:     strings.Repeat("ab", 32),
			wantErr: "sha256",
		},
		{
			name:    "bad size",
			body:    good,
			size:    int64(len(good) + 9),
			wantErr: "size",
		},
		{
			name:    "version mismatch",
			body:    []byte("#!/bin/sh\necho vocat 0.0.1\n"),
			wantErr: "does not match",
		},
		{
			name:    "contains vocat without version",
			body:    []byte("#!/bin/sh\necho this output contains vocat but no version\n"),
			wantErr: "unexpected version",
		},
		{
			name:    "wrong architecture",
			body:    []byte("#!/bin/sh\nexit 1\n"),
			wantErr: "cannot run",
		},
		{
			name:    "call during download",
			body:    good,
			before:  func() error { return errors.New("call appeared") },
			wantErr: "call appeared",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			body := item.body
			size := item.size
			if size == 0 {
				size = int64(len(body))
			}
			sum := item.sum
			if sum == "" {
				digest := sha256.Sum256(body)
				sum = hex.EncodeToString(digest[:])
			}
			target := filepath.Join(t.TempDir(), "halo")
			if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(target), "config.json"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			server := binaryReleaseServer(t, version, body, size, sum)
			defer server.Close()
			ctx := WithGitHubForTest(WithChannel(context.Background(), ChannelCellbridge), server.URL, server.Client())
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			_, err := ApplyLatest(ctx, logger, Options{
				Repo: "Nan7Li/VoCat", Target: target, BeforeInstall: item.before,
			}, false)
			if err == nil || !strings.Contains(err.Error(), item.wantErr) {
				t.Fatalf("error = %v", err)
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil || string(got) != "old-binary" {
				t.Fatalf("target = %q, %v", got, readErr)
			}
			if _, statErr := os.Stat(target + ".previous"); !os.IsNotExist(statErr) {
				t.Fatalf("previous binary stat = %v", statErr)
			}
			kept, _ := os.ReadFile(filepath.Join(filepath.Dir(target), "config.json"))
			if string(kept) != "keep" {
				t.Fatalf("config = %q", kept)
			}
		})
	}
}

func TestApplyCellbridgeInstallsMatchingBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script binary")
	}
	version := "1.1.15-cellbridge-preview.1"
	body := []byte("#!/bin/sh\necho vocat " + version + "\n")
	digest := sha256.Sum256(body)
	dir := t.TempDir()
	target := filepath.Join(dir, "halo")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recording.wav"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := binaryReleaseServer(t, version, body, int64(len(body)), hex.EncodeToString(digest[:]))
	defer server.Close()
	ctx := WithGitHubForTest(WithChannel(context.Background(), ChannelCellbridge), server.URL, server.Client())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result, err := ApplyLatest(ctx, logger, Options{Repo: "Nan7Li/VoCat", Target: target}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Latest != version || result.Channel != ChannelCellbridge {
		t.Fatalf("result = %+v", result)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(body) {
		t.Fatalf("installed = %q, %v", got, err)
	}
	previous, err := os.ReadFile(target + ".previous")
	if err != nil || string(previous) != "old-binary" {
		t.Fatalf("previous = %q, %v", previous, err)
	}
	if kept, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(kept) != "keep" {
		t.Fatalf("config overwritten: %q", kept)
	}
	if audio, _ := os.ReadFile(filepath.Join(dir, "recording.wav")); string(audio) != "audio" {
		t.Fatalf("recording overwritten: %q", audio)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".vocat-update-*"))
	if len(matches) != 0 {
		t.Fatalf("private temp left behind: %v", matches)
	}
}

func binaryReleaseServer(t *testing.T, version string, body []byte, size int64, sum string) *httptest.Server {
	t.Helper()
	assetName := assetNamesFor(runtime.GOOS, runtime.GOARCH)[0]
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			_ = json.NewEncoder(w).Encode([]Release{{
				TagName:    "halo-" + version,
				Prerelease: true,
				Assets: []Asset{
					{Name: assetName, BrowserDownloadURL: server.URL + "/bin", Size: size},
					{Name: "SHA256SUMS", BrowserDownloadURL: server.URL + "/sums", Size: 1},
				},
			}})
		case r.URL.Path == "/bin":
			_, _ = w.Write(body)
		case r.URL.Path == "/sums":
			_, _ = io.WriteString(w, sum+"  "+assetName+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}
