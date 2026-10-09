// Package update implements the `vocat update` self-updater. It queries the
// GitHub Releases API for a newer build, downloads the matching Linux binary
// for the current architecture, verifies it against a published SHA256SUMS,
// atomically replaces the running binary on disk, and restarts the vocat
// systemd unit.
//
// Trust model: GitHub TLS guarantees the channel; the repository owner controls
// which assets are published; SHA256SUMS guards integrity. There is no GPG
// signature verification — an accepted trade-off for a closed-network testing
// tool. Both the CLI and authenticated web UI use this same verified replacement
// path.
package update

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"vocat/internal/buildinfo"
)

// currentBuildVersion is production's buildinfo.Version. Tests do not replace it.
func runningVersion() string { return buildinfo.Version }

// Options captures the resolved flags for an update invocation.
type Options struct {
	Check         bool   // report-only
	Repo          string // owner/name
	Target        string // binary path to replace
	Force         bool   // reinstall even at equal version
	Token         string // optional GitHub bearer token
	Help          bool   // print usage, do nothing
	Channel       string // optional channel override; empty resolves env and version
	BeforeInstall func() error
}

// Run executes the update subcommand. It returns nil on success or when an
// update is reported-but-not-applied under --check; it returns an error only
// when something concrete went wrong.
func Run(logger *slog.Logger, args []string) error {
	opts, err := parseFlags(args)
	if err != nil {
		return err
	}
	if opts.Help {
		printUpdateUsage()
		return nil
	}
	if opts.Repo == "" {
		opts.Repo = strings.TrimSpace(os.Getenv("VOCAT_REPO"))
	}
	if opts.Repo == "" {
		opts.Repo = DefaultRepository
	}
	if opts.Token == "" {
		opts.Token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if opts.Target == "" {
		opts.Target = resolveDefaultTarget()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	channel, err := resolveChannel(ctx, opts.Channel)
	if err != nil {
		return err
	}
	if opts.Channel != "" {
		ctx = WithChannel(ctx, opts.Channel)
	}
	current := runningVersion()
	logger.Info("checking for updates", "repo", opts.Repo, "current", current, "channel", channel)
	result, err := CheckLatest(ctx, opts.Repo, opts.Token, current)
	if err != nil {
		return err
	}
	if !result.Available && !opts.Force {
		logger.Info("already up to date", "version", current, "channel", channel)
		fmt.Printf("vocat %s is already the latest release.\n", current)
		return nil
	}
	if !result.Available && opts.Force {
		equal, equalErr := sameVersion(current, result.Latest)
		if equalErr != nil {
			return equalErr
		}
		if !equal {
			return fmt.Errorf("update: refusing downgrade from %s to %s", current, result.Latest)
		}
	}
	if opts.Check {
		fmt.Printf("update available: %s -> %s\n", current, result.Latest)
		if result.ReleaseNotes != "" {
			fmt.Println(result.ReleaseNotes)
		}
		return nil
	}

	logger.Info("update available", "current", current, "latest", result.Latest, "channel", result.Channel)
	return applyUpdate(ctx, logger, opts, result.Release, result.Latest, true)
}

// ApplyLatest downloads, verifies, and atomically installs the newest trusted
// release. HTTP callers can pass restart=false and restart after flushing the
// response.
func ApplyLatest(ctx context.Context, logger *slog.Logger, opts Options, restart bool) (CheckResult, error) {
	if strings.TrimSpace(opts.Repo) == "" {
		opts.Repo = DefaultRepository
	}
	if strings.TrimSpace(opts.Token) == "" {
		opts.Token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if strings.TrimSpace(opts.Target) == "" {
		opts.Target = resolveDefaultTarget()
	}
	if strings.TrimSpace(opts.Channel) != "" {
		ctx = WithChannel(ctx, opts.Channel)
	}
	current := runningVersion()
	result, err := CheckLatest(ctx, opts.Repo, opts.Token, current)
	if err != nil {
		return CheckResult{}, err
	}
	if !result.Available && !opts.Force {
		return result, nil
	}
	if !result.Available && opts.Force {
		equal, equalErr := sameVersion(current, result.Latest)
		if equalErr != nil {
			return CheckResult{}, equalErr
		}
		if !equal {
			return CheckResult{}, fmt.Errorf("update: refusing downgrade from %s to %s", current, result.Latest)
		}
	}
	if err := applyUpdate(ctx, logger, opts, result.Release, result.Latest, restart); err != nil {
		return CheckResult{}, err
	}
	result.Applied = true
	return result, nil
}

func applyUpdate(ctx context.Context, logger *slog.Logger, opts Options, release *Release, latest string, restart bool) error {
	if release == nil {
		return fmt.Errorf("update: release metadata is missing")
	}
	channel, err := resolveChannel(ctx, opts.Channel)
	if err != nil {
		return err
	}
	if err := versionMatchesChannel(latest, channel); err != nil {
		return err
	}
	assetNames := assetNamesFor(runtime.GOOS, runtime.GOARCH)
	var asset *Asset
	for _, name := range assetNames {
		if asset = findAsset(release, name); asset != nil {
			break
		}
	}
	if asset == nil {
		return fmt.Errorf("update: release %s has none of assets %q for %s/%s", release.TagName, assetNames, runtime.GOOS, runtime.GOARCH)
	}

	sumsAsset := findAsset(release, "SHA256SUMS")
	if sumsAsset == nil {
		return fmt.Errorf("update: release %s missing SHA256SUMS — refusing to install unverified", release.TagName)
	}

	// The download lives in a private directory on the target's filesystem.
	// os.Rename then stays atomic, and another local user cannot replace the
	// verified file before it is installed.
	targetDir := filepath.Dir(opts.Target)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("update: ensure target dir %s: %w", targetDir, err)
	}
	tmpDir, err := os.MkdirTemp(targetDir, ".vocat-update-")
	if err != nil {
		return fmt.Errorf("update: create private temp dir: %w", err)
	}
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("update: protect temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmpPath := filepath.Join(tmpDir, "binary")
	tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("update: create temp file: %w", err)
	}
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
	}()

	logger.Info("downloading binary", "asset", asset.Name, "size", asset.Size, "url", asset.BrowserDownloadURL)
	if err := downloadAssetWithProgress(ctx, logger, asset, opts.Token, tmp); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update: finalize temp file: %w", err)
	}
	tmp = nil

	var sums bytes.Buffer
	if err := downloadAsset(ctx, sumsAsset.BrowserDownloadURL, opts.Token, &sums); err != nil {
		return err
	}
	expectedHash, err := ParseSHA256SUMS(sums.String(), asset.Name)
	if err != nil {
		return err
	}
	ok, err := VerifyFileSHA256(tmpPath, expectedHash)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("update: sha256 mismatch for %s — refusing to install", asset.Name)
	}
	logger.Info("verified binary", "sha256", expectedHash)

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("update: chmod temp binary: %w", err)
	}
	if err := validateExecutable(ctx, tmpPath, latest, channel); err != nil {
		return err
	}
	if opts.BeforeInstall != nil {
		if err := opts.BeforeInstall(); err != nil {
			return err
		}
	}
	// If a verified installation already succeeded but its restart failed,
	// retry the restart without replacing .previous with the new binary itself.
	alreadyInstalled, _ := VerifyFileSHA256(opts.Target, expectedHash)
	if !alreadyInstalled {
		if err := backupAndReplace(opts.Target, tmpPath); err != nil {
			return err
		}
	}
	logger.Info("installed new binary", "target", opts.Target, "version", latest)
	fmt.Printf("vocat updated to %s.\n", latest)

	if restart {
		if err := RestartService(logger); err != nil {
			// The file replacement already succeeded; a restart failure is not
			// fatal — the operator can restart the service manually.
			fmt.Printf("Binary replaced, but automatic restart failed: %v\n", err)
			fmt.Println("Restart the vocat service manually to apply the new build.")
		}
	}
	return nil
}

