//go:build windows

package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestAssetNamesForWindowsPrefersExeAndKeepsLegacyNames(t *testing.T) {
	tests := []struct {
		goarch string
		want   []string
	}{
		{"amd64", []string{"halo-windows-amd64.exe", "vocat-windows-amd64.exe", "halo-windows-amd64", "vocat-windows-amd64"}},
		{"arm64", []string{"halo-windows-arm64.exe", "vocat-windows-arm64.exe", "halo-windows-arm64", "vocat-windows-arm64"}},
	}
	for _, item := range tests {
		got := assetNamesFor("windows", item.goarch)
		if !reflect.DeepEqual(got, item.want) {
			t.Errorf("assetNamesFor(windows, %q) = %#v, want %#v", item.goarch, got, item.want)
		}
	}
	linux := assetNamesFor("linux", "amd64")
	if !reflect.DeepEqual(linux, []string{"halo-linux-amd64", "vocat-linux-amd64"}) {
		t.Errorf("linux asset names changed: %#v", linux)
	}
}

func TestWindowsServiceExecutableMatchesTarget(t *testing.T) {
	target := `C:\Program Files\Halo\halo.exe`
	matches := []string{
		target,
		`C:\Program Files\Halo\HALO.EXE`,
		`C:/Program Files/Halo/halo.exe`,
		`"C:\Program Files\Halo\halo.exe"`,
		`"C:\Program Files\Halo\halo.exe" --service`,
		`C:\Program Files\Halo\halo.exe --service`,
		`\\?\C:\Program Files\Halo\halo.exe`,
		`\??\C:\Program Files\Halo\halo.exe`,
	}
	for _, binary := range matches {
		if !windowsServiceExecutableMatchesTarget(binary, target) {
			t.Errorf("expected match for %q", binary)
		}
	}
	mismatches := []string{
		"",
		`halo.exe`,
		`C:\Program Files\Halo\other.exe`,
		`C:\Program Files\Other\halo.exe`,
		`"C:\Program Files\Other\halo.exe" --service`,
		`D:\Program Files\Halo\halo.exe`,
	}
	for _, binary := range mismatches {
		if windowsServiceExecutableMatchesTarget(binary, target) {
			t.Errorf("expected mismatch for %q", binary)
		}
	}
}

