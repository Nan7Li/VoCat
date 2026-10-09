package qdc507

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	processOwner = "halo-cellbridge"
	cleanupBound = 5 * time.Second

	routeNone    = "none"
	routeOwned   = "owned"
	routeForeign = "foreign"

	kindRoute       = "route"
	kindCalibration = "calibration"

	// foreignScan lists mavo route processes without matching its own grep.
	foreignScan = `ps | grep '[m]avo-pcm-bridge' || true`
)

var (
	errForeignRoute      = errors.New("模块上已有不属于本程序的 mavo 语音进程，已拒绝接管")
	errRemoteHash        = errors.New("QDC507 远端运行时文件与允许列表不一致")
	errOwnedContent      = errors.New("QDC507 正在使用的远端桥与允许列表不一致")
	errUploadHash        = errors.New("QDC507 上传后的远端 SHA-256 与允许列表不一致")
	errRemoteSymlink     = errors.New("QDC507 远端运行时路径是符号链接")
	errRuntimePerm       = errors.New("QDC507 运行时路径禁止组或其他用户写入")
	errRuntimeNotRegular = errors.New("QDC507 运行时文件不是普通文件")
)

var (
	routeArgv         = []string{remoteBridge, "--voice-route-session", "--verbose"}
	calibrationArgv   = []string{"/usr/bin/alsaucm_test"}
	numericID         = regexp.MustCompile(`^[0-9]+$`)
	stagePattern      = regexp.MustCompile(`^/run/maccellular-call/stage-[0-9]+-[0-9a-f]{16}$`)
	safeArgPattern    = regexp.MustCompile(`^[A-Za-z0-9_./:=+-]+$`)
	ownerTokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

const (
	ownerTokenPlaceholder = "@TOKEN@"
	calibrationOwnerFile  = "/run/halo-cellbridge-alsaucm.owner"
	routeOwnerFile        = "/run/halo-cellbridge-route.owner"
)

// Process is a calibration or route process recorded for one Prepare.
// Created is true only when this Prepare started it. StopOnClose is true
// when this Runtime holds the in-process claim and Close may signal it
// after the USB, transport, pid, starttime and argv checks pass.
type Process struct {
	Kind        string
	PID         string
	StartTime   string
	Argv        []string
	Owner       string
	Created     bool
	StopOnClose bool
}

type processRecord struct {
	Kind      string
	PID       string
	StartTime string
	Argv      []string
	Owner     string
	Created   bool
	stop      bool
}

func (p processRecord) export() Process {
	return Process{
		Kind:        p.Kind,
		PID:         p.PID,
		StartTime:   p.StartTime,
		Argv:        append([]string(nil), p.Argv...),
		Owner:       p.Owner,
		Created:     p.Created,
		StopOnClose: p.stop,
	}
}

type routeState struct {
	Kind string
	Proc processRecord
}

// Runtime is the handle returned by a successful Prepare.
// Close is idempotent. It freezes the Config and ADB transport captured
// at Prepare and never signals a process by name.
type Runtime struct {
	mu        sync.Mutex
	closed    bool
	cfg       Config
	runner    Runner
	transport string
	token     string
	snapshot  string
	stage     string
	created   []processRecord
	adopted   []processRecord
	toStop    []processRecord
}

type claimKey struct {
	USB       string
	Transport string
	Kind      string
	PID       string
	Start     string
}

var (
	claimMu     sync.Mutex
	claimHolder = map[claimKey]*Runtime{}
)

func resetClaims() {
	claimMu.Lock()
	claimHolder = map[claimKey]*Runtime{}
	claimMu.Unlock()
}

// Processes reports created and adopted processes. The caller can see which
// ones this handle will stop.
func (r *Runtime) Processes() []Process {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Process, 0, len(r.created)+len(r.adopted))
	for _, p := range r.created {
		out = append(out, p.export())
	}
	for _, p := range r.adopted {
		out = append(out, p.export())
	}
	return out
}

