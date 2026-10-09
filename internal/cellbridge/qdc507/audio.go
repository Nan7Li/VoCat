// Adapted from CellBridge gateway/internal/voice/qdc507/audio.go
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: no pkill of the host ADB daemon, no HOME=/root, the process
// environment is preserved, and a foreign mavo route is never killed. A
// model string or USB VID/PID alone cannot authorise the runtime. Missing
// vendor files stay unavailable and name the missing check. Prepare returns a
// Runtime handle. Close stops only processes this process claimed, and only
// after the original USB path, ADB transport, pid, starttime and argv still
// match. The final signal rechecks that identity inside one shell script.
// Each Runtime freezes a random owner token into the remote pid record so a
// failed start can be reclaimed without adopting a process that already
// existed. Payloads are snapshotted on the host and hashed on the module
// before install or insmod.

package qdc507

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	expectedKernel    = "3.18.44"
	expectedCardName  = "mdm9607-tomtom-i2s-snd-card"
	remoteRuntimeDir  = "/run/maccellular-call"
	remoteBridge      = remoteRuntimeDir + "/mavo-pcm-bridge.armv7"
	remoteAPRModule   = remoteRuntimeDir + "/qdc507_aprv3.ko"
	remoteVoiceModule = remoteRuntimeDir + "/qdc507_voice.ko"
)

type runtimeArtifact struct {
	name string
	mode string
	hash string
}

// Hashes are the CellBridge fixed allowlist. The files themselves are vendor
// binaries and are not shipped in this repository.
var trustedRuntimeArtifacts = []runtimeArtifact{
	{name: "mavo-pcm-bridge.armv7", mode: "755", hash: "88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc"},
	{name: "qdc507_aprv3.ko", mode: "644", hash: "3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a"},
	{name: "qdc507_voice.ko", mode: "644", hash: "ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c"},
}

// Config is the explicit opt-in for one already matched DJI module.
// Model, VendorID and ProductID are accepted so callers can pass them, and
// they are intentionally ignored as authorisation evidence.
type Config struct {
	OptIn      bool
	Bootstrap  bool
	DeviceType string
	USBPath    string
	Model      string
	VendorID   string
	ProductID  string
	RuntimeDir string
	ADBPath    string
	ADBSocket  string
	Runner     Runner
}

// Status is the hardware readiness result shown to the operator.
// Runtime is set only when Ready is true. It is omitted from JSON so a
// status payload cannot smuggle a live handle.
type Status struct {
	Ready     bool
	Reason    string
	Transport string
	Runtime   *Runtime `json:"-"`
}