func TestStageSiblingHelperCopiesVerifiedBytesBesideTarget(t *testing.T) {
	directory := t.TempDir()
	sourceDir := filepath.Join(directory, ".vocat-update-src")
	replacement := filepath.Join(sourceDir, "binary")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("V"), 8192)
	if err := os.WriteFile(replacement, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := stageSiblingHelper(replacement, directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := stageSiblingHelper(replacement, directory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(first, second) {
		t.Fatalf("helper name was not random: %s", first)
	}
	for _, helper := range []string{first, second} {
		if !sameWindowsDirectory(helper, filepath.Join(directory, "halo.exe")) {
			t.Fatalf("helper %s is not beside %s", helper, directory)
		}
		if !strings.EqualFold(filepath.Ext(helper), ".exe") {
			t.Fatalf("helper %s is not an executable name", helper)
		}
		if !strings.HasPrefix(filepath.Base(helper), ".vocat-update-") {
			t.Fatalf("helper name = %s", filepath.Base(helper))
		}
		got, err := os.ReadFile(helper)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("helper contents = %q", got)
		}
	}
	source, err := os.ReadFile(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, payload) {
		t.Fatal("verified source was moved or modified")
	}
	if _, err := parseHelperFlags([]string{
		"--target", filepath.Join(directory, "halo.exe"),
		"--replacement", first,
		"--service", "Halo",
		"--parent-pid", "1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyExclusiveSyncDoesNotReplaceExistingFile(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source.exe")
	destination := filepath.Join(directory, "helper.exe")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyExclusiveSync(source, destination); err == nil {
		t.Fatal("expected exclusive create to fail")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestCopyExclusiveSyncRemovesPartialDestination(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "not-a-file")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "partial.exe")
	if err := copyExclusiveSync(source, destination); err == nil {
		t.Fatal("expected copy of a directory to fail")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial helper remains: %v", err)
	}
}

func TestCopyExclusiveSyncMissingSourceDoesNotRemoveExistingFile(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "existing.exe")
	if err := os.WriteFile(destination, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyExclusiveSync(filepath.Join(directory, "missing.exe"), destination); err == nil {
		t.Fatal("missing source accepted")
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "keep" {
		t.Fatalf("unowned destination changed: %q %v", got, err)
	}
}

func TestServicePathDoesNotMatchOnlyExecutableDirectoryPrefix(t *testing.T) {
	if windowsServiceExecutableMatchesTarget(`C:\app.exe-dir\other.exe`, `C:\app.exe-dir\halo.exe`) {
		t.Fatal("different service executable accepted")
	}
}

func TestRemoveIncompleteHelperKeepsDirectories(t *testing.T) {
	directory := t.TempDir()
	nested := filepath.Join(directory, "dir.exe")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeIncompleteHelper(nested); err == nil {
		t.Fatal("expected directory removal to be refused")
	}
	info, err := os.Stat(nested)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory helper path was removed: %v", err)
	}
	if err := removeIncompleteHelper(filepath.Join(directory, "missing.exe")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallVerifiedBinaryRejectsMissingServiceWithoutStaging(t *testing.T) {
	directory := t.TempDir()
	target, replacement := writeVerifiedDownload(t, directory, []byte("verified"))
	withWindowsServiceLookup(t, func(name string) (string, error) {
		if name != "Halo" {
			t.Errorf("service name = %q", name)
		}
		return "", errWindowsServiceNotInstalled
	})
	withDetachedStart(t, func(*exec.Cmd) error {
		t.Fatal("helper started without an installed service")
		return errors.New("started")
	})

	err := installVerifiedBinary(context.Background(), discardLogger(), target, replacement, "1.2.3", false)
	if err == nil {
		t.Fatal("missing service reported success")
	}
	if !strings.Contains(err.Error(), "automatic Windows service update is not supported") || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("error = %v", err)
	}
	if helpers := stagedHelpers(t, directory); len(helpers) != 0 {
		t.Fatalf("staged helper despite missing service: %v", helpers)
	}
}

func TestInstallVerifiedBinaryRejectsServiceExecutableMismatch(t *testing.T) {
	directory := t.TempDir()
	target, replacement := writeVerifiedDownload(t, directory, []byte("verified"))
	withWindowsServiceLookup(t, func(string) (string, error) {
		return `C:\Program Files\Halo\halo.exe`, nil
	})
	withDetachedStart(t, func(*exec.Cmd) error {
		t.Fatal("helper started for a different service executable")
		return errors.New("started")
	})

	err := installVerifiedBinary(context.Background(), discardLogger(), target, replacement, "1.2.3", true)
	if err == nil {
		t.Fatal("mismatched service executable reported success")
	}
	if strings.Contains(err.Error(), "not supported") || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
	if helpers := stagedHelpers(t, directory); len(helpers) != 0 {
		t.Fatalf("staged helper despite executable mismatch: %v", helpers)
	}
}

func TestInstallVerifiedBinaryDoesNotTreatSCMErrorsAsSuccess(t *testing.T) {
	directory := t.TempDir()
	target, replacement := writeVerifiedDownload(t, directory, []byte("verified"))
	withWindowsServiceLookup(t, func(string) (string, error) {
		return "", errors.New("access denied")
	})
	withDetachedStart(t, func(*exec.Cmd) error {
		t.Fatal("helper started after an SCM read error")
		return errors.New("started")
	})

	err := installVerifiedBinary(context.Background(), discardLogger(), target, replacement, "1.2.3", true)
	if err == nil || strings.Contains(err.Error(), "not supported") || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallVerifiedBinaryStagesSiblingAndCleansUpWhenStartFails(t *testing.T) {
	directory := t.TempDir()
	payload := []byte("verified-sibling")
	target, replacement := writeVerifiedDownload(t, directory, payload)
	withWindowsServiceLookup(t, func(string) (string, error) {
		return `"` + target + `" --service`, nil
	})
	var seenPath string
	var seenArgs []string
	withDetachedStart(t, func(command *exec.Cmd) error {
		seenPath = command.Path
		seenArgs = append([]string(nil), command.Args...)
		got, err := os.ReadFile(command.Path)
		if err != nil {
			t.Errorf("read staged helper: %v", err)
		} else if !bytes.Equal(got, payload) {
			t.Errorf("staged helper = %q", got)
		}
		return errors.New("blocked")
	})

	err := installVerifiedBinary(context.Background(), discardLogger(), target, replacement, "1.2.3", false)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error = %v", err)
	}
	if seenPath == "" || strings.EqualFold(seenPath, replacement) {
		t.Fatalf("helper path = %s, replacement = %s", seenPath, replacement)
	}
	if !sameWindowsDirectory(seenPath, target) || !strings.EqualFold(filepath.Ext(seenPath), ".exe") {
		t.Fatalf("helper path = %s, target = %s", seenPath, target)
	}
	flags := seenArgs
	for index, arg := range seenArgs {
		if arg == "--target" {
			flags = seenArgs[index:]
			break
		}
	}
	options, err := parseHelperFlags(flags)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(options.replacement, seenPath) || !strings.EqualFold(options.target, target) {
		t.Fatalf("helper flags = %#v", options)
	}
	if _, err := os.Lstat(seenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed helper was not removed: %v", err)
	}
	if got, err := os.ReadFile(replacement); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("source after failed start = %q, %v", got, err)
	}
}

func TestInstallVerifiedBinaryKeepsSiblingWhenHandoffStarts(t *testing.T) {
	directory := t.TempDir()
	payload := []byte("verified-running-helper")
	target, replacement := writeVerifiedDownload(t, directory, payload)
	withWindowsServiceLookup(t, func(string) (string, error) {
		return strings.ToUpper(target), nil
	})
	var seenPath string
	withDetachedStart(t, func(command *exec.Cmd) error {
		seenPath = command.Path
		command.Process = &os.Process{Pid: 4242}
		return nil
	})

	if err := installVerifiedBinary(context.Background(), discardLogger(), target, replacement, "9.9.9", true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(seenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("kept helper = %q", got)
	}
	if !sameWindowsDirectory(seenPath, target) {
		t.Fatalf("kept helper %s is not beside %s", seenPath, target)
	}
}

func writeVerifiedDownload(t *testing.T, directory string, payload []byte) (string, string) {
	t.Helper()
	target := filepath.Join(directory, "halo.exe")
	replacement := filepath.Join(directory, ".vocat-update-src", "binary")
	if err := os.MkdirAll(filepath.Dir(replacement), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOCAT_WINDOWS_SERVICE_NAME", "Halo")
	return target, replacement
}

func withWindowsServiceLookup(t *testing.T, lookup func(string) (string, error)) {
	t.Helper()
	previous := lookupWindowsServiceExecutable
	lookupWindowsServiceExecutable = lookup
	t.Cleanup(func() { lookupWindowsServiceExecutable = previous })
}

func withDetachedStart(t *testing.T, start func(*exec.Cmd) error) {
	t.Helper()
	previous := startDetachedProcess
	startDetachedProcess = start
	t.Cleanup(func() { startDetachedProcess = previous })
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func stagedHelpers(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".vocat-update-") && strings.HasSuffix(strings.ToLower(name), ".exe") {
			names = append(names, name)
		}
	}
	return names
}