// Close stops processes this runtime claimed and removes its private host
// snapshot and remote stage directory. A second call does nothing.
// Cleanup uses its own bounded context and does not inherit caller
// cancellation: a finished HTTP request must not skip reclaim. A changed
// USB mapping, a different ADB transport, PID reuse, or a live argv that no
// longer matches is not signalled. The final signal is one shell script
// that rechecks starttime and the full argv before kill.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	// ctx is intentionally unused. Production cancels the request context
	// before calling Close; reclaim must still run.
	_ = ctx
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	procs := append([]processRecord(nil), r.toStop...)
	r.toStop = nil
	snap := r.snapshot
	r.snapshot = ""
	stage := r.stage
	r.stage = ""
	transport := r.transport
	cfg := r.cfg
	runner := r.runner
	r.mu.Unlock()

	cleanup, cancel := context.WithTimeout(context.Background(), cleanupBound)
	defer cancel()

	var errs []error
	if snap != "" {
		if err := os.RemoveAll(snap); err != nil {
			errs = append(errs, err)
		}
	}
	if transport == "" || runner == nil {
		for _, p := range procs {
			r.release(p)
		}
		return errors.Join(errs...)
	}
	if !deviceMatches(cleanup, runner, cfg, transport) {
		for _, p := range procs {
			r.release(p)
		}
		return errors.Join(errs...)
	}
	if stage != "" {
		if err := removeRemoteStage(cleanup, runner, cfg, transport, stage); err != nil {
			errs = append(errs, err)
		}
	}
	for i := len(procs) - 1; i >= 0; i-- {
		if !deviceMatches(cleanup, runner, cfg, transport) {
			for _, p := range procs[:i+1] {
				r.release(p)
			}
			break
		}
		if err := stopProcess(cleanup, runner, cfg, transport, procs[i]); err != nil {
			errs = append(errs, err)
		}
		r.release(procs[i])
	}
	return errors.Join(errs...)
}

func (r *Runtime) track(p processRecord) {
	p.Owner = processOwner
	p.Argv = append([]string(nil), p.Argv...)
	key := claimKey{USB: r.cfg.USBPath, Transport: r.transport, Kind: p.Kind, PID: p.PID, Start: p.StartTime}
	claimMu.Lock()
	holder := claimHolder[key]
	if p.Created || holder == nil {
		claimHolder[key] = r
		p.stop = true
	}
	claimMu.Unlock()
	r.mu.Lock()
	if p.Created {
		r.created = append(r.created, p)
	} else {
		r.adopted = append(r.adopted, p)
	}
	if p.stop {
		r.toStop = append(r.toStop, p)
	}
	r.mu.Unlock()
}

func (r *Runtime) release(p processRecord) {
	key := claimKey{USB: r.cfg.USBPath, Transport: r.transport, Kind: p.Kind, PID: p.PID, Start: p.StartTime}
	claimMu.Lock()
	if claimHolder[key] == r {
		delete(claimHolder, key)
	}
	claimMu.Unlock()
}

func (r *Runtime) dropSnapshot() {
	r.mu.Lock()
	snap := r.snapshot
	r.snapshot = ""
	r.mu.Unlock()
	if snap != "" {
		_ = os.RemoveAll(snap)
	}
}

func (r *Runtime) setStage(stage string) {
	r.mu.Lock()
	r.stage = stage
	r.mu.Unlock()
}

func rollbackIndependent(rt *Runtime) {
	if rt == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBound)
	defer cancel()
	_ = rt.Close(ctx)
}

func deviceMatches(ctx context.Context, runner Runner, cfg Config, transport string) bool {
	if strings.TrimSpace(transport) == "" || strings.TrimSpace(cfg.USBPath) == "" || runner == nil {
		return false
	}
	out, err := runADB(ctx, runner, cfg, "devices", "-l")
	if err != nil {
		return false
	}
	id, err := SelectTransportID(out, cfg.USBPath)
	if err != nil {
		return false
	}
	return id == transport
}

