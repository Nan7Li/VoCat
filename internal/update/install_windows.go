//go:build windows

package update

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const defaultWindowsServiceName = "Halo"

var validWindowsServiceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`)

// errWindowsServiceNotInstalled is returned by the read-only service lookup
// when SCM has no such service. Electron portable mode is that case.
var errWindowsServiceNotInstalled = errors.New("windows service is not installed")

// lookupWindowsServiceExecutable only reads the service ImagePath.
// Tests replace it so file tests never connect to SCM.
var lookupWindowsServiceExecutable = queryWindowsServiceExecutable

// startDetachedProcess starts the staged helper. Tests replace it so they
// never execute a binary or change SCM.
var startDetachedProcess = func(command *exec.Cmd) error {
	return command.Start()
}

type helperOptions struct {
	target      string
	replacement string
	service     string
	version     string
	parentPID   uint32
	delay       time.Duration
}

type helperResult struct {
	Success bool   `json:"success"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
	Time    string `json:"time"`
}

func installVerifiedBinary(
	_ context.Context,
	logger *slog.Logger,
	target string,
	replacement string,
	latest string,
	restart bool,
) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("update: resolve target path: %w", err)
	}
	replacement, err = filepath.Abs(replacement)
	if err != nil {
		return fmt.Errorf("update: resolve replacement path: %w", err)
	}
	serviceName := windowsServiceName()
	if !validWindowsServiceName.MatchString(serviceName) {
		return fmt.Errorf("update: invalid Windows service name %q", serviceName)
	}
	// The verified download lives in targetDir/.vocat-update-xxx/binary, which
	// the helper rejects. Copy it to a sibling .exe before handover. The helper
	// still requires that sibling path and performs the SCM stop/replace itself.
	helper, err := stageVerifiedWindowsHelper(serviceName, target, replacement)
	if err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			_ = os.Remove(helper)
		}
	}()
	delayMS := 0
	if !restart {
		// The authenticated HTTP handler needs time to flush its success response
		// before the helper asks SCM to stop this process.
		delayMS = 2000
	}
	arguments := []string{
		"update-helper",
		"--target", target,
		"--replacement", helper,
		"--service", serviceName,
		"--parent-pid", strconv.Itoa(os.Getpid()),
		"--delay-ms", strconv.Itoa(delayMS),
		"--version", latest,
	}
	logPath := target + ".update.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("update: open helper log %s: %w", logPath, err)
	}
	command := exec.Command(helper, arguments...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.Stdin = nil
	command.Env = windowsHelperEnvironment(os.Environ())
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := startDetachedProcess(command); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("update: start Windows updater helper: %w", err)
	}
	started = true
	_ = logFile.Close()
	helperPID := 0
	if command.Process != nil {
		helperPID = command.Process.Pid
	}
	logger.Info(
		"verified update handed to Windows helper",
		"target", target,
		"version", latest,
		"helper_pid", helperPID,
		"status", target+".update-result.json",
	)
	fmt.Printf("vocat %s was verified; Windows service update is continuing in helper process %d.\n", latest, helperPID)
	fmt.Printf("Update result: %s\n", target+".update-result.json")
	return nil
}

// stageVerifiedWindowsHelper checks that the named service is installed and
// points at target, then copies the verified file beside target. It does not
// start, stop, or reconfigure the service.
func stageVerifiedWindowsHelper(serviceName, target, replacement string) (string, error) {
	if err := validateWindowsServiceForUpdate(serviceName, target); err != nil {
		return "", err
	}
	helper, err := stageSiblingHelper(replacement, filepath.Dir(target))
	if err != nil {
		return "", err
	}
	if !sameWindowsDirectory(target, helper) {
		_ = os.Remove(helper)
		return "", errors.New("update: Windows replacement must be staged beside the target executable")
	}
	return helper, nil
}

func validateWindowsServiceForUpdate(serviceName, target string) error {
	binary, err := lookupWindowsServiceExecutable(serviceName)
	if err != nil {
		if errors.Is(err, errWindowsServiceNotInstalled) {
			return fmt.Errorf("update: automatic Windows service update is not supported because service %q is not installed (Electron portable mode has no SCM service)", serviceName)
		}
		return fmt.Errorf("update: read Windows service %s executable: %w", serviceName, err)
	}
	if !windowsServiceExecutableMatchesTarget(binary, target) {
		return fmt.Errorf("update: Windows service %s executable does not match the update target", serviceName)
	}
	return nil
}