// Runner executes one already-built argv. Production uses os.Environ().
type Runner interface {
	Run(ctx context.Context, argv []string, env []string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, argv []string, env []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var (
	usbPathPattern = regexp.MustCompile(`^[0-9]+-[0-9.]+$`)
	socketPattern  = regexp.MustCompile(`^(tcp:[A-Za-z0-9._:-]+|unix:[A-Za-z0-9._/-]+)$`)
)

// ADBInvocation builds an adb command that keeps the current environment.
// It never sets HOME and never inserts a pkill.
func ADBInvocation(path, socket string, args []string) (argv []string, env []string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, errors.New("adb path is required")
	}
	if strings.ContainsAny(path, "\r\n\t;|&$`<>") {
		return nil, nil, errors.New("adb path contains unsupported characters")
	}
	for _, arg := range args {
		if strings.Contains(arg, "\x00") {
			return nil, nil, errors.New("adb argument contains a NUL")
		}
	}
	env = append([]string{}, os.Environ()...)
	argv = []string{path}
	socket = strings.TrimSpace(socket)
	if socket != "" {
		if !socketPattern.MatchString(socket) {
			return nil, nil, fmt.Errorf("adb server socket %q is invalid", socket)
		}
		env = append(env, "ADB_SERVER_SOCKET="+socket)
		argv = append(argv, "-L", socket)
	}
	argv = append(argv, args...)
	return argv, env, nil
}

// Prepare checks the opt-in, module identity, runtime hashes and, when
// bootstrap is enabled, the owned module route. It does not report ready
// when any check fails. A successful result carries a Runtime the caller
// closes when the bridge stops. Failure rolls newly created resources back
// on an independent bounded context.
func Prepare(ctx context.Context, cfg Config) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{Reason: err.Error()}, err
	}
	if !cfg.OptIn {
		return Status{Reason: "QDC507 语音运行时未启用"}, nil
	}
	if cfg.DeviceType != "" && cfg.DeviceType != "dji_4g" {
		reason := fmt.Sprintf("QDC507 运行时只适用于大疆模块，当前设备类型是 %s", cfg.DeviceType)
		return Status{Reason: reason}, errors.New(reason)
	}
	if strings.TrimSpace(cfg.USBPath) == "" {
		reason := "QDC507 需要与 Halo 设备一致的 USB 路径；型号或 USB VID/PID 不足以加载运行时"
		return Status{Reason: reason}, errors.New(reason)
	}
	if !usbPathPattern.MatchString(strings.TrimSpace(cfg.USBPath)) {
		reason := fmt.Sprintf("QDC507 USB 路径 %q 无效", cfg.USBPath)
		return Status{Reason: reason}, errors.New(reason)
	}
	snap, err := snapshotVerifiedRuntime(cfg.RuntimeDir)
	if err != nil {
		return Status{Reason: err.Error()}, err
	}
	runner := cfg.Runner
	if runner == nil {
		runner = execRunner{}
	}
	frozen := cfg
	frozen.USBPath = strings.TrimSpace(cfg.USBPath)
	frozen.Runner = runner
	token, tokenErr := newOwnerToken()
	rt := &Runtime{cfg: frozen, runner: runner, snapshot: snap, token: token}
	kept := false
	defer func() {
		if !kept {
			rollbackIndependent(rt)
		}
	}()
	if tokenErr != nil {
		return Status{Reason: tokenErr.Error()}, tokenErr
	}
	devices, err := runADB(ctx, runner, frozen, "devices", "-l")
	if err != nil {
		reason := fmt.Errorf("列出 ADB 设备失败: %w (%s)", err, devices)
		return Status{Reason: reason.Error()}, reason
	}
	transport, err := SelectTransportID(devices, frozen.USBPath)
	if err != nil {
		return Status{Reason: err.Error()}, err
	}
	rt.transport = transport
	uid, err := runADB(ctx, runner, frozen, "-t", transport, "shell", "id -u")
	if err != nil || strings.TrimSpace(uid) != "0" {
		reason := fmt.Errorf("QDC507 ADB root 不可用: %v (%s)", err, strings.TrimSpace(uid))
		return Status{Reason: reason.Error(), Transport: transport}, reason
	}
	kernel, err := runADB(ctx, runner, frozen, "-t", transport, "shell", "uname -r")
	if err != nil || strings.TrimSpace(kernel) != expectedKernel {
		reason := fmt.Errorf("QDC507 内核不匹配: 需要 %s，实际 %q", expectedKernel, strings.TrimSpace(kernel))
		return Status{Reason: reason.Error(), Transport: transport}, reason
	}
	// Every route uses hw:0,4. Verify card0 before either adopting an
	// existing route or pushing/loading target-specific kernel modules.
	if err := verifyCardZero(ctx, runner, frozen, transport); err != nil {
		return Status{Reason: err.Error(), Transport: transport}, err
	}
	state, err := classifyRoute(ctx, runner, frozen, transport)
	if err != nil {
		return Status{Reason: err.Error(), Transport: transport}, err
	}
	switch state.Kind {
	case routeForeign:
		return Status{Reason: errForeignRoute.Error(), Transport: transport}, errForeignRoute
	case routeOwned:
		if err := verifyOwnedRemote(ctx, runner, frozen, transport, state.Proc); err != nil {
			return Status{Reason: err.Error(), Transport: transport}, err
		}
		ready, rerr := runADB(ctx, runner, frozen, "-t", transport, "shell", ownedReadyScript)
		if rerr != nil || strings.TrimSpace(ready) != "ready" {
			err = fmt.Errorf("QDC507 已有路由未通过远端内容与 RUNNING 校验: %v (%s)", rerr, strings.TrimSpace(ready))
			return Status{Reason: err.Error(), Transport: transport}, err
		}
		rt.track(state.Proc)
		trackLiveCalibration(ctx, rt)
		rt.dropSnapshot()
		kept = true
		return Status{Ready: true, Transport: transport, Runtime: rt}, nil
	case routeNone:
		if !frozen.Bootstrap {
			reason := errors.New("QDC507 bootstrap 未启用，模块语音路由未由本程序建立")
			return Status{Reason: reason.Error(), Transport: transport}, reason
		}
		if err := bootstrap(ctx, rt); err != nil {
			return Status{Reason: err.Error(), Transport: transport}, err
		}
		rt.dropSnapshot()
		kept = true
		return Status{Ready: true, Transport: transport, Runtime: rt}, nil
	default:
		err = errors.New("QDC507 路由状态无法识别")
		return Status{Reason: err.Error(), Transport: transport}, err
	}
}