type downloadProgressWriter struct {
	destination io.Writer
	downloaded  atomic.Int64
}

func (writer *downloadProgressWriter) Write(data []byte) (int, error) {
	written, err := writer.destination.Write(data)
	writer.downloaded.Add(int64(written))
	return written, err
}

func downloadAssetWithProgress(
	ctx context.Context,
	logger *slog.Logger,
	asset *Asset,
	token string,
	destination io.Writer,
) error {
	progress := &downloadProgressWriter{destination: destination}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				downloaded := progress.downloaded.Load()
				percent := float64(0)
				if asset.Size > 0 {
					percent = float64(downloaded) * 100 / float64(asset.Size)
				}
				logger.Info(
					"download progress",
					"asset", asset.Name,
					"downloaded", downloaded,
					"total", asset.Size,
					"percent", fmt.Sprintf("%.1f", percent),
				)
			}
		}
	}()
	err := downloadAsset(ctx, asset.BrowserDownloadURL, token, progress)
	close(done)
	if err != nil {
		return err
	}
	if asset.Size > 0 && progress.downloaded.Load() != asset.Size {
		return fmt.Errorf(
			"update: asset size mismatch for %s: downloaded %d bytes, expected %d",
			asset.Name,
			progress.downloaded.Load(),
			asset.Size,
		)
	}
	logger.Info("download completed", "asset", asset.Name, "bytes", progress.downloaded.Load())
	return nil
}

