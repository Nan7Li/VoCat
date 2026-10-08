package qdc507

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type fakeFile struct {
	hash    string
	symlink bool
}

type fakeProc struct {
	pid     string
	start   string
	argv    []string
	exe     string
	exeHash string
	alive   bool
}

// fakeModule is a stateful ADB stand-in. It records pushes from the bytes it
// reads, answers identity queries from its process table, and applies
// kill -TERM only from the stop script after that script's own starttime
// and argv checks. A bare kill command is rejected.
type fakeModule struct {
	usb             string
	transport       string
	kernel          string
	uid             string
	cards           string
	files           map[string]fakeFile
	ownerFiles      map[string]string
	procs           []fakeProc
	nextPID         int
	nextStart       int
	routePID        string
	calPID          string
	pcm             bool
	failEndpoints   bool
	failReady       bool
	lieStage        bool
	routeLogOK      bool
	audioEnable     string
	pcmRunning      bool
	calMode         string
	routeMode       string
	cancel          context.CancelFunc
	before          func([]string)
	beforeFinalStop func(string)
	calls           [][]string
	envs            [][]string
	ctxErr          []error
	pushed          []string
	pushedBytes     [][]byte
	pushDest        []string
	insmod          []string
	killed          []string
	skipped         []string
	removed         []string
}

func newFakeModule() *fakeModule {
	return &fakeModule{
		usb:        "1-4.2",
		transport:  "7",
		kernel:     expectedKernel,
		uid:        "0",
		cards:      "0 [" + expectedCardName + "]",
		files:      map[string]fakeFile{},
		ownerFiles: map[string]string{},
		pcm:        true,
		nextPID:    400,
		nextStart:  8000,
	}
}

func newFixture(t *testing.T) (Config, *fakeModule, string) {
	t.Helper()
	resetClaims()
	t.Cleanup(resetClaims)
	previous := append([]runtimeArtifact(nil), trustedRuntimeArtifacts...)
	restoreHashes(t, previous)
	dir := writeRuntime(t, nil)
	t.Setenv("HOME", "/home/halo")
	fake := newFakeModule()
	cfg := Config{
		OptIn: true, Bootstrap: true, DeviceType: "dji_4g", USBPath: "1-4.2",
		RuntimeDir: dir, ADBPath: "adb", ADBSocket: "tcp:127.0.0.1:5038", Runner: fake,
	}
	return cfg, fake, dir
}

func (f *fakeModule) Run(ctx context.Context, argv []string, env []string) (string, error) {
	if ctx == nil {
		return "", errors.New("nil context")
	}
	f.calls = append(f.calls, append([]string(nil), argv...))
	f.envs = append(f.envs, append([]string(nil), env...))
	f.ctxErr = append(f.ctxErr, ctx.Err())
	if f.before != nil {
		f.before(append([]string(nil), argv...))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	tail := adbTail(argv)
	if len(tail) == 0 {
		return "", errors.New("empty adb command")
	}
	if tail[0] == "devices" {
		return fmt.Sprintf("serial device usb:%s transport_id:%s\n", f.usb, f.transport), nil
	}
	if tail[0] != "-t" || len(tail) < 3 {
		return "", fmt.Errorf("unexpected adb %q", tail)
	}
	rest := tail[2:]
	switch rest[0] {
	case "push":
		if len(rest) != 3 {
			return "", errors.New("bad push")
		}
		data, err := os.ReadFile(rest[1])
		if err != nil {
			return "", err
		}
		sum := sha256Hex(data)
		f.files[rest[2]] = fakeFile{hash: sum}
		f.pushed = append(f.pushed, rest[1])
		f.pushedBytes = append(f.pushedBytes, append([]byte(nil), data...))
		f.pushDest = append(f.pushDest, rest[2])
		return "", nil
	case "shell":
		return f.shell(strings.Join(rest[1:], " "))
	default:
		return "", fmt.Errorf("unexpected adb verb %q", rest[0])
	}
}

func adbTail(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	args := argv[1:]
	if len(args) >= 2 && args[0] == "-L" {
		args = args[2:]
	}
	return args
}

func (f *fakeModule) shell(cmd string) (string, error) {
	if strings.Contains(cmd, "# halo-qdc507-stop") {
		return f.evalStop(cmd)
	}
	switch cmd {
	case "id -u":
		return f.uid, nil
	case "uname -r":
		return f.kernel, nil
	case "cat /proc/asound/cards":
		return f.cards, nil
	case "cat /run/halo-cellbridge-route.pid":
		if f.routePID == "" {
			return "No such file", errors.New("No such file")
		}
		return f.routePID, nil
	case "cat /run/halo-cellbridge-alsaucm.pid":
		if f.calPID == "" {
			return "No such file", errors.New("No such file")
		}
		return f.calPID, nil
	case "cat " + calibrationOwnerFile:
		return f.readOwner(calibrationOwnerFile)
	case "cat " + routeOwnerFile:
		return f.readOwner(routeOwnerFile)
	case foreignScan:
		return f.foreignPS(), nil
	case pcmReadyScript:
		if f.pcm {
			return "ready", nil
		}
		return "missing", errors.New("pcm missing")
	case endpointScript:
		if f.failEndpoints {
			return "missing", errors.New("endpoints missing")
		}
		return "ready", nil
	}
	switch {
	case strings.Contains(cmd, "# halo-qdc507-remote-files"):
		return f.remoteFiles()
	case strings.Contains(cmd, "# halo-qdc507-calibration"):
		return f.startCalibration(cmd)
	case strings.Contains(cmd, "# halo-qdc507-route-start"):
		return f.startRoute(cmd)
	case strings.Contains(cmd, "# halo-qdc507-owned-ready"):
		return f.ownedReady()
	case strings.HasPrefix(cmd, "sha256sum "):
		return f.sha256(strings.Fields(cmd)[1:])
	case strings.HasPrefix(cmd, "mkdir -p "):
		return f.mkdir(cmd)
	case strings.HasPrefix(cmd, "rm -rf -- "):
		return f.removeStage(strings.TrimPrefix(cmd, "rm -rf -- "))
	case strings.HasPrefix(cmd, "kill -TERM "):
		return "", errors.New("bare kill across adb commands")
	case strings.Contains(cmd, "insmod "):
		f.insmod = append(f.insmod, cmd)
		return "", nil
	case installRe.MatchString(cmd):
		return f.install(cmd)
	case strings.Contains(cmd, "cut -d ") && strings.Contains(cmd, "/stat"):
		pid, ok := pidFromProc(cmd)
		if !ok {
			return "", errors.New("bad stat")
		}
		proc := f.proc(pid)
		if proc == nil {
			return "No such file", errors.New("missing stat")
		}
		return proc.start, nil
	case strings.Contains(cmd, "/cmdline"):
		pid, ok := pidFromProc(cmd)
		if !ok {
			return "", errors.New("bad cmdline")
		}
		proc := f.proc(pid)
		if proc == nil {
			return "No such file", errors.New("missing cmdline")
		}
		return strings.Join(proc.argv, "\n"), nil
	case strings.HasPrefix(cmd, "readlink "):
		pid, ok := pidFromProc(cmd)
		if !ok {
			return "", errors.New("bad readlink")
		}
		proc := f.proc(pid)
		if proc == nil {
			return "No such file", errors.New("missing exe")
		}
		return proc.exe, nil
	default:
		return "", fmt.Errorf("unexpected shell: %s", cmd)
	}
}

var installRe = regexp.MustCompile(`^if test -L "([^"]+)"; then echo symlink; exit 76; fi; cp -f "([^"]+)" "([^"]+)" && chmod ([0-7]+) "([^"]+)" && echo installed$`)

func (f *fakeModule) remoteFiles() (string, error) {
	paths := remoteArtifactPaths()
	for _, path := range paths {
		if file, ok := f.files[path]; ok && file.symlink {
			return "symlink", nil
		}
	}
	for _, path := range paths {
		if _, ok := f.files[path]; !ok {
			return "missing", nil
		}
	}
	return f.sha256(paths)
}

func (f *fakeModule) sha256(paths []string) (string, error) {
	var b strings.Builder
	for _, path := range paths {
		if f.lieStage && strings.Contains(path, "/stage-") {
			fmt.Fprintf(&b, "%s  %s\n", strings.Repeat("cd", 32), path)
			continue
		}
		if strings.HasPrefix(path, "/proc/") && strings.HasSuffix(path, "/exe") {
			pid, ok := pidFromProc(path)
			if !ok {
				return "", errors.New("bad exe hash")
			}
			proc := f.proc(pid)
			if proc == nil {
				return "No such file", errors.New("missing exe")
			}
			fmt.Fprintf(&b, "%s  %s\n", proc.exeHash, path)
			continue
		}
		file, ok := f.files[path]
		if !ok || file.symlink {
			return "No such file", errors.New("missing")
		}
		fmt.Fprintf(&b, "%s  %s\n", file.hash, path)
	}
	return strings.TrimSpace(b.String()), nil
}

func (f *fakeModule) mkdir(cmd string) (string, error) {
	parts := strings.Split(cmd, " && ")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "mkdir -p ") || !strings.HasPrefix(parts[1], "chmod 700 ") {
		return "", fmt.Errorf("bad mkdir %s", cmd)
	}
	stage := strings.TrimPrefix(parts[0], "mkdir -p ")
	if stage != strings.TrimPrefix(parts[1], "chmod 700 ") || !stagePattern.MatchString(stage) {
		return "", fmt.Errorf("bad stage %s", stage)
	}
	return "", nil
}