func runADB(ctx context.Context, runner Runner, cfg Config, args ...string) (string, error) {
	argv, env, err := ADBInvocation(cfg.ADBPath, cfg.ADBSocket, args)
	if err != nil {
		return "", err
	}
	return runner.Run(ctx, argv, env)
}

func bootstrap(ctx context.Context, rt *Runtime) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := refuseIfRouteBusy(ctx, rt); err != nil {
		return err
	}
	present, err := installedFilesMatch(ctx, rt)
	if err != nil {
		return err
	}
	if !present {
		if err := stageAndInstall(ctx, rt); err != nil {
			return err
		}
	}
	if err := refuseIfRouteBusy(ctx, rt); err != nil {
		return err
	}
	if err := requireFinalHashes(ctx, rt); err != nil {
		return err
	}
	if _, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdInsmod("qdc507_aprv3", remoteAPRModule)); err != nil {
		return fmt.Errorf("加载 QDC507 APR 模块失败: %w", err)
	}
	if _, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdInsmod("qdc507_voice", remoteVoiceModule)); err != nil {
		return fmt.Errorf("加载 QDC507 voice 模块失败: %w", err)
	}
	var ready string
	for attempt := 0; attempt < 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err = runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", pcmReadyScript)
		if err == nil && strings.TrimSpace(ready) == "ready" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err != nil || strings.TrimSpace(ready) != "ready" {
		return fmt.Errorf("QDC507 VoLTE PCM 设备不可用: %v (%s)", err, strings.TrimSpace(ready))
	}
	card, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", "cat /proc/asound/cards")
	if err != nil || !strings.Contains(card, expectedCardName) {
		return fmt.Errorf("QDC507 ALSA 声卡 %q 不可用: %v (%s)", expectedCardName, err, strings.TrimSpace(card))
	}
	if err := runTracked(ctx, rt, kindCalibration, calibrationScript); err != nil {
		if errors.Is(err, errForeignRoute) {
			return err
		}
		return fmt.Errorf("QDC507 VoLTE 校准不可用: %w", err)
	}
	endpoints, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", endpointScript)
	if err != nil || strings.TrimSpace(endpoints) != "ready" {
		return fmt.Errorf("QDC507 语音端点不可用: %v (%s)", err, strings.TrimSpace(endpoints))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runTracked(ctx, rt, kindRoute, routeStartScript); err != nil {
		if errors.Is(err, errForeignRoute) {
			return err
		}
		return fmt.Errorf("启动本程序拥有的 QDC507 语音路由失败: %w", err)
	}
	var route string
	for attempt := 0; attempt < 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		route, err = runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", ownedReadyScript)
		if err == nil && strings.TrimSpace(route) == "ready" {
			_ = discardStage(ctx, rt)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fmt.Errorf("QDC507 语音路由未进入本程序拥有的 RUNNING 状态: %v (%s)", err, strings.TrimSpace(route))
}

func classifyRoute(ctx context.Context, runner Runner, cfg Config, transport string) (routeState, error) {
	out, err := runADB(ctx, runner, cfg, "-t", transport, "shell", "cat /run/halo-cellbridge-route.pid")
	if err == nil {
		if pid, start, ok := parsePidLine(out); ok {
			cur, cerr := runADB(ctx, runner, cfg, "-t", transport, "shell", cmdStart(pid))
			cmd, xerr := runADB(ctx, runner, cfg, "-t", transport, "shell", cmdCmdline(pid))
			if cerr == nil && xerr == nil && strings.TrimSpace(cur) == start && argvEqual(argvFromCmdline(cmd), routeArgv) {
				return routeState{Kind: routeOwned, Proc: processRecord{
					Kind:      kindRoute,
					PID:       pid,
					StartTime: start,
					Argv:      append([]string(nil), routeArgv...),
					Created:   false,
				}}, nil
			}
		}
	}
	ps, err := runADB(ctx, runner, cfg, "-t", transport, "shell", foreignScan)
	if err != nil {
		return routeState{}, fmt.Errorf("检查 QDC507 路由进程失败: %w", err)
	}
	if strings.Contains(ps, "mavo-pcm-bridge") {
		return routeState{Kind: routeForeign}, nil
	}
	return routeState{Kind: routeNone}, nil
}

func refuseIfRouteBusy(ctx context.Context, rt *Runtime) error {
	state, err := classifyRoute(ctx, rt.runner, rt.cfg, rt.transport)
	if err != nil {
		return err
	}
	switch state.Kind {
	case routeForeign:
		return errForeignRoute
	case routeOwned:
		return errors.New("模块上已有本程序的语音路由正在使用远端文件，已拒绝覆盖")
	default:
		return nil
	}
}

func verifyOwnedRemote(ctx context.Context, runner Runner, cfg Config, transport string, proc processRecord) error {
	if !numericID.MatchString(proc.PID) {
		return errOwnedContent
	}
	exePath := "/proc/" + proc.PID + "/exe"
	out, err := runADB(ctx, runner, cfg, "-t", transport, "shell", cmdSha256(exePath))
	if err != nil {
		return errOwnedContent
	}
	sums, err := parseSha256Sum(out)
	if err != nil || sums[exePath] != artifactHash("mavo-pcm-bridge.armv7") {
		return errOwnedContent
	}
	out, err = runADB(ctx, runner, cfg, "-t", transport, "shell", cmdSha256(remoteArtifactPaths()...))
	if err != nil {
		return errOwnedContent
	}
	sums, err = parseSha256Sum(out)
	if err != nil || !remotePathsMatch(sums) {
		return errOwnedContent
	}
	return nil
}

func installedFilesMatch(ctx context.Context, rt *Runtime) (bool, error) {
	out, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", remoteFilesScript)
	if err != nil {
		return false, fmt.Errorf("读取 QDC507 远端运行时摘要失败: %w", err)
	}
	state, sums, err := interpretRemoteListing(out)
	if err != nil {
		return false, fmt.Errorf("读取 QDC507 远端运行时摘要失败: %w", err)
	}
	switch state {
	case "missing":
		return false, nil
	case "symlink":
		return false, errRemoteSymlink
	case "present":
		if !remotePathsMatch(sums) {
			return true, errRemoteHash
		}
		return true, nil
	default:
		return false, errors.New("QDC507 远端运行时摘要无法识别")
	}
}

func requireFinalHashes(ctx context.Context, rt *Runtime) error {
	out, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdSha256(remoteArtifactPaths()...))
	if err != nil {
		return errRemoteHash
	}
	sums, err := parseSha256Sum(out)
	if err != nil || !remotePathsMatch(sums) {
		return errRemoteHash
	}
	return nil
}