// queryWindowsServiceExecutable opens SCM with connect and query-config rights
// only. It must not start, stop, create, delete, or reconfigure a service.
func queryWindowsServiceExecutable(name string) (string, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", fmt.Errorf("update: connect to Windows Service Control Manager: %w", err)
	}
	defer windows.CloseServiceHandle(manager)

	serviceName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", fmt.Errorf("update: encode Windows service name: %w", err)
	}
	handle, err := windows.OpenService(manager, serviceName, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return "", errWindowsServiceNotInstalled
		}
		return "", fmt.Errorf("update: open Windows service %s: %w", name, err)
	}
	service := &mgr.Service{Name: name, Handle: handle}
	defer service.Close()

	config, err := service.Config()
	if err != nil {
		return "", fmt.Errorf("update: read Windows service %s config: %w", name, err)
	}
	if strings.TrimSpace(config.BinaryPathName) == "" {
		return "", fmt.Errorf("update: Windows service %s has no executable path", name)
	}
	return config.BinaryPathName, nil
}

func windowsServiceExecutableMatchesTarget(serviceBinary, target string) bool {
	servicePath := normalizeWindowsExecutable(windowsServiceImagePath(serviceBinary))
	targetPath := normalizeWindowsExecutable(target)
	if servicePath == "" || targetPath == "" || servicePath == "." || targetPath == "." {
		return false
	}
	return strings.EqualFold(servicePath, targetPath)
}

func windowsServiceImagePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, `"`) {
		rest := value[1:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return strings.TrimSpace(rest)
		}
		return strings.TrimSpace(rest[:end])
	}
	lower := strings.ToLower(value)
	if index := strings.Index(lower, ".exe"); index >= 0 {
		return strings.TrimSpace(value[:index+len(".exe")])
	}
	if fields := strings.Fields(value); len(fields) > 0 {
		return fields[0]
	}
	return value
}