// validateExecutable catches incompatible architectures and missing dynamic
// loaders before the working installation is touched. A valid checksum alone
// cannot detect those packaging errors.
func validateExecutable(ctx context.Context, path, expectedVersion, channel string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("update: downloaded binary cannot run on this host: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	got, err := parseReportedVersion(string(output))
	if err != nil {
		return err
	}
	if got != expectedVersion {
		return fmt.Errorf("update: downloaded binary version %s does not match release %s", got, expectedVersion)
	}
	if err := versionMatchesChannel(got, channel); err != nil {
		return err
	}
	return nil
}

func parseReportedVersion(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for index, field := range fields {
			name := strings.ToLower(strings.Trim(field, ":"))
			if name != "vocat" && name != "halo" {
				continue
			}
			if index+1 >= len(fields) {
				continue
			}
			version := strings.TrimPrefix(fields[index+1], "v")
			if _, err := parseSemanticVersion(version); err != nil {
				return "", fmt.Errorf("update: downloaded binary returned an unexpected version response: %q", strings.TrimSpace(output))
			}
			return version, nil
		}
	}
	return "", fmt.Errorf("update: downloaded binary returned an unexpected version response: %q", strings.TrimSpace(output))
}

// backupAndReplace renames the current binary aside, then moves the verified
// temp file into place. Both renames are atomic on the same filesystem. The
// previous working binary is retained for service-level or manual rollback.
func backupAndReplace(target, tmp string) error {
	backup := target + ".previous"
	if _, err := os.Stat(target); err == nil {
		_ = os.Remove(backup)
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("update: move current binary aside: %w", err)
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		// Best-effort rollback so the operator is not left without a binary.
		if _, statErr := os.Stat(backup); statErr == nil {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("update: move new binary into place: %w", err)
	}
	return nil
}

type serviceRestartPlan struct {
	openwrt bool
	unit    string
}

func openWrtInitExists() bool {
	_, err := os.Stat("/etc/init.d/vocat")
	return err == nil
}

func restartPlan(unitEnv string, openwrt bool, logger *slog.Logger) serviceRestartPlan {
	if unit := strings.TrimSpace(unitEnv); validSystemdUnit.MatchString(unit) {
		return serviceRestartPlan{unit: unit}
	}
	if openwrt {
		return serviceRestartPlan{openwrt: true}
	}
	return serviceRestartPlan{unit: detectSystemdUnit(logger)}
}

func planServiceRestart(logger *slog.Logger) serviceRestartPlan {
	return restartPlan(os.Getenv("VOCAT_SYSTEMD_UNIT"), openWrtInitExists(), logger)
}

// RestartService supports both systemd hosts and OpenWrt/procd routers.
// VOCAT_SYSTEMD_UNIT selects the NAS unit and is not replaced by an old
// OpenWrt init script. Without that variable, OpenWrt behavior is unchanged.
func RestartService(logger *slog.Logger) error {
	plan := planServiceRestart(logger)
	if plan.openwrt {
		cmd := exec.Command("/etc/init.d/vocat", "restart")
		if out, err := cmd.CombinedOutput(); err != nil {
			if logger != nil {
				logger.Warn("OpenWrt service restart failed", "error", err, "output", string(out))
			}
			return fmt.Errorf("restart OpenWrt vocat service: %w", err)
		}
		return nil
	}
	if plan.unit == "" {
		return fmt.Errorf("neither /etc/init.d/vocat nor systemctl is available")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("neither /etc/init.d/vocat nor systemctl is available")
	}
	unit := plan.unit
	// Queue the restart and let systemctl exit before systemd stops this unit.
	// A blocking restart command becomes part of vocat.service's own cgroup and
	// waits for that same cgroup to terminate, creating a stop-timeout cycle.
	cmd := exec.Command("systemctl", "restart", "--no-block", unit)
	if out, err := cmd.CombinedOutput(); err != nil {
		if logger != nil {
			logger.Warn("systemctl restart failed", "error", err, "output", string(out))
		}
		return fmt.Errorf("systemctl restart %s: %w", unit, err)
	}
	return nil
}

var validSystemdUnit = regexp.MustCompile(`^[A-Za-z0-9_.@:-]+\.service$`)

func detectSystemdUnit(logger *slog.Logger) string {
	if configured := strings.TrimSpace(os.Getenv("VOCAT_SYSTEMD_UNIT")); validSystemdUnit.MatchString(configured) {
		return configured
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if unit := systemdUnitFromCgroup(string(data)); unit != "" {
			return unit
		}
	}
	// Some cgroup namespaces hide the unit name. Query loaded services and
	// identify the unit whose MainPID is this process before falling back.
	list := exec.Command("systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain")
	if output, err := list.Output(); err == nil {
		pid := strconv.Itoa(os.Getpid())
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || !validSystemdUnit.MatchString(fields[0]) {
				continue
			}
			show := exec.Command("systemctl", "show", fields[0], "--property=MainPID", "--value")
			if value, showErr := show.Output(); showErr == nil && strings.TrimSpace(string(value)) == pid {
				return fields[0]
			}
		}
	}
	if logger != nil {
		logger.Warn("could not identify the current systemd unit; using vocat.service", "hint", "set VOCAT_SYSTEMD_UNIT for a custom unit")
	}
	return "vocat.service"
}