func stageAndInstall(ctx context.Context, rt *Runtime) error {
	stage, err := newStageDir()
	if err != nil {
		return err
	}
	if _, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdMkdir(stage)); err != nil {
		return fmt.Errorf("创建 QDC507 远端临时目录失败: %w", err)
	}
	rt.setStage(stage)
	var paths []string
	for _, art := range trustedRuntimeArtifacts {
		local := filepath.Join(rt.snapshot, art.name)
		remote := stage + "/" + art.name
		if _, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "push", local, remote); err != nil {
			return fmt.Errorf("推送 QDC507 运行时 %s 失败: %w", art.name, err)
		}
		paths = append(paths, remote)
	}
	out, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdSha256(paths...))
	if err != nil {
		return errUploadHash
	}
	sums, err := parseSha256Sum(out)
	if err != nil || !stagePathsMatch(sums, stage) {
		return errUploadHash
	}
	if err := refuseIfRouteBusy(ctx, rt); err != nil {
		return err
	}
	for _, art := range trustedRuntimeArtifacts {
		src := stage + "/" + art.name
		dst := remoteRuntimeDir + "/" + art.name
		installed, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdInstall(src, dst, art.mode))
		if strings.TrimSpace(installed) == "symlink" {
			return errRemoteSymlink
		}
		if err != nil || strings.TrimSpace(installed) != "installed" {
			return fmt.Errorf("安装 QDC507 运行时 %s 失败: %v (%s)", art.name, err, strings.TrimSpace(installed))
		}
	}
	return nil
}