func stopProcess(ctx context.Context, runner Runner, cfg Config, transport string, p processRecord) error {
	if !numericID.MatchString(p.PID) || !numericID.MatchString(p.StartTime) {
		return nil
	}
	for _, arg := range p.Argv {
		if !safeArgPattern.MatchString(arg) {
			return nil
		}
	}
	out, err := runADB(ctx, runner, cfg, "-t", transport, "shell", cmdStart(p.PID))
	if err != nil || strings.TrimSpace(out) != p.StartTime {
		return nil
	}
	cmd, err := runADB(ctx, runner, cfg, "-t", transport, "shell", cmdCmdline(p.PID))
	if err != nil || !argvEqual(argvFromCmdline(cmd), p.Argv) {
		return nil
	}
	// Preflight above can be stale by the time a later ADB command runs.
	// The signal itself stays inside one shell script that reads starttime
	// and every argv element again and exits without signalling on mismatch.
	if _, err := runADB(ctx, runner, cfg, "-t", transport, "shell", cmdStop(p)); err != nil {
		return fmt.Errorf("停止 QDC507 %s 进程 %s 失败: %w", p.Kind, p.PID, err)
	}
	return nil
}

func cmdStop(p processRecord) string {
	if !numericID.MatchString(p.PID) || !numericID.MatchString(p.StartTime) {
		return "# halo-qdc507-stop\nexit 0\n"
	}
	for _, arg := range p.Argv {
		if !safeArgPattern.MatchString(arg) {
			return "# halo-qdc507-stop\nexit 0\n"
		}
	}
	var b strings.Builder
	b.WriteString("# halo-qdc507-stop\n")
	fmt.Fprintf(&b, "pid=%s\n", p.PID)
	fmt.Fprintf(&b, "expected_start=%s\n", p.StartTime)
	b.WriteString("current_start=$(cut -d ' ' -f 22 /proc/\"$pid\"/stat 2>/dev/null) || exit 0\n")
	b.WriteString("test \"$current_start\" = \"$expected_start\" || exit 0\n")
	for i, arg := range p.Argv {
		fmt.Fprintf(&b, "arg%d=$(tr '\\000' '\\n' < /proc/\"$pid\"/cmdline 2>/dev/null | sed -n '%dp')\n", i, i+1)
		fmt.Fprintf(&b, "test \"$arg%d\" = '%s' || exit 0\n", i, arg)
	}
	fmt.Fprintf(&b, "extra=$(tr '\\000' '\\n' < /proc/\"$pid\"/cmdline 2>/dev/null | sed -n '%dp')\n", len(p.Argv)+1)
	b.WriteString("test -z \"$extra\" || exit 0\n")
	b.WriteString("kill -TERM \"$pid\"\n")
	return b.String()
}

func newOwnerToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成 QDC507 归属标记失败: %w", err)
	}
	token := hex.EncodeToString(buf[:])
	if !ownerTokenPattern.MatchString(token) {
		return "", errors.New("生成 QDC507 归属标记失败")
	}
	return token, nil
}

func bindOwnerToken(script, token string) (string, error) {
	if !ownerTokenPattern.MatchString(token) {
		return "", errors.New("QDC507 归属标记无效")
	}
	if !strings.Contains(script, ownerTokenPlaceholder) {
		return "", errors.New("QDC507 启动脚本缺少归属标记")
	}
	bound := strings.ReplaceAll(script, ownerTokenPlaceholder, token)
	if strings.Contains(bound, ownerTokenPlaceholder) || !strings.Contains(bound, "token='"+token+"'") {
		return "", errors.New("QDC507 归属标记未写入启动脚本")
	}
	return bound, nil
}

func ownerFile(kind string) string {
	switch kind {
	case kindCalibration:
		return calibrationOwnerFile
	case kindRoute:
		return routeOwnerFile
	default:
		return ""
	}
}