func systemdUnitFromCgroup(data string) string {
	for _, line := range strings.Split(data, "\n") {
		for _, part := range strings.Split(line, "/") {
			part = strings.TrimSpace(part)
			if validSystemdUnit.MatchString(part) {
				return part
			}
		}
	}
	return ""
}

// resolveDefaultTarget prefers VOCAT_UPDATE_TARGET, then the running
// executable. An old /opt/vocat/bin/vocat install is not selected merely
// because that file exists beside a Halo binary.
func resolveDefaultTarget() string {
	executable := ""
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			executable = resolved
		} else {
			executable = exe
		}
	}
	return resolveInstallTarget(os.Getenv("VOCAT_UPDATE_TARGET"), executable)
}

func resolveInstallTarget(explicit, executable string) string {
	if target := strings.TrimSpace(explicit); target != "" {
		return target
	}
	if executable != "" {
		return executable
	}
	return "/opt/vocat/bin/vocat"
}

func findAsset(release *Release, name string) *Asset {
	for i := range release.Assets {
		if release.Assets[i].Name == name {
			return &release.Assets[i]
		}
	}
	return nil
}

func assetNamesFor(goos, goarch string) []string {
	if goos == "linux" && goarch == "arm64" {
		// Halo releases publish halo-linux-*; keep VoCat names as fallback so a
		// Halo host can still consume an upstream vocat-linux-* asset.
		return []string{"halo-linux-arm64", "halo-linux-aarch64", "vocat-linux-arm64", "vocat-linux-aarch64"}
	}
	if goos == "linux" && goarch == "arm" {
		// Official 32-bit ARM builds target GOARM=7. Keep the generic legacy
		// name as a fallback for installations consuming an older release.
		return []string{"halo-linux-armv7", "halo-linux-arm", "vocat-linux-armv7", "vocat-linux-arm"}
	}
	return []string{
		fmt.Sprintf("halo-%s-%s", goos, goarch),
		fmt.Sprintf("vocat-%s-%s", goos, goarch),
	}
}

func printUpdateUsage() {
	fmt.Println(`Usage: vocat update [flags]

Fetch the latest release from GitHub and replace this binary in place.

Flags:
  --check            Report whether an update is available, then exit.
  --force            Reinstall even when already at the latest version.
  --repo owner/name  GitHub repository (default: $VOCAT_REPO or Nan7Li/VoCat).
  --target path      Binary to replace (default: $VOCAT_UPDATE_TARGET, otherwise
                     the running executable).
  --token token      GitHub bearer token (default: $GITHUB_TOKEN).
  -h, --help         Show this help.

Environment:
  VOCAT_REPO            Fallback for --repo.
  VOCAT_UPDATE_CHANNEL  stable or cellbridge. Unset uses cellbridge when this
                        build's version contains -cellbridge, otherwise stable.
  VOCAT_UPDATE_TARGET   Fallback for --target.
  VOCAT_SYSTEMD_UNIT    systemd unit to restart after install.
  GITHUB_TOKEN          Fallback for --token. Required for private repos and
                        recommended to avoid unauthenticated rate limits.`)
}

func parseFlags(args []string) (Options, error) {
	var opts Options
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--check":
			opts.Check = true
		case arg == "--force":
			opts.Force = true
		case arg == "--repo":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("update: --repo requires a value")
			}
			opts.Repo = args[i]
		case strings.HasPrefix(arg, "--repo="):
			opts.Repo = strings.TrimPrefix(arg, "--repo=")
		case arg == "--target":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("update: --target requires a value")
			}
			opts.Target = args[i]
		case strings.HasPrefix(arg, "--target="):
			opts.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--token":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("update: --token requires a value")
			}
			opts.Token = args[i]
		case strings.HasPrefix(arg, "--token="):
			opts.Token = strings.TrimPrefix(arg, "--token=")
		case arg == "-h" || arg == "--help":
			opts.Help = true
		default:
			return opts, fmt.Errorf("update: unknown flag %q", arg)
		}
	}
	return opts, nil
}