func discardStage(ctx context.Context, rt *Runtime) error {
	rt.mu.Lock()
	stage := rt.stage
	runner := rt.runner
	cfg := rt.cfg
	transport := rt.transport
	rt.mu.Unlock()
	if stage == "" {
		return nil
	}
	if err := removeRemoteStage(ctx, runner, cfg, transport, stage); err != nil {
		return err
	}
	rt.mu.Lock()
	if rt.stage == stage {
		rt.stage = ""
	}
	rt.mu.Unlock()
	return nil
}

func runTracked(ctx context.Context, rt *Runtime, kind, script string) error {
	script, err := bindOwnerToken(script, rt.token)
	if err != nil {
		return err
	}
	out, runErr := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", script)
	if refusedForeign(out) {
		return errForeignRoute
	}
	rec, parseErr := parseTrack(out, kind)
	switch {
	case parseErr == nil && rec.Created:
		// The process was started before a later wait or a dropped ADB
		// status. Track it even when the command itself failed.
		rt.track(rec)
	case parseErr == nil && runErr == nil:
		rt.track(rec)
	default:
		// Owned lines are not tracked on failure. An empty or truncated
		// stdout can still name a process this runtime just created.
		if recov, ok := recoverOwnStart(rt, kind); ok {
			rt.track(recov)
		}
	}
	if runErr != nil {
		return runErr
	}
	return parseErr
}

func trackLiveCalibration(ctx context.Context, rt *Runtime) {
	out, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", "cat /run/halo-cellbridge-alsaucm.pid")
	if err != nil {
		return
	}
	pid, start, ok := parsePidLine(out)
	if !ok {
		return
	}
	cur, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdStart(pid))
	if err != nil || strings.TrimSpace(cur) != start {
		return
	}
	cmd, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdCmdline(pid))
	if err != nil || !argvEqual(argvFromCmdline(cmd), calibrationArgv) {
		return
	}
	rt.track(processRecord{
		Kind:      kindCalibration,
		PID:       pid,
		StartTime: start,
		Argv:      append([]string(nil), calibrationArgv...),
		Created:   false,
	})
}

const (
	pcmReadyScript = "test -e /dev/snd/pcmC0D4p && test -e /dev/snd/pcmC0D4c && test -e /dev/snd/pcmC0D5p && test -e /dev/snd/pcmC0D6c && echo ready"
	endpointScript = "test -c /dev/ttyGS0 && test -p /run/voc_svr && echo ready"
)

// remoteFilesScript reports whether the installed allowlist files are
// missing, a symlink, or ready to be hashed. It does not install anything.
const remoteFilesScript = `# halo-qdc507-remote-files
b=/run/maccellular-call/mavo-pcm-bridge.armv7
a=/run/maccellular-call/qdc507_aprv3.ko
v=/run/maccellular-call/qdc507_voice.ko
if test -L "$b" || test -L "$a" || test -L "$v"; then echo symlink; exit 0; fi
if test ! -f "$b" || test ! -f "$a" || test ! -f "$v"; then echo missing; exit 0; fi
sha256sum "$b" "$a" "$v"
`