func (f *fakeModule) install(cmd string) (string, error) {
	groups := installRe.FindStringSubmatch(cmd)
	if len(groups) != 6 {
		return "", fmt.Errorf("bad install %s", cmd)
	}
	dst, src := groups[1], groups[2]
	if groups[3] != dst || groups[5] != dst {
		return "", errors.New("install destination mismatch")
	}
	file, ok := f.files[src]
	if !ok {
		return "missing", errors.New("missing source")
	}
	if existing, exists := f.files[dst]; exists && existing.symlink {
		return "symlink", errors.New("symlink")
	}
	f.files[dst] = fakeFile{hash: file.hash}
	return "installed", nil
}

func (f *fakeModule) removeStage(stage string) (string, error) {
	if !stagePattern.MatchString(stage) {
		return "", errors.New("refusing rm")
	}
	f.removed = append(f.removed, stage)
	for path := range f.files {
		if strings.HasPrefix(path, stage+"/") {
			delete(f.files, path)
		}
	}
	return "", nil
}

func (f *fakeModule) kill(pid string) (string, error) {
	if !numericID.MatchString(pid) {
		return "", errors.New("bad pid")
	}
	f.killed = append(f.killed, pid)
	for i := range f.procs {
		if f.procs[i].pid == pid {
			f.procs[i].alive = false
		}
	}
	if strings.HasPrefix(f.routePID, pid+" ") {
		f.routePID = ""
	}
	if strings.HasPrefix(f.calPID, pid+" ") {
		f.calPID = ""
	}
	return "killed", nil
}

func (f *fakeModule) readOwner(path string) (string, error) {
	body, ok := f.ownerFiles[path]
	if !ok {
		return "No such file", errors.New("No such file")
	}
	return body, nil
}

func (f *fakeModule) startCalibration(cmd string) (string, error) {
	token := tokenFromScript(cmd)
	if proc := f.ownedCal(); proc != nil {
		switch f.calMode {
		case "", "partial", "empty", "truncated", "empty-cancel", "pending", "stale":
			if f.calMode == "" {
				return trackLine(kindCalibration, "owned", proc), nil
			}
			return "", fmt.Errorf("owned calibration cannot use create mode %s", f.calMode)
		case "owned-error":
			return "", errors.New("calibration wait failed")
		case "owned-track":
			return trackLine(kindCalibration, "owned", proc), errors.New("calibration wait failed")
		default:
			return "", fmt.Errorf("unknown calibration mode %s", f.calMode)
		}
	}
	for i := range f.procs {
		if f.procs[i].alive && len(f.procs[i].argv) > 0 && f.procs[i].argv[0] == calibrationArgv[0] {
			return "foreign-calibration", errors.New("exit 75")
		}
	}
	if token != "" {
		f.ownerFiles[calibrationOwnerFile] = "pending " + token
	}
	f.spawn(calibrationArgv, calibrationArgv[0], "")
	idx := len(f.procs) - 1
	f.calPID = f.procs[idx].pid + " " + f.procs[idx].start
	if token != "" {
		f.ownerFiles[calibrationOwnerFile] = f.procs[idx].pid + " " + f.procs[idx].start + " " + token
	}
	line := trackLine(kindCalibration, "created", &f.procs[idx])
	switch f.calMode {
	case "":
		return line, nil
	case "partial":
		delete(f.ownerFiles, calibrationOwnerFile)
		return line + "\nfifo timeout", errors.New("fifo timeout")
	case "empty":
		return "", errors.New("adb connection lost")
	case "truncated":
		return "halo-track calibration created " + f.procs[idx].pid, errors.New("short write")
	case "empty-cancel":
		if f.cancel != nil {
			f.cancel()
		}
		return "", context.Canceled
	case "pending":
		if token != "" {
			f.ownerFiles[calibrationOwnerFile] = "pending " + token
		}
		return "", errors.New("lost before pid record")
	case "stale":
		f.procs[idx].start = "1"
		return "", errors.New("adb connection lost")
	default:
		return "", fmt.Errorf("unknown calibration mode %s", f.calMode)
	}
}