// recoverOwnStart reads the owner file written by this runtime's start
// script. A pending line, another runtime's token, or a live pid whose
// starttime or argv no longer matches is ignored. Caller cancellation is
// not inherited: the read happens after the start command has already failed.
func recoverOwnStart(rt *Runtime, kind string) (processRecord, bool) {
	if rt == nil || rt.runner == nil || strings.TrimSpace(rt.transport) == "" || !ownerTokenPattern.MatchString(rt.token) {
		return processRecord{}, false
	}
	file := ownerFile(kind)
	if file == "" {
		return processRecord{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBound)
	defer cancel()
	out, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", "cat "+file)
	if err != nil {
		return processRecord{}, false
	}
	pid, start, token, ok := parseOwnerRecord(out)
	if !ok || token != rt.token {
		return processRecord{}, false
	}
	want := routeArgv
	if kind == kindCalibration {
		want = calibrationArgv
	}
	cur, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdStart(pid))
	if err != nil || strings.TrimSpace(cur) != start {
		return processRecord{}, false
	}
	cmdline, err := runADB(ctx, rt.runner, rt.cfg, "-t", rt.transport, "shell", cmdCmdline(pid))
	if err != nil || !argvEqual(argvFromCmdline(cmdline), want) {
		return processRecord{}, false
	}
	return processRecord{
		Kind:      kind,
		PID:       pid,
		StartTime: start,
		Argv:      append([]string(nil), want...),
		Created:   true,
	}, true
}

func snapshotVerifiedRuntime(directory string) (string, error) {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == "." || directory == "" || !filepath.IsAbs(directory) {
		return "", errors.New("QDC507 运行时目录必须是绝对路径")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("QDC507 运行时目录不可用: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("QDC507 运行时目录必须是真实目录")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%w: %s", errRuntimePerm, directory)
	}
	snap, err := os.MkdirTemp("", "halo-qdc507-snap-")
	if err != nil {
		return "", fmt.Errorf("创建 QDC507 私有快照失败: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(snap)
		}
	}()
	if err := os.Chmod(snap, 0o700); err != nil {
		return "", fmt.Errorf("收紧 QDC507 私有快照权限失败: %w", err)
	}
	for _, art := range trustedRuntimeArtifacts {
		data, err := readPrivateFile(filepath.Join(directory, art.name))
		if err != nil {
			if errors.Is(err, errRuntimePerm) || errors.Is(err, errRuntimeNotRegular) {
				return "", fmt.Errorf("%w: %s", err, art.name)
			}
			return "", fmt.Errorf("QDC507 运行时文件 %s 不可用: %w", art.name, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != art.hash {
			return "", fmt.Errorf("QDC507 运行时文件 %s 的 SHA-256 与允许列表不一致", art.name)
		}
		if err := writePrivate(filepath.Join(snap, art.name), data); err != nil {
			return "", fmt.Errorf("写入 QDC507 私有快照 %s 失败: %w", art.name, err)
		}
	}
	cleanup = false
	return snap, nil
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errRuntimeNotRegular
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, errRuntimePerm
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o022 != 0 {
		return nil, errRuntimePerm
	}
	return io.ReadAll(file)
}

func writePrivate(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := file.Write(data)
	cerr := file.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func newStageDir() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	stage := fmt.Sprintf("%s/stage-%d-%s", remoteRuntimeDir, time.Now().UnixNano(), hex.EncodeToString(buf[:]))
	if !stagePattern.MatchString(stage) {
		return "", errors.New("QDC507 远端临时目录无效")
	}
	return stage, nil
}

func removeRemoteStage(ctx context.Context, runner Runner, cfg Config, transport, stage string) error {
	if !stagePattern.MatchString(stage) {
		return errors.New("拒绝删除未识别的 QDC507 远端临时目录")
	}
	_, err := runADB(ctx, runner, cfg, "-t", transport, "shell", "rm -rf -- "+stage)
	if err != nil {
		return fmt.Errorf("清理 QDC507 远端临时目录失败: %w", err)
	}
	return nil
}

func cmdStart(pid string) string {
	return "cut -d ' ' -f 22 /proc/" + pid + "/stat"
}

func cmdCmdline(pid string) string {
	return "tr '\\000' '\\n' < /proc/" + pid + "/cmdline"
}

func cmdSha256(paths ...string) string {
	return "sha256sum " + strings.Join(paths, " ")
}

func cmdMkdir(stage string) string {
	return "mkdir -p " + stage + " && chmod 700 " + stage
}

func cmdInstall(src, dst, mode string) string {
	return fmt.Sprintf("if test -L %q; then echo symlink; exit 76; fi; cp -f %q %q && chmod %s %q && echo installed", dst, src, dst, mode, dst)
}

func cmdInsmod(sysName, path string) string {
	return "test -d /sys/module/" + sysName + " || insmod " + path
}

func remoteArtifactPaths() []string {
	paths := make([]string, 0, len(trustedRuntimeArtifacts))
	for _, art := range trustedRuntimeArtifacts {
		paths = append(paths, remoteRuntimeDir+"/"+art.name)
	}
	return paths
}

func artifactHash(name string) string {
	for _, art := range trustedRuntimeArtifacts {
		if art.name == name {
			return art.hash
		}
	}
	return ""
}

func parsePidLine(out string) (string, string, bool) {
	fields := strings.Fields(out)
	if len(fields) < 2 || !numericID.MatchString(fields[0]) || !numericID.MatchString(fields[1]) {
		return "", "", false
	}
	return fields[0], fields[1], true
}

// parseOwnerRecord accepts only "pid start token". A pending line has no pid
// and is rejected so a start that died before the process existed cannot
// adopt whatever now occupies that name.
func parseOwnerRecord(out string) (string, string, string, bool) {
	fields := strings.Fields(out)
	if len(fields) != 3 || !numericID.MatchString(fields[0]) || !numericID.MatchString(fields[1]) || !ownerTokenPattern.MatchString(fields[2]) {
		return "", "", "", false
	}
	return fields[0], fields[1], fields[2], true
}

func argvFromCmdline(out string) []string {
	var argv []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		argv = append(argv, line)
	}
	return argv
}

func argvEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseSha256Sum(out string) (map[string]string, error) {
	sums := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !isHex64(fields[0]) {
			return nil, fmt.Errorf("无法解析 sha256sum 输出 %q", line)
		}
		sums[fields[1]] = strings.ToLower(fields[0])
	}
	if len(sums) == 0 {
		return nil, errors.New("sha256sum 输出为空")
	}
	return sums, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func interpretRemoteListing(out string) (string, map[string]string, error) {
	switch strings.TrimSpace(out) {
	case "missing":
		return "missing", nil, nil
	case "symlink":
		return "symlink", nil, nil
	}
	sums, err := parseSha256Sum(out)
	if err != nil {
		return "", nil, err
	}
	return "present", sums, nil
}

func remotePathsMatch(sums map[string]string) bool {
	for _, art := range trustedRuntimeArtifacts {
		if sums[remoteRuntimeDir+"/"+art.name] != art.hash {
			return false
		}
	}
	return true
}

func stagePathsMatch(sums map[string]string, stage string) bool {
	for _, art := range trustedRuntimeArtifacts {
		if sums[stage+"/"+art.name] != art.hash {
			return false
		}
	}
	return true
}

func refusedForeign(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		switch strings.TrimSpace(line) {
		case "foreign", "foreign-calibration":
			return true
		}
	}
	return false
}

