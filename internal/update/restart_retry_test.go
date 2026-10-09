package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRetryInstalledUpdatePreservesRollbackBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script executable")
	}
	version := "1.1.15-cellbridge-preview.1"
	body := []byte("#!/bin/sh\necho vocat " + version + "\n")
	digest := sha256.Sum256(body)
	target := filepath.Join(t.TempDir(), "halo")
	if err := os.WriteFile(target, body, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".previous", []byte("last-working-version"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := binaryReleaseServer(t, version, body, int64(len(body)), hex.EncodeToString(digest[:]))
	defer server.Close()
	ctx := WithGitHubForTest(WithChannel(context.Background(), ChannelCellbridge), server.URL, server.Client())
	checked := false
	result, err := ApplyLatest(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Repo: "Nan7Li/VoCat", Target: target,
		BeforeInstall: func() error { checked = true; return nil },
	}, false)
	if err != nil || !result.Applied || !checked {
		t.Fatalf("retry = %+v, before-install = %v, error = %v", result, checked, err)
	}
	previous, err := os.ReadFile(target + ".previous")
	if err != nil || string(previous) != "last-working-version" {
		t.Fatalf("rollback binary = %q, error = %v", previous, err)
	}
}