func (f *fakeModule) startRoute(cmd string) (string, error) {
	token := tokenFromScript(cmd)
	if proc := f.ownedRoute(); proc != nil {
		if f.routeMode != "" {
			return "", fmt.Errorf("owned route cannot use create mode %s", f.routeMode)
		}
		return trackLine(kindRoute, "owned", proc), nil
	}
	if strings.TrimSpace(f.foreignPS()) != "" {
		return "foreign", errors.New("exit 75")
	}
	if token != "" {
		f.ownerFiles[routeOwnerFile] = "pending " + token
	}
	f.spawn(routeArgv, remoteBridge, artifactHash("mavo-pcm-bridge.armv7"))
	idx := len(f.procs) - 1
	f.routePID = f.procs[idx].pid + " " + f.procs[idx].start
	f.routeLogOK = true
	f.audioEnable = "1"
	f.pcmRunning = true
	if token != "" {
		f.ownerFiles[routeOwnerFile] = f.procs[idx].pid + " " + f.procs[idx].start + " " + token
	}
	line := "started\n" + trackLine(kindRoute, "created", &f.procs[idx])
	switch f.routeMode {
	case "":
		if f.cancel != nil {
			f.cancel()
		}
		return line, nil
	case "empty":
		return "", errors.New("adb error")
	case "cancel":
		if f.cancel != nil {
			f.cancel()
		}
		return "", context.Canceled
	default:
		return "", fmt.Errorf("unknown route mode %s", f.routeMode)
	}
}

func (f *fakeModule) ownedReady() (string, error) {
	if f.failReady || f.ownedRoute() == nil || !f.routeLogOK || f.audioEnable != "1" || !f.pcmRunning {
		return "not-ready", errors.New("exit 74")
	}
	return "ready", nil
}

func (f *fakeModule) spawn(argv []string, exe, exeHash string) fakeProc {
	f.nextPID++
	f.nextStart++
	proc := fakeProc{
		pid:     strconv.Itoa(f.nextPID),
		start:   strconv.Itoa(f.nextStart),
		argv:    append([]string(nil), argv...),
		exe:     exe,
		exeHash: exeHash,
		alive:   true,
	}
	f.procs = append(f.procs, proc)
	return proc
}

func (f *fakeModule) proc(pid string) *fakeProc {
	for i := range f.procs {
		if f.procs[i].pid == pid && f.procs[i].alive {
			return &f.procs[i]
		}
	}
	return nil
}

func (f *fakeModule) ownedRoute() *fakeProc {
	pid, start, ok := parsePidLine(f.routePID)
	if !ok {
		return nil
	}
	proc := f.proc(pid)
	if proc == nil || proc.start != start || !argvEqual(proc.argv, routeArgv) {
		return nil
	}
	return proc
}

func (f *fakeModule) ownedCal() *fakeProc {
	pid, start, ok := parsePidLine(f.calPID)
	if !ok {
		return nil
	}
	proc := f.proc(pid)
	if proc == nil || proc.start != start || !argvEqual(proc.argv, calibrationArgv) {
		return nil
	}
	return proc
}

func (f *fakeModule) foreignPS() string {
	var b strings.Builder
	for i := range f.procs {
		if !f.procs[i].alive {
			continue
		}
		line := strings.Join(f.procs[i].argv, " ")
		if strings.Contains(line, "mavo-pcm-bridge") {
			fmt.Fprintf(&b, "%s %s\n", f.procs[i].pid, line)
		}
	}
	return b.String()
}

func (f *fakeModule) aliveArgv0(argv0 string) int {
	count := 0
	for i := range f.procs {
		if f.procs[i].alive && len(f.procs[i].argv) > 0 && f.procs[i].argv[0] == argv0 {
			count++
		}
	}
	return count
}

func trackLine(kind, origin string, proc *fakeProc) string {
	return "halo-track " + kind + " " + origin + " " + proc.pid + " " + proc.start + " " + strings.Join(proc.argv, " ")
}

func pidFromProc(cmd string) (string, bool) {
	const marker = "/proc/"
	index := strings.Index(cmd, marker)
	if index < 0 {
		return "", false
	}
	rest := cmd[index+len(marker):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", false
	}
	pid := rest[:slash]
	if !numericID.MatchString(pid) {
		return "", false
	}
	return pid, true
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func checkSafeCalls(t *testing.T, calls [][]string, envs [][]string) {
	t.Helper()
	if len(calls) != len(envs) {
		t.Fatalf("calls %d envs %d", len(calls), len(envs))
	}
	for _, env := range envs {
		home := ""
		socket := ""
		for _, item := range env {
			if strings.HasPrefix(item, "HOME=") {
				if home != "" {
					t.Fatalf("HOME overwritten: %s then %s", home, item)
				}
				home = item
			}
			if strings.HasPrefix(item, "ADB_SERVER_SOCKET=") {
				socket = item
			}
		}
		if home != "HOME=/home/halo" {
			t.Fatalf("HOME = %q", home)
		}
		if socket != "ADB_SERVER_SOCKET=tcp:127.0.0.1:5038" {
			t.Fatalf("socket = %q", socket)
		}
	}
	for _, argv := range calls {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "pkill") || strings.Contains(joined, "killall") || strings.Contains(joined, "HOME=/root") {
			t.Fatalf("unsafe call %s", joined)
		}
		if !strings.Contains(joined, "-L tcp:127.0.0.1:5038") && !strings.Contains(joined, "devices -l") {
			// devices still carries -L when a socket is configured.
		}
		if !strings.Contains(joined, "-L") || !strings.Contains(joined, "tcp:127.0.0.1:5038") {
			t.Fatalf("adb socket dropped: %s", joined)
		}
		shell := shellCommand(argv)
		if isBareKill(shell) {
			t.Fatalf("bare kill across adb commands: %s", shell)
		}
		for _, line := range strings.Split(shell, "\n") {
			if err := validateKillLine(line); err != nil {
				t.Fatalf("%v in %s", err, shell)
			}
		}
	}
}

func shellCommand(argv []string) string {
	for i, arg := range argv {
		if arg == "shell" {
			return strings.Join(argv[i+1:], " ")
		}
	}
	return ""
}

var bareKillRe = regexp.MustCompile(`^kill -TERM [0-9]+$`)

func isBareKill(shell string) bool {
	return bareKillRe.MatchString(strings.TrimSpace(shell))
}

func validateKillLine(line string) error {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "kill") {
		return nil
	}
	switch line {
	case `kill -TERM "$pid"`, `kill -0 "$pid" 2>/dev/null || exit 72`:
		return nil
	default:
		return fmt.Errorf("unsafe kill line %q", line)
	}
}