// routeStartScript starts a route only when this program's pid file owns it.
// A foreign mavo process is reported and left running. A created process
// writes this runtime's owner token before and immediately after fork, and
// prints its track line before any later output. An owned process does not
// rewrite that record.
const routeStartScript = `# halo-qdc507-route-start
token='@TOKEN@'
pidfile=/run/halo-cellbridge-route.pid
owner=/run/halo-cellbridge-route.owner
bridge=/run/maccellular-call/mavo-pcm-bridge.armv7
log=/run/halo-cellbridge-route.log
origin=
pid=
expected_start=
if test -s "$pidfile"; then
  read pid expected_start < "$pidfile" || true
  current_start=$(cut -d ' ' -f 22 /proc/$pid/stat 2>/dev/null)
  argv0=$(tr '\000' '\n' < /proc/$pid/cmdline 2>/dev/null | sed -n '1p')
  arg1=$(tr '\000' '\n' < /proc/$pid/cmdline 2>/dev/null | sed -n '2p')
  arg2=$(tr '\000' '\n' < /proc/$pid/cmdline 2>/dev/null | sed -n '3p')
  if test "$current_start" = "$expected_start" && test "$argv0" = "$bridge" && test "$arg1" = --voice-route-session && test "$arg2" = --verbose; then
    origin=owned
  else
    pid=
    expected_start=
  fi
fi
if test -z "$origin"; then
  if ps | grep -q '[m]avo-pcm-bridge'; then
    echo foreign
    exit 75
  fi
  origin=created
  printf 'pending %s\n' "$token" > "$owner"
  rm -f "$pidfile" "$log"
  nohup "$bridge" --voice-route-session --verbose </dev/null >> "$log" 2>&1 &
  pid=$!
  expected_start=$(cut -d ' ' -f 22 /proc/$pid/stat 2>/dev/null)
  printf '%s %s\n' "$pid" "$expected_start" > "$pidfile"
  printf '%s %s %s\n' "$pid" "$expected_start" "$token" > "$owner"
  printf 'halo-track route created %s %s %s --voice-route-session --verbose\n' "$pid" "$expected_start" "$bridge"
  echo started
fi
test -n "$pid" && test -n "$expected_start" || exit 74
if test "$origin" = owned; then
  printf 'halo-track route owned %s %s %s --voice-route-session --verbose\n' "$pid" "$expected_start" "$bridge"
fi
`

// ownedReadyScript accepts only the pid file written by routeStartScript.
const ownedReadyScript = `# halo-qdc507-owned-ready
pidfile=/run/halo-cellbridge-route.pid
bridge=/run/maccellular-call/mavo-pcm-bridge.armv7
log=/run/halo-cellbridge-route.log
test -s "$pidfile" || exit 70
read pid expected_start < "$pidfile" || exit 70
test "$(cut -d ' ' -f 22 /proc/$pid/stat 2>/dev/null)" = "$expected_start" || exit 71
test "$(tr '\000' '\n' < /proc/$pid/cmdline 2>/dev/null | sed -n '1p')" = "$bridge" || exit 71
test "$(tr '\000' '\n' < /proc/$pid/cmdline 2>/dev/null | sed -n '2p')" = --voice-route-session || exit 71
test "$(tr '\000' '\n' < /proc/$pid/cmdline 2>/dev/null | sed -n '3p')" = --verbose || exit 71
grep -q 'VoLTE route session active on hw:0,4' "$log" || exit 72
test "$(cat /sys/class/android_usb/f_audio/audio_enable 2>/dev/null)" = 1 || exit 73
grep -q '^state: RUNNING' /proc/asound/card0/pcm4p/sub0/status || exit 74
grep -q '^state: RUNNING' /proc/asound/card0/pcm4c/sub0/status || exit 74
echo ready
`