func normalizeWindowsExecutable(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, `\\?\`)
	path = strings.TrimPrefix(path, `\??\`)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func sameWindowsDirectory(left, right string) bool {
	return strings.EqualFold(filepath.Clean(filepath.Dir(left)), filepath.Clean(filepath.Dir(right)))
}

func stageSiblingHelper(source, targetDir string) (string, error) {
	name, err := randomWindowsHelperFileName()
	if err != nil {
		return "", err
	}
	helper := filepath.Join(targetDir, name)
	if err := copyExclusiveSync(source, helper); err != nil {
		return "", fmt.Errorf("update: stage Windows helper beside target: %w", err)
	}
	return helper, nil
}

func randomWindowsHelperFileName() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("update: create Windows helper name: %w", err)
	}
	return fmt.Sprintf(".vocat-update-%x.exe", buf[:]), nil
}

// copyExclusiveSync copies source to a new file. copyFileSync uses O_EXCL and
// Sync. An existing destination is left untouched. A partial new file is removed.
func copyExclusiveSync(source, destination string) error {
	return copyFileSync(source, destination)
}

func removeIncompleteHelper(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("update: refusing to remove helper directory %s", path)
	}
	return os.Remove(path)
}

func windowsHelperEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		upperName := strings.ToUpper(strings.TrimSpace(name))
		if strings.Contains(upperName, "TOKEN") ||
			strings.Contains(upperName, "PASSWORD") ||
			strings.Contains(upperName, "SECRET") ||
			upperName == "APN_PASSWORD" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func RunHelper(args []string) (returnErr error) {
	options, err := parseHelperFlags(args)
	if err != nil {
		return err
	}
	resultPath := options.target + ".update-result.json"
	defer scheduleDeleteOnReboot(options.replacement)
	defer func() {
		result := helperResult{Success: returnErr == nil, Version: options.version, Time: time.Now().UTC().Format(time.RFC3339)}
		if returnErr != nil {
			result.Error = returnErr.Error()
		}
		data, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr == nil {
			data = append(data, '\n')
			_ = os.WriteFile(resultPath, data, 0o600)
		}
	}()
	if options.delay > 0 {
		time.Sleep(options.delay)
	}
	fmt.Printf("%s stopping Windows service %s\n", time.Now().UTC().Format(time.RFC3339), options.service)
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("update: connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(options.service)
	if err != nil {
		return fmt.Errorf("update: open Windows service %s: %w", options.service, err)
	}
	defer service.Close()
	if err := stopService(service, 45*time.Second); err != nil {
		return err
	}
	if err := waitForProcessExit(options.parentPID, 45*time.Second); err != nil {
		return err
	}
	if err := replaceAndStart(service, options.target, options.replacement); err != nil {
		return err
	}
	fmt.Printf("%s Windows service %s updated to %s and is running\n", time.Now().UTC().Format(time.RFC3339), options.service, options.version)
	return nil
}

func parseHelperFlags(args []string) (helperOptions, error) {
	options := helperOptions{}
	for index := 0; index < len(args); index++ {
		if index+1 >= len(args) {
			return options, fmt.Errorf("update: helper flag %s requires a value", args[index])
		}
		value := args[index+1]
		index++
		switch args[index-1] {
		case "--target":
			options.target = value
		case "--replacement":
			options.replacement = value
		case "--service":
			options.service = value
		case "--version":
			options.version = value
		case "--parent-pid":
			parsed, err := strconv.ParseUint(value, 10, 32)
			if err != nil || parsed == 0 {
				return options, errors.New("update: invalid helper parent PID")
			}
			options.parentPID = uint32(parsed)
		case "--delay-ms":
			parsed, err := strconv.ParseUint(value, 10, 32)
			if err != nil || parsed > 30_000 {
				return options, errors.New("update: invalid helper delay")
			}
			options.delay = time.Duration(parsed) * time.Millisecond
		default:
			return options, fmt.Errorf("update: unknown helper flag %q", args[index-1])
		}
	}
	if options.target == "" || options.replacement == "" || options.parentPID == 0 {
		return options, errors.New("update: helper requires target, replacement, and parent PID")
	}
	if !filepath.IsAbs(options.target) || !filepath.IsAbs(options.replacement) {
		return options, errors.New("update: helper paths must be absolute")
	}
	if !strings.EqualFold(filepath.Dir(options.target), filepath.Dir(options.replacement)) {
		return options, errors.New("update: helper replacement must be staged beside the target")
	}
	if !validWindowsServiceName.MatchString(options.service) {
		return options, fmt.Errorf("update: invalid Windows service name %q", options.service)
	}
	return options, nil
}

func windowsServiceName() string {
	if value := strings.TrimSpace(os.Getenv("VOCAT_WINDOWS_SERVICE_NAME")); value != "" {
		return value
	}
	return defaultWindowsServiceName
}

func waitForProcessExit(pid uint32, timeout time.Duration) error {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("update: open parent process %d: %w", pid, err)
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, uint32(timeout/time.Millisecond))
	if err != nil {
		return fmt.Errorf("update: wait for parent process %d: %w", pid, err)
	}
	if status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("update: parent process %d did not exit within %s", pid, timeout)
	}
	return nil
}

func stopService(service *mgr.Service, timeout time.Duration) error {
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("update: query Windows service: %w", err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err := service.Control(svc.Stop); err != nil {
			return fmt.Errorf("update: stop Windows service: %w", err)
		}
	}
	return waitForServiceState(service, svc.Stopped, timeout)
}

func startService(service *mgr.Service, timeout time.Duration) error {
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("update: query Windows service before start: %w", err)
	}
	if status.State == svc.Running {
		return nil
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("update: start Windows service: %w", err)
	}
	return waitForServiceState(service, svc.Running, timeout)
}

func waitForServiceState(service *mgr.Service, state svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("update: query Windows service state: %w", err)
		}
		if status.State == state {
			return nil
		}
		if state == svc.Running && status.State == svc.Stopped {
			return fmt.Errorf("update: Windows service stopped during startup (exit code %d)", status.Win32ExitCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("update: Windows service did not reach state %d within %s", state, timeout)
}

func replaceAndStart(service *mgr.Service, target string, replacement string) error {
	backup := target + ".previous"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("update: remove stale backup %s: %w", backup, err)
	}
	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("update: back up current executable: %w", err)
	}
	rollback := func(cause error) error {
		var rollbackErrors []error
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove failed replacement: %w", err))
		}
		if err := os.Rename(backup, target); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous executable: %w", err))
		}
		if err := startService(service, 45*time.Second); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restart rolled-back service: %w", err))
		}
		if len(rollbackErrors) > 0 {
			return errors.Join(append([]error{cause}, rollbackErrors...)...)
		}
		return fmt.Errorf("%w; previous executable restored and service restarted", cause)
	}
	// The helper is executing from replacement, so Windows will not allow that
	// file to be renamed. Copy its already-verified bytes into the now-vacant
	// target and schedule the temporary helper image for deletion on reboot.
	if err := copyFileSync(replacement, target); err != nil {
		return rollback(fmt.Errorf("update: copy new binary into place: %w", err))
	}
	if err := startService(service, 45*time.Second); err != nil {
		_ = stopService(service, 15*time.Second)
		return rollback(fmt.Errorf("update: new executable failed to start: %w", err))
	}
	return nil
}

func copyFileSync(source, destination string) (returnErr error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if err := output.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
		// Only an output successfully created by this call may be reclaimed.
		// Opening the source can fail while an unrelated destination exists.
		if returnErr != nil {
			if err := removeIncompleteHelper(destination); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func scheduleDeleteOnReboot(path string) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	const moveFileDelayUntilReboot = 0x4
	_ = windows.MoveFileEx(pathUTF16, nil, moveFileDelayUntilReboot)
}

func restartWindowsService(logger *slog.Logger) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("restart Windows service: connect to SCM: %w", err)
	}
	defer manager.Disconnect()
	name := windowsServiceName()
	service, err := manager.OpenService(name)
	if err != nil {
		return fmt.Errorf("restart Windows service: open %s: %w", name, err)
	}
	defer service.Close()
	if err := stopService(service, 45*time.Second); err != nil {
		return err
	}
	if err := startService(service, 45*time.Second); err != nil {
		return err
	}
	if logger != nil {
		logger.Info("Windows service restarted", "service", name)
	}
	return nil
}
