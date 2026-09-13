//go:build windows

package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHelperFlagsRequiresAbsoluteSiblingPaths(t *testing.T) {
	directory := t.TempDir()
	options, err := parseHelperFlags([]string{
		"--target", filepath.Join(directory, "vocat.exe"),
		"--replacement", filepath.Join(directory, ".vocat-update.exe"),
		"--service", "Halo-Test",
		"--parent-pid", "1234",
		"--delay-ms", "2000",
		"--version", "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.service != "Halo-Test" || options.parentPID != 1234 || options.delay.Milliseconds() != 2000 {
		t.Fatalf("unexpected helper options: %#v", options)
	}
	if _, err := parseHelperFlags([]string{
		"--target", filepath.Join(directory, "vocat.exe"),
		"--replacement", filepath.Join(directory, "other", "replacement.exe"),
		"--service", "Halo",
		"--parent-pid", "1",
	}); err == nil {
		t.Fatal("helper accepted a replacement on a different directory")
	}
}

func TestParseHelperFlagsRejectsUnknownOrSecretFlags(t *testing.T) {
	directory := t.TempDir()
	base := []string{
		"--target", filepath.Join(directory, "vocat.exe"),
		"--replacement", filepath.Join(directory, "replacement.exe"),
		"--service", "Halo",
		"--parent-pid", "1",
	}
	for _, name := range []string{"--token", "--password", "--apn-password", "--unknown"} {
		args := append(append([]string(nil), base...), name, "secret")
		if _, err := parseHelperFlags(args); err == nil {
			t.Fatalf("helper accepted forbidden/unknown flag %s", name)
		}
	}
}

func TestCopyFileSyncLeavesRunningHelperSourceInPlace(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "replacement.exe")
	destination := filepath.Join(directory, "vocat.exe")
	want := []byte("verified replacement")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFileSync(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("copied contents = %q, want %q", got, want)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("helper source was moved or removed: %v", err)
	}
	if err := copyFileSync(source, destination); err == nil {
		t.Fatal("copy overwrote an existing destination")
	}
}

func TestWindowsHelperEnvironmentDropsCredentials(t *testing.T) {
	got := windowsHelperEnvironment([]string{
		`SystemRoot=C:\Windows`,
		`GITHUB_TOKEN=github-secret`,
		`VOCAT_API_PASSWORD=vocat-secret`,
		`APN_PASSWORD=carrier-secret`,
		`SOME_CLIENT_SECRET=oauth-secret`,
	})
	joined := strings.Join(got, "\n")
	if joined != `SystemRoot=C:\Windows` {
		t.Fatalf("unexpected helper environment: %q", joined)
	}
}