// calibrationScript is the module-side ACDB sequence. It only starts
// /usr/bin/alsaucm_test when the pid file proves this program does not
// already own one. The created pid, starttime and this runtime's owner
// token are recorded, and the track line is printed, before the FIFO and
// ACDB waits. An owned process is not rewritten and is not claimed when
// those waits fail. It does not signal unrelated processes.
const calibrationScript = `# halo-qdc507-calibration
token='@TOKEN@'
owner=/run/halo-cellbridge-alsaucm.owner
pidfile=/run/halo-cellbridge-alsaucm.pid
origin=
pid=
expected_start=
if test -s "$pidfile"; then
  read pid expected_start < "$pidfile" || true
  current_start=$(cut -d ' ' -f 22 /proc/$pid/stat 2>/dev/null)
  argv0=$(tr '\000' '\n' < /proc/$pid/cmdline 2>/dev/null | sed -n '1p')
  if test "$current_start" = "$expected_start" && test "$argv0" = /usr/bin/alsaucm_test; then
    origin=owned
  else
    pid=
    expected_start=
  fi
fi
if test -z "$origin"; then
  if ps | grep -q '[a]lsaucm_test'; then
    echo foreign-calibration
    exit 75
  fi
  origin=created
  printf 'pending %s\n' "$token" > "$owner"
  rm -f /run/alsaucm_test "$pidfile" /run/halo-cellbridge-alsaucm.log
  nohup /usr/bin/alsaucm_test </dev/null >> /run/halo-cellbridge-alsaucm.log 2>&1 &
  pid=$!
  expected_start=$(cut -d ' ' -f 22 /proc/$pid/stat 2>/dev/null)
  printf '%s %s\n' "$pid" "$expected_start" > "$pidfile"
  printf '%s %s %s\n' "$pid" "$expected_start" "$token" > "$owner"
  printf 'halo-track calibration created %s %s /usr/bin/alsaucm_test\n' "$pid" "$expected_start"
  n=0
  while test "$n" -lt 50 && test ! -p /run/alsaucm_test; do
    kill -0 "$pid" 2>/dev/null || exit 72
    sleep 0.1
    n=$((n+1))
  done
  test -p /run/alsaucm_test || exit 73
fi
if ! grep -q 'ACDB -> Sent VocProc Cal!' /run/halo-cellbridge-alsaucm.log 2>/dev/null; then
  printf 'open snd_soc_msm_9x07_Tomtom_I2S\n' > /run/alsaucm_test
  printf 'set _verb VoLTE\n' > /run/alsaucm_test
  printf 'set _enadev Auxpcm Rx\n' > /run/alsaucm_test
  printf 'set _enadev Auxpcm Tx\n' > /run/alsaucm_test
  n=0
  while test "$n" -lt 100; do
    grep -q 'ACDB -> Sent VocProc Cal!' /run/halo-cellbridge-alsaucm.log 2>/dev/null && break
    sleep 0.1
    n=$((n+1))
  done
fi
grep -q 'ACDB -> Sent VocProc Cal!' /run/halo-cellbridge-alsaucm.log || exit 74
test -n "$pid" && test -n "$expected_start" || exit 74
if test "$origin" = owned; then
  printf 'halo-track calibration owned %s %s /usr/bin/alsaucm_test\n' "$pid" "$expected_start"
fi
`

// SelectTransportID accepts one ready ADB device whose usb: path matches.
func SelectTransportID(devices, usbPath string) (string, error) {
	usbPath = strings.TrimSpace(usbPath)
	if usbPath == "" {
		return "", errors.New("QDC507 USB 路径为空")
	}
	var result string
	for _, line := range strings.Split(devices, "\n") {
		fields := strings.Fields(line)
		statusIndex := -1
		for index, field := range fields {
			if field == "device" {
				statusIndex = index
				break
			}
		}
		if statusIndex < 0 {
			continue
		}
		matched := false
		transport := ""
		for _, field := range fields[statusIndex+1:] {
			switch {
			case strings.HasPrefix(field, "usb:") && strings.TrimPrefix(field, "usb:") == usbPath:
				matched = true
			case strings.HasPrefix(field, "transport_id:"):
				transport = strings.TrimPrefix(field, "transport_id:")
			}
		}
		if matched && transport != "" {
			if result != "" {
				return "", fmt.Errorf("多个 ADB 设备匹配 USB 路径 %s", usbPath)
			}
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(transport) {
				return "", fmt.Errorf("ADB transport id %q 无效", transport)
			}
			result = transport
		}
	}
	if result == "" {
		return "", fmt.Errorf("没有就绪的 ADB 设备匹配 USB 路径 %s", usbPath)
	}
	return result, nil
}