var (
	scriptTokenRe = regexp.MustCompile(`token='([0-9a-f]{32})'`)
	stopPidRe     = regexp.MustCompile(`(?m)^pid=([0-9]+)$`)
	stopStartRe   = regexp.MustCompile(`(?m)^expected_start=([0-9]+)$`)
	stopArgRe     = regexp.MustCompile(`(?m)^test "\$arg[0-9]+" = '([^']*)' \|\| exit 0$`)
	bareInScript  = regexp.MustCompile(`kill -TERM [0-9]+`)
)

func tokenFromScript(cmd string) string {
	match := scriptTokenRe.FindStringSubmatch(cmd)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func (f *fakeModule) evalStop(cmd string) (string, error) {
	const killLine = "kill -TERM \"$pid\""
	killAt := strings.Index(cmd, killLine)
	if killAt < 0 || strings.Contains(cmd[killAt+len(killLine):], "kill -TERM") || bareInScript.MatchString(cmd) {
		return "", errors.New("stop script kill is not a single guarded kill -TERM \"$pid\"")
	}
	head := cmd[:killAt]
	if !strings.Contains(head, `test "$current_start" = "$expected_start"`) || !strings.Contains(head, `test -z "$extra"`) {
		return "", errors.New("stop script does not recheck identity before kill")
	}
	for _, loc := range stopArgRe.FindAllStringIndex(cmd, -1) {
		if loc[0] > killAt {
			return "", errors.New("argv check is after kill")
		}
	}
	pidMatch := stopPidRe.FindStringSubmatch(cmd)
	startMatch := stopStartRe.FindStringSubmatch(cmd)
	argMatches := stopArgRe.FindAllStringSubmatch(cmd, -1)
	if pidMatch == nil || startMatch == nil || len(argMatches) == 0 {
		return "", fmt.Errorf("stop script identity block unreadable")
	}
	pid, start := pidMatch[1], startMatch[1]
	argv := make([]string, len(argMatches))
	for i, match := range argMatches {
		argv[i] = match[1]
	}
	if f.beforeFinalStop != nil {
		f.beforeFinalStop(pid)
	}
	proc := f.proc(pid)
	if proc == nil {
		f.skipped = append(f.skipped, pid+":missing")
		return "skip-missing", nil
	}
	if proc.start != start {
		f.skipped = append(f.skipped, pid+":reuse")
		return "skip-reuse", nil
	}
	if !argvEqual(proc.argv, argv) {
		f.skipped = append(f.skipped, pid+":argv")
		return "skip-argv", nil
	}
	return f.kill(pid)
}

func snapCount() int {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "halo-qdc507-snap-*"))
	if err != nil {
		return -1
	}
	return len(matches)
}

func stopCount(rt *Runtime) int {
	count := 0
	for _, proc := range rt.Processes() {
		if proc.StopOnClose {
			count++
		}
	}
	return count
}