func parseTrack(output, kind string) (processRecord, error) {
	var line string
	for _, raw := range strings.Split(output, "\n") {
		raw = strings.TrimSpace(raw)
		if strings.HasPrefix(raw, "halo-track ") {
			line = raw
		}
	}
	if line == "" {
		return processRecord{}, fmt.Errorf("QDC507 未返回 %s 进程跟踪记录 (%s)", kind, strings.TrimSpace(output))
	}
	fields := strings.Fields(line)
	if len(fields) < 6 || fields[0] != "halo-track" || fields[1] != kind {
		return processRecord{}, fmt.Errorf("QDC507 %s 进程跟踪记录无效", kind)
	}
	origin := fields[2]
	if origin != "created" && origin != "owned" {
		return processRecord{}, fmt.Errorf("QDC507 %s 进程来源无效", kind)
	}
	if !numericID.MatchString(fields[3]) || !numericID.MatchString(fields[4]) {
		return processRecord{}, fmt.Errorf("QDC507 %s 进程标识无效", kind)
	}
	argv := append([]string(nil), fields[5:]...)
	want := routeArgv
	if kind == kindCalibration {
		want = calibrationArgv
	}
	if !argvEqual(argv, want) {
		return processRecord{}, fmt.Errorf("QDC507 %s 进程参数与拥有者记录不一致", kind)
	}
	return processRecord{
		Kind:      kind,
		PID:       fields[3],
		StartTime: fields[4],
		Argv:      argv,
		Created:   origin == "created",
	}, nil
}