func TestFakeWrongCardDoesNotPushOrLoad(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	fake.cards = "0 [unrelated]: wrong sound card"
	before := snapCount()
	status, err := Prepare(context.Background(), cfg)
	if err == nil || status.Ready || status.Runtime != nil || !strings.Contains(status.Reason, "card0") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(fake.pushed) != 0 || len(fake.insmod) != 0 || len(fake.killed) != 0 {
		t.Fatalf("pushed=%v insmod=%v killed=%v", fake.pushed, fake.insmod, fake.killed)
	}
	for _, argv := range fake.calls {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "push") || strings.Contains(joined, "insmod") || strings.Contains(joined, "sha256sum") || strings.Contains(joined, "halo-cellbridge-route.pid") {
			t.Fatalf("unverified target touched: %s", joined)
		}
	}
	if snapCount() != before {
		t.Fatalf("snapshot leak %d -> %d", before, snapCount())
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestCalibrationEndpointFailureRollsBack(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	other := fake.spawn([]string{"/bin/sleep", "10"}, "/bin/sleep", "")
	fake.failEndpoints = true
	status, err := Prepare(context.Background(), cfg)
	if err == nil || status.Ready || status.Runtime != nil || !strings.Contains(status.Reason, "端点") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if fake.aliveArgv0(calibrationArgv[0]) != 0 || fake.aliveArgv0(remoteBridge) != 0 {
		t.Fatalf("procs=%+v", fake.procs)
	}
	if fake.aliveArgv0("/bin/sleep") != 1 || containsString(fake.killed, other.pid) {
		t.Fatalf("unrelated killed=%v procs=%+v", fake.killed, fake.procs)
	}
	if len(fake.killed) != 1 {
		t.Fatalf("killed=%v", fake.killed)
	}
	if len(fake.removed) == 0 {
		t.Fatal("stage directory was left behind")
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestRouteReadyFailureRollsBack(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	other := fake.spawn([]string{"/bin/sleep", "10"}, "/bin/sleep", "")
	fake.failReady = true
	status, err := Prepare(context.Background(), cfg)
	if err == nil || status.Ready || status.Runtime != nil || !strings.Contains(status.Reason, "RUNNING") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if fake.aliveArgv0(calibrationArgv[0]) != 0 || fake.aliveArgv0(remoteBridge) != 0 {
		t.Fatalf("procs=%+v killed=%v", fake.procs, fake.killed)
	}
	if len(fake.killed) != 2 || containsString(fake.killed, other.pid) || fake.aliveArgv0("/bin/sleep") != 1 {
		t.Fatalf("killed=%v procs=%+v", fake.killed, fake.procs)
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestRouteCancelRollsBackWithIndependentContext(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.cancel = cancel
	status, err := Prepare(ctx, cfg)
	if !errors.Is(err, context.Canceled) || status.Ready || status.Runtime != nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(fake.killed) != 2 || fake.aliveArgv0(remoteBridge) != 0 || fake.aliveArgv0(calibrationArgv[0]) != 0 {
		t.Fatalf("killed=%v procs=%+v", fake.killed, fake.procs)
	}
	kills := 0
	for i, argv := range fake.calls {
		if strings.Contains(strings.Join(argv, " "), "kill -TERM") {
			kills++
			if fake.ctxErr[i] != nil {
				t.Fatal("rollback reused the cancelled caller context")
			}
		}
	}
	if kills != 2 {
		t.Fatalf("kills=%d", kills)
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestCloseTwiceStopsCreatedOnce(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	before := snapCount()
	status, err := Prepare(context.Background(), cfg)
	if err != nil || !status.Ready || status.Runtime == nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if stopCount(status.Runtime) != 2 {
		t.Fatalf("processes=%+v", status.Runtime.Processes())
	}
	for _, proc := range status.Runtime.Processes() {
		if !proc.Created || proc.Owner != processOwner || proc.PID == "" || proc.StartTime == "" || len(proc.Argv) == 0 {
			t.Fatalf("process=%+v", proc)
		}
	}
	if err := status.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.killed) != 2 || fake.aliveArgv0(remoteBridge) != 0 || fake.aliveArgv0(calibrationArgv[0]) != 0 {
		t.Fatalf("killed=%v", fake.killed)
	}
	if err := status.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.killed) != 2 {
		t.Fatalf("second close killed=%v", fake.killed)
	}
	if snapCount() != before {
		t.Fatalf("snapshot leak %d -> %d", before, snapCount())
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestTransportUSBAndPIDReuseDoNotKill(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		fake.transport = "99"
		if err := status.Runtime.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fake.killed) != 0 || fake.aliveArgv0(remoteBridge) != 1 {
			t.Fatalf("killed=%v procs=%+v", fake.killed, fake.procs)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("usb", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		fake.usb = "9-9.1"
		if err := status.Runtime.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fake.killed) != 0 || fake.aliveArgv0(remoteBridge) != 1 || fake.aliveArgv0(calibrationArgv[0]) != 1 {
			t.Fatalf("killed=%v", fake.killed)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("pid-reuse", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		var routePID string
		for i := range fake.procs {
			if len(fake.procs[i].argv) > 0 && fake.procs[i].argv[0] == remoteBridge {
				fake.procs[i].start = "1"
				routePID = fake.procs[i].pid
			}
		}
		if err := status.Runtime.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if containsString(fake.killed, routePID) || fake.proc(routePID) == nil {
			t.Fatalf("reused pid killed=%v", fake.killed)
		}
		if fake.aliveArgv0(calibrationArgv[0]) != 0 {
			t.Fatal("calibration was not cleaned")
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("external-argv", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		var routePID string
		for i := range fake.procs {
			if len(fake.procs[i].argv) > 0 && fake.procs[i].argv[0] == remoteBridge {
				fake.procs[i].argv = []string{"/bin/replaced"}
				routePID = fake.procs[i].pid
			}
		}
		if err := status.Runtime.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if containsString(fake.killed, routePID) || fake.proc(routePID) == nil {
			t.Fatalf("external pid killed=%v", fake.killed)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
}

func TestRemoteAndUploadHashFailuresDoNotInstall(t *testing.T) {
	t.Run("remote-hash", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		for _, art := range trustedRuntimeArtifacts {
			fake.files[remoteRuntimeDir+"/"+art.name] = fakeFile{hash: strings.Repeat("ab", 32)}
		}
		status, err := Prepare(context.Background(), cfg)
		if !errors.Is(err, errRemoteHash) || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		if len(fake.pushed) != 0 || len(fake.insmod) != 0 || len(fake.killed) != 0 {
			t.Fatalf("pushed=%d insmod=%v killed=%v", len(fake.pushed), fake.insmod, fake.killed)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("owned-old-exe", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		proc := fake.spawn(routeArgv, "/opt/old/mavo-pcm-bridge.armv7", strings.Repeat("ab", 32))
		fake.routePID = proc.pid + " " + proc.start
		fake.routeLogOK = true
		fake.audioEnable = "1"
		fake.pcmRunning = true
		for _, art := range trustedRuntimeArtifacts {
			fake.files[remoteRuntimeDir+"/"+art.name] = fakeFile{hash: strings.Repeat("ef", 32)}
		}
		status, err := Prepare(context.Background(), cfg)
		if !errors.Is(err, errOwnedContent) || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		if len(fake.pushed) != 0 || len(fake.insmod) != 0 || len(fake.killed) != 0 || fake.proc(proc.pid) == nil {
			t.Fatalf("pushed=%d insmod=%v killed=%v", len(fake.pushed), fake.insmod, fake.killed)
		}
		sawExe := false
		for _, argv := range fake.calls {
			joined := strings.Join(argv, " ")
			if strings.Contains(joined, "sha256sum /proc/"+proc.pid+"/exe") {
				sawExe = true
			}
		}
		if !sawExe {
			t.Fatal("owned fast path did not hash /proc/pid/exe")
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("upload-hash", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		fake.lieStage = true
		before := snapCount()
		status, err := Prepare(context.Background(), cfg)
		if !errors.Is(err, errUploadHash) || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		if len(fake.pushed) != len(trustedRuntimeArtifacts) || len(fake.insmod) != 0 {
			t.Fatalf("pushed=%v insmod=%v", fake.pushed, fake.insmod)
		}
		if len(fake.removed) == 0 {
			t.Fatal("stage was not removed")
		}
		for path := range fake.files {
			if strings.Contains(path, "/stage-") {
				t.Fatalf("stage file left %s", path)
			}
		}
		if snapCount() != before {
			t.Fatalf("snapshot leak %d -> %d", before, snapCount())
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
}

func TestForeignRouteRefusedBeforeUpload(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	proc := fake.spawn([]string{"/usr/bin/mavo-pcm-bridge.armv7", "--other"}, "/usr/bin/mavo-pcm-bridge.armv7", strings.Repeat("ab", 32))
	status, err := Prepare(context.Background(), cfg)
	if !errors.Is(err, errForeignRoute) || status.Ready || status.Runtime != nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(fake.pushed) != 0 || len(fake.insmod) != 0 || len(fake.killed) != 0 || fake.proc(proc.pid) == nil {
		t.Fatalf("pushed=%d insmod=%v killed=%v", len(fake.pushed), fake.insmod, fake.killed)
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestSnapshotPushIgnoresReplacedSource(t *testing.T) {
	cfg, fake, dir := newFixture(t)
	bridge := filepath.Join(dir, "mavo-pcm-bridge.armv7")
	original, err := os.ReadFile(bridge)
	if err != nil {
		t.Fatal(err)
	}
	tampered := false
	fake.before = func(argv []string) {
		if tampered || !strings.Contains(strings.Join(argv, " "), "asound/cards") {
			return
		}
		tampered = true
		if err := os.WriteFile(bridge, []byte("tampered-bytes"), 0o600); err != nil {
			t.Errorf("tamper: %v", err)
		}
	}
	status, err := Prepare(context.Background(), cfg)
	if err != nil || !status.Ready {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	defer status.Runtime.Close(context.Background())
	if !tampered {
		t.Fatal("source was not replaced")
	}
	var saw bool
	for i, dest := range fake.pushDest {
		if !strings.HasSuffix(dest, "mavo-pcm-bridge.armv7") {
			continue
		}
		saw = true
		pushed := fake.pushedBytes[i]
		if !bytes.Equal(pushed, original) || bytes.Equal(pushed, []byte("tampered-bytes")) {
			t.Fatal("push followed the replaced path")
		}
		if !strings.Contains(fake.pushed[i], "halo-qdc507-snap-") || strings.Contains(fake.pushed[i], dir) {
			t.Fatalf("push source %s", fake.pushed[i])
		}
	}
	if !saw {
		t.Fatal("bridge was not pushed")
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestRepeatedPrepareAdoptsWithoutDoubleStopOrSnapshotLeak(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	before := snapCount()
	first, err := Prepare(context.Background(), cfg)
	if err != nil || !first.Ready || stopCount(first.Runtime) != 2 {
		t.Fatalf("status=%+v err=%v procs=%+v", first, err, first.Runtime.Processes())
	}
	routeOwner := fake.ownerFiles[routeOwnerFile]
	calOwner := fake.ownerFiles[calibrationOwnerFile]
	if _, _, token, ok := parseOwnerRecord(routeOwner); !ok || token != first.Runtime.token {
		t.Fatalf("route owner %q token %q", routeOwner, first.Runtime.token)
	}
	if _, _, token, ok := parseOwnerRecord(calOwner); !ok || token != first.Runtime.token {
		t.Fatalf("calibration owner %q", calOwner)
	}
	pushed := len(fake.pushed)
	loaded := len(fake.insmod)
	second, err := Prepare(context.Background(), cfg)
	if err != nil || !second.Ready || second.Runtime == nil {
		t.Fatalf("status=%+v err=%v", second, err)
	}
	if len(fake.pushed) != pushed || len(fake.insmod) != loaded {
		t.Fatalf("second prepare pushed=%d insmod=%d", len(fake.pushed)-pushed, len(fake.insmod)-loaded)
	}
	if fake.aliveArgv0(remoteBridge) != 1 {
		t.Fatalf("procs=%+v", fake.procs)
	}
	if stopCount(second.Runtime) != 0 {
		t.Fatalf("second handle stole cleanup: %+v", second.Runtime.Processes())
	}
	if fake.ownerFiles[routeOwnerFile] != routeOwner || fake.ownerFiles[calibrationOwnerFile] != calOwner {
		t.Fatal("later prepare rewrote owner records")
	}
	if second.Runtime.token == first.Runtime.token {
		t.Fatal("two prepares share an owner token")
	}
	for _, proc := range second.Runtime.Processes() {
		if proc.Created || proc.StopOnClose || proc.Owner != processOwner {
			t.Fatalf("adopted=%+v", proc)
		}
	}
	if err := second.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.killed) != 0 || fake.aliveArgv0(remoteBridge) != 1 {
		t.Fatalf("viewer close killed=%v", fake.killed)
	}
	if err := first.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.killed) != 2 {
		t.Fatalf("owner close killed=%v", fake.killed)
	}
	if err := first.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.killed) != 2 {
		t.Fatalf("second owner close killed=%v", fake.killed)
	}
	if snapCount() != before {
		t.Fatalf("snapshot leak %d -> %d", before, snapCount())
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestFastPathClaimsOrphanAndCloseStopsOnce(t *testing.T) {
	cfg, fake, _ := newFixture(t)
	cfg.Bootstrap = false
	proc := fake.spawn(routeArgv, remoteBridge, artifactHash("mavo-pcm-bridge.armv7"))
	fake.routePID = proc.pid + " " + proc.start
	fake.routeLogOK = true
	fake.audioEnable = "1"
	fake.pcmRunning = true
	for _, art := range trustedRuntimeArtifacts {
		fake.files[remoteRuntimeDir+"/"+art.name] = fakeFile{hash: art.hash}
	}
	status, err := Prepare(context.Background(), cfg)
	if err != nil || !status.Ready || status.Runtime == nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(fake.pushed) != 0 || len(fake.insmod) != 0 {
		t.Fatalf("pushed=%v insmod=%v", fake.pushed, fake.insmod)
	}
	var adopted Process
	for _, proc := range status.Runtime.Processes() {
		if proc.Kind == kindRoute {
			adopted = proc
		}
	}
	if adopted.Created || !adopted.StopOnClose || adopted.PID != proc.pid || adopted.Owner != processOwner {
		t.Fatalf("adopted=%+v", adopted)
	}
	if err := status.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.killed) != 1 || fake.killed[0] != proc.pid || fake.proc(proc.pid) != nil {
		t.Fatalf("killed=%v", fake.killed)
	}
	if err := status.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.killed) != 1 {
		t.Fatalf("second close killed=%v", fake.killed)
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func TestSnapshotIsPrivate(t *testing.T) {
	previous := append([]runtimeArtifact(nil), trustedRuntimeArtifacts...)
	restoreHashes(t, previous)
	dir := writeRuntime(t, nil)
	snap, err := snapshotVerifiedRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(snap) })
	info, err := os.Lstat(snap)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("snap mode %v", info.Mode())
	}
	for _, art := range trustedRuntimeArtifacts {
		file, err := os.Lstat(filepath.Join(snap, art.name))
		if err != nil {
			t.Fatal(err)
		}
		if !file.Mode().IsRegular() || file.Mode().Perm()&0o022 != 0 || file.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s mode %v", art.name, file.Mode())
		}
	}
}

func TestFailedStartReclaimsOnlyThisRun(t *testing.T) {
	t.Run("cal-partial-track", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		other := fake.spawn([]string{"/opt/foreign/helper"}, "/opt/foreign/helper", "")
		fake.calMode = "partial"
		status, err := Prepare(context.Background(), cfg)
		if err == nil || status.Ready || status.Runtime != nil || !strings.Contains(status.Reason, "校准") {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		if _, ok := fake.ownerFiles[calibrationOwnerFile]; ok {
			t.Fatal("partial stdout case left an owner record")
		}
		cal := spawnedPID(fake, calibrationArgv[0])
		if cal == "" || !containsString(fake.killed, cal) || fake.proc(cal) != nil {
			t.Fatalf("calibration not reclaimed killed=%v procs=%+v", fake.killed, fake.procs)
		}
		if containsString(fake.killed, other.pid) || fake.proc(other.pid) == nil || len(fake.killed) != 1 {
			t.Fatalf("foreign killed=%v", fake.killed)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("cal-empty-output", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		other := fake.spawn([]string{"/opt/foreign/helper"}, "/opt/foreign/helper", "")
		fake.calMode = "empty"
		status, err := Prepare(context.Background(), cfg)
		if err == nil || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		pid, _, token, ok := parseOwnerRecord(fake.ownerFiles[calibrationOwnerFile])
		if !ok || token == "" || !containsString(fake.killed, pid) || fake.proc(pid) != nil {
			t.Fatalf("owner %q killed=%v", fake.ownerFiles[calibrationOwnerFile], fake.killed)
		}
		if !sawLiveOwnerRead(fake, calibrationOwnerFile) {
			t.Fatal("empty stdout was not recovered from the owner record")
		}
		if containsString(fake.killed, other.pid) || fake.proc(other.pid) == nil || len(fake.killed) != 1 {
			t.Fatalf("foreign killed=%v", fake.killed)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("cal-truncated-track", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		fake.calMode = "truncated"
		status, err := Prepare(context.Background(), cfg)
		if err == nil || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		pid, _, _, ok := parseOwnerRecord(fake.ownerFiles[calibrationOwnerFile])
		if !ok || !containsString(fake.killed, pid) || fake.proc(pid) != nil {
			t.Fatalf("truncated track was not recovered killed=%v owner=%q", fake.killed, fake.ownerFiles[calibrationOwnerFile])
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("cal-cancelled-no-output", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fake.cancel = cancel
		fake.calMode = "empty-cancel"
		other := fake.spawn([]string{"/bin/sleep", "10"}, "/bin/sleep", "")
		status, err := Prepare(ctx, cfg)
		if !errors.Is(err, context.Canceled) || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		pid, _, _, ok := parseOwnerRecord(fake.ownerFiles[calibrationOwnerFile])
		if !ok || !containsString(fake.killed, pid) || fake.proc(pid) != nil {
			t.Fatalf("cancelled calibration leaked killed=%v", fake.killed)
		}
		if containsString(fake.killed, other.pid) || fake.proc(other.pid) == nil {
			t.Fatalf("unrelated killed=%v", fake.killed)
		}
		if !ownerReadSurvivedCancel(t, fake, calibrationOwnerFile) {
			t.Fatal("owner read inherited the cancelled caller context")
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("route-adb-error", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		other := fake.spawn([]string{"/bin/sleep", "10"}, "/bin/sleep", "")
		fake.routeMode = "empty"
		status, err := Prepare(context.Background(), cfg)
		if err == nil || status.Ready || status.Runtime != nil || !strings.Contains(status.Reason, "路由") {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		route := spawnedPID(fake, remoteBridge)
		if route == "" || !containsString(fake.killed, route) || fake.proc(route) != nil || fake.aliveArgv0(calibrationArgv[0]) != 0 {
			t.Fatalf("route/cal leaked killed=%v procs=%+v", fake.killed, fake.procs)
		}
		if !sawLiveOwnerRead(fake, routeOwnerFile) {
			t.Fatal("route adb error was not recovered from the owner record")
		}
		if containsString(fake.killed, other.pid) || fake.proc(other.pid) == nil || len(fake.killed) != 2 {
			t.Fatalf("killed=%v", fake.killed)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("route-cancelled-no-output", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fake.cancel = cancel
		fake.routeMode = "cancel"
		status, err := Prepare(ctx, cfg)
		if !errors.Is(err, context.Canceled) || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		route := spawnedPID(fake, remoteBridge)
		if route == "" || !containsString(fake.killed, route) || fake.aliveArgv0(remoteBridge) != 0 || fake.aliveArgv0(calibrationArgv[0]) != 0 {
			t.Fatalf("cancelled route leaked killed=%v procs=%+v", fake.killed, fake.procs)
		}
		if !ownerReadSurvivedCancel(t, fake, routeOwnerFile) {
			t.Fatal("route owner read inherited the cancelled caller context")
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("owned-cal-error", func(t *testing.T) {
		assertOwnedCalibrationNotReclaimed(t, "owned-error")
	})
	t.Run("owned-cal-track-line", func(t *testing.T) {
		assertOwnedCalibrationNotReclaimed(t, "owned-track")
	})
	t.Run("foreign-calibration", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		foreign := fake.spawn(calibrationArgv, calibrationArgv[0], "")
		status, err := Prepare(context.Background(), cfg)
		if !errors.Is(err, errForeignRoute) || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		if len(fake.killed) != 0 || fake.proc(foreign.pid) == nil || fake.aliveArgv0(calibrationArgv[0]) != 1 {
			t.Fatalf("foreign calibration killed=%v procs=%+v", fake.killed, fake.procs)
		}
		if _, ok := fake.ownerFiles[calibrationOwnerFile]; ok {
			t.Fatal("foreign calibration wrote an owner record")
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("pending-record", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		fake.calMode = "pending"
		status, err := Prepare(context.Background(), cfg)
		if err == nil || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		pid := spawnedPID(fake, calibrationArgv[0])
		if pid == "" || containsString(fake.killed, pid) || fake.proc(pid) == nil {
			t.Fatalf("pending record reclaimed killed=%v procs=%+v", fake.killed, fake.procs)
		}
		if _, _, _, ok := parseOwnerRecord(fake.ownerFiles[calibrationOwnerFile]); ok {
			t.Fatalf("pending line was treated as a pid record %q", fake.ownerFiles[calibrationOwnerFile])
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("reused-pid", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		fake.calMode = "stale"
		status, err := Prepare(context.Background(), cfg)
		if err == nil || status.Ready || status.Runtime != nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		pid, start, _, ok := parseOwnerRecord(fake.ownerFiles[calibrationOwnerFile])
		proc := fake.proc(pid)
		if !ok || proc == nil || proc.start == start || containsString(fake.killed, pid) {
			t.Fatalf("reused pid reclaimed owner=%q proc=%+v killed=%v", fake.ownerFiles[calibrationOwnerFile], proc, fake.killed)
		}
		for _, argv := range fake.calls {
			shell := shellCommand(argv)
			if strings.Contains(shell, "# halo-qdc507-stop") && strings.Contains(shell, "pid="+pid) {
				t.Fatal("reused pid was sent a stop script")
			}
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("owned-route-not-ready", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		proc := fake.spawn(routeArgv, remoteBridge, artifactHash("mavo-pcm-bridge.armv7"))
		fake.routePID = proc.pid + " " + proc.start
		fake.audioEnable = "1"
		fake.pcmRunning = true
		for _, art := range trustedRuntimeArtifacts {
			fake.files[remoteRuntimeDir+"/"+art.name] = fakeFile{hash: art.hash}
		}
		owner := proc.pid + " " + proc.start + " " + strings.Repeat("cd", 16)
		fake.ownerFiles[routeOwnerFile] = owner
		foreign := fake.spawn([]string{"/usr/bin/mavo-pcm-bridge.armv7", "--other"}, "/usr/bin/mavo-pcm-bridge.armv7", strings.Repeat("ab", 32))
		status, err := Prepare(context.Background(), cfg)
		if err == nil || status.Ready || status.Runtime != nil || !strings.Contains(status.Reason, "已有路由") {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		if len(fake.killed) != 0 || fake.proc(proc.pid) == nil || fake.proc(foreign.pid) == nil {
			t.Fatalf("pre-existing processes killed=%v", fake.killed)
		}
		if fake.ownerFiles[routeOwnerFile] != owner {
			t.Fatalf("owner rewritten %q", fake.ownerFiles[routeOwnerFile])
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
}

func TestStopScriptRechecksIdentityBeforeSignal(t *testing.T) {
	t.Run("changed-after-preflight", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil || status.Runtime == nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		routePID := spawnedPID(fake, remoteBridge)
		fake.beforeFinalStop = func(pid string) {
			if pid != routePID {
				return
			}
			for i := range fake.procs {
				if fake.procs[i].pid == pid {
					fake.procs[i].start = "999999"
					fake.procs[i].argv = []string{"/bin/replacement"}
				}
			}
		}
		closeAt := len(fake.calls)
		if err := status.Runtime.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if containsString(fake.killed, routePID) || fake.proc(routePID) == nil || !argvEqual(fake.proc(routePID).argv, []string{"/bin/replacement"}) {
			t.Fatalf("replacement signalled killed=%v proc=%+v", fake.killed, fake.proc(routePID))
		}
		if !containsString(fake.skipped, routePID+":reuse") {
			t.Fatalf("final check did not reject the replacement skipped=%v", fake.skipped)
		}
		if fake.aliveArgv0(calibrationArgv[0]) != 0 || len(fake.killed) != 1 {
			t.Fatalf("calibration was not stopped killed=%v", fake.killed)
		}
		sawStart, sawArgv, sawStop := false, false, false
		for _, argv := range fake.calls[closeAt:] {
			shell := shellCommand(argv)
			if isBareKill(shell) {
				t.Fatalf("bare kill across adb commands: %s", shell)
			}
			if shell == cmdStart(routePID) {
				sawStart = true
			}
			if shell == cmdCmdline(routePID) {
				sawArgv = true
			}
			if strings.Contains(shell, "# halo-qdc507-stop") && strings.Contains(shell, "pid="+routePID) {
				if !sawStart || !sawArgv {
					t.Fatal("stop script ran before preflight")
				}
				sawStop = true
			}
		}
		if !sawStop {
			t.Fatal("preflight passed but the guarded stop script was not sent")
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("unchanged", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := status.Runtime.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fake.killed) != 2 || len(fake.skipped) != 0 || fake.aliveArgv0(remoteBridge) != 0 || fake.aliveArgv0(calibrationArgv[0]) != 0 {
			t.Fatalf("normal close killed=%v skipped=%v", fake.killed, fake.skipped)
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
}

func TestCloseCancelledContextStillReclaims(t *testing.T) {
	t.Run("matching-device", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil || status.Runtime == nil {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		closeAt := len(fake.calls)
		if err := status.Runtime.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if len(fake.killed) != 2 || fake.aliveArgv0(remoteBridge) != 0 || fake.aliveArgv0(calibrationArgv[0]) != 0 {
			t.Fatalf("cancelled close leaked killed=%v", fake.killed)
		}
		if closeAt == len(fake.calls) {
			t.Fatal("cancelled close issued no cleanup commands")
		}
		for i := closeAt; i < len(fake.calls); i++ {
			if fake.ctxErr[i] != nil {
				t.Fatalf("cleanup inherited caller cancellation: %s (%v)", strings.Join(fake.calls[i], " "), fake.ctxErr[i])
			}
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
	t.Run("usb-changed", func(t *testing.T) {
		cfg, fake, _ := newFixture(t)
		status, err := Prepare(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fake.usb = "9-9.1"
		closeAt := len(fake.calls)
		if err := status.Runtime.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if len(fake.killed) != 0 || fake.aliveArgv0(remoteBridge) != 1 || fake.aliveArgv0(calibrationArgv[0]) != 1 {
			t.Fatalf("changed usb was killed=%v", fake.killed)
		}
		sawDevices := false
		for i := closeAt; i < len(fake.calls); i++ {
			if fake.ctxErr[i] != nil {
				t.Fatalf("device check inherited caller cancellation: %v", fake.ctxErr[i])
			}
			if strings.Contains(strings.Join(fake.calls[i], " "), "devices -l") {
				sawDevices = true
			}
		}
		if !sawDevices {
			t.Fatal("cancelled close skipped the USB check")
		}
		checkSafeCalls(t, fake.calls, fake.envs)
	})
}

func assertOwnedCalibrationNotReclaimed(t *testing.T, mode string) {
	t.Helper()
	cfg, fake, _ := newFixture(t)
	proc := fake.spawn(calibrationArgv, calibrationArgv[0], "")
	fake.calPID = proc.pid + " " + proc.start
	owner := proc.pid + " " + proc.start + " " + strings.Repeat("ab", 16)
	fake.ownerFiles[calibrationOwnerFile] = owner
	foreign := fake.spawn([]string{"/opt/foreign/helper"}, "/opt/foreign/helper", "")
	fake.calMode = mode
	status, err := Prepare(context.Background(), cfg)
	if err == nil || status.Ready || status.Runtime != nil || !strings.Contains(status.Reason, "校准") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(fake.killed) != 0 || fake.proc(proc.pid) == nil || fake.proc(foreign.pid) == nil {
		t.Fatalf("old or foreign process killed=%v procs=%+v", fake.killed, fake.procs)
	}
	if fake.ownerFiles[calibrationOwnerFile] != owner {
		t.Fatalf("owner rewritten %q", fake.ownerFiles[calibrationOwnerFile])
	}
	if fake.aliveArgv0(calibrationArgv[0]) != 1 {
		t.Fatal("owned calibration was replaced")
	}
	checkSafeCalls(t, fake.calls, fake.envs)
}

func spawnedPID(fake *fakeModule, argv0 string) string {
	for i := range fake.procs {
		if fake.procs[i].alive && len(fake.procs[i].argv) > 0 && fake.procs[i].argv[0] == argv0 {
			return fake.procs[i].pid
		}
	}
	for i := range fake.procs {
		if len(fake.procs[i].argv) > 0 && fake.procs[i].argv[0] == argv0 {
			return fake.procs[i].pid
		}
	}
	return ""
}

func sawLiveOwnerRead(fake *fakeModule, path string) bool {
	want := "cat " + path
	for _, argv := range fake.calls {
		if shellCommand(argv) == want {
			return true
		}
	}
	return false
}

func ownerReadSurvivedCancel(t *testing.T, fake *fakeModule, path string) bool {
	t.Helper()
	want := "cat " + path
	for i, argv := range fake.calls {
		if shellCommand(argv) != want {
			continue
		}
		if fake.ctxErr[i] != nil {
			t.Fatalf("owner read ctx %v", fake.ctxErr[i])
		}
		return true
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
