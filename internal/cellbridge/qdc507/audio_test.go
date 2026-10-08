package qdc507

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type scriptRunner struct {
	calls [][]string
	envs  [][]string
	reply func(argv []string) (string, error)
}

func (runner *scriptRunner) Run(ctx context.Context, argv []string, env []string) (string, error) {
	runner.calls = append(runner.calls, append([]string(nil), argv...))
	runner.envs = append(runner.envs, append([]string(nil), env...))
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	if runner.reply == nil {
		return "", nil
	}
	return runner.reply(argv)
}

func writeRuntime(t *testing.T, mutate func(name string, data []byte) []byte) string {
	t.Helper()
	dir := t.TempDir()
	for _, artifact := range trustedRuntimeArtifacts {
		raw := []byte("runtime-" + artifact.name)
		sum := sha256.Sum256(raw)
		// Tests cannot use the vendor bytes. Rewrite the allowlist entry to
		// the hash of this fixture unless mutate changes the bytes afterwards.
		artifact.hash = hex.EncodeToString(sum[:])
		for index := range trustedRuntimeArtifacts {
			if trustedRuntimeArtifacts[index].name == artifact.name && mutate == nil {
				trustedRuntimeArtifacts[index].hash = artifact.hash
			}
		}
		data := raw
		if mutate != nil {
			data = mutate(artifact.name, raw)
		}
		if err := os.WriteFile(filepath.Join(dir, artifact.name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func restoreHashes(t *testing.T, previous []runtimeArtifact) {
	t.Helper()
	t.Cleanup(func() {
		copy(trustedRuntimeArtifacts, previous)
	})
}

func TestPrepareRejectsModelAndUSBIDWithoutPath(t *testing.T) {
	runner := &scriptRunner{}
	status, err := Prepare(context.Background(), Config{
		OptIn:      true,
		DeviceType: "dji_4g",
		Model:      "QDC507",
		VendorID:   "2ca3",
		ProductID:  "4006",
		ADBPath:    "adb",
		Runner:     runner,
	})
	if err == nil || status.Ready {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if !strings.Contains(status.Reason, "USB") {
		t.Fatalf("reason = %q", status.Reason)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("adb was called: %#v", runner.calls)
	}
}

func TestPrepareOptInOffDoesNotTouchADB(t *testing.T) {
	runner := &scriptRunner{}
	status, err := Prepare(context.Background(), Config{OptIn: false, Runner: runner, ADBPath: "adb"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || !strings.Contains(status.Reason, "未启用") {
		t.Fatalf("status=%+v", status)
	}
	if len(runner.calls) != 0 {
		t.Fatal("adb called while disabled")
	}
}

func TestPrepareNamesMissingRuntimeFile(t *testing.T) {
	dir := t.TempDir()
	status, err := Prepare(context.Background(), Config{
		OptIn: true, DeviceType: "dji_4g", USBPath: "1-1.2", RuntimeDir: dir, ADBPath: "adb",
	})
	if err == nil || status.Ready {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if !strings.Contains(status.Reason, "mavo-pcm-bridge.armv7") {
		t.Fatalf("reason = %q", status.Reason)
	}
}

func TestPrepareHashMismatch(t *testing.T) {
	previous := append([]runtimeArtifact(nil), trustedRuntimeArtifacts...)
	restoreHashes(t, previous)
	dir := writeRuntime(t, nil)
	trustedRuntimeArtifacts[0].hash = strings.Repeat("ab", 32)
	status, err := Prepare(context.Background(), Config{
		OptIn: true, DeviceType: "dji_4g", USBPath: "1-1.2", RuntimeDir: dir, ADBPath: "adb",
	})
	if err == nil || !strings.Contains(status.Reason, "SHA-256") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestADBInvocationPreservesEnvironment(t *testing.T) {
	t.Setenv("HOME", "/home/halo")
	argv, env, err := ADBInvocation("adb", "tcp:127.0.0.1:5038", []string{"devices", "-l"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "pkill") || strings.Contains(joined, "kill-server") {
		t.Fatalf("argv = %q", joined)
	}
	if argv[0] != "adb" || argv[1] != "-L" || argv[2] != "tcp:127.0.0.1:5038" {
		t.Fatalf("argv = %#v", argv)
	}
	home := ""
	socket := ""
	for _, item := range env {
		if strings.HasPrefix(item, "HOME=") {
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

func TestScriptsDoNotKillForeignProcesses(t *testing.T) {
	for _, script := range []string{routeStartScript, ownedReadyScript, calibrationScript, remoteFilesScript, foreignScan, pcmReadyScript, endpointScript} {
		if strings.Contains(script, "pkill") || strings.Contains(script, "killall") || strings.Contains(script, "HOME=/root") {
			t.Fatalf("unsafe script: %s", script)
		}
	}
	if strings.Contains(routeStartScript, "kill -TERM") || strings.Contains(calibrationScript, "kill -TERM") {
		t.Fatal("start scripts must not terminate a route")
	}
}

func TestStartScriptsRecordOwnerBeforeWait(t *testing.T) {
	for name, script := range map[string]string{"calibration": calibrationScript, "route": routeStartScript} {
		marker := strings.Index(script, "origin=created")
		if marker < 0 {
			t.Fatalf("%s missing created path", name)
		}
		head := script[:marker]
		if strings.Contains(head, "printf 'pending") || strings.Contains(head, "halo-track") {
			t.Fatalf("%s records ownership before the created path", name)
		}
		rest := script[marker:]
		pending := strings.Index(rest, "printf 'pending")
		nohup := strings.Index(rest, "nohup")
		record := strings.Index(rest, "printf '%s %s %s")
		track := strings.Index(rest, "halo-track")
		if pending < 0 || nohup < 0 || record < 0 || track < 0 || !(pending < nohup && nohup < record && record < track) {
			t.Fatalf("%s order pending=%d nohup=%d record=%d track=%d", name, pending, nohup, record, track)
		}
		if !strings.Contains(script, "token='"+ownerTokenPlaceholder+"'") {
			t.Fatalf("%s missing owner token", name)
		}
	}
	rest := calibrationScript[strings.Index(calibrationScript, "origin=created"):]
	if strings.Index(rest, "halo-track") > strings.Index(rest, "while test") {
		t.Fatal("calibration track is printed after the FIFO wait")
	}
	if strings.Contains(calibrationOwnerFile, " ") || strings.Contains(routeOwnerFile, " ") {
		t.Fatal("owner file path is not a single shell word")
	}
	if !strings.Contains(calibrationScript, calibrationOwnerFile) || !strings.Contains(routeStartScript, routeOwnerFile) {
		t.Fatal("start script owner path drifted from the recovery path")
	}
}

func TestSelectTransportID(t *testing.T) {
	listing := "List of devices attached\nserial device usb:1-1.2 transport_id:4 product:qdc model:QDC507\n"
	id, err := SelectTransportID(listing, "1-1.2")
	if err != nil || id != "4" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	listing += "other device usb:1-1.2 transport_id:5\n"
	if _, err := SelectTransportID(listing, "1-1.2"); err == nil {
		t.Fatal("expected multiple match error")
	}
	if _, err := SelectTransportID("serial device usb:2-1 transport_id:1\n", "1-1.2"); err == nil {
		t.Fatal("expected no match")
	}
}

func bootstrapReply(ownedChecks *int) func(argv []string) (string, error) {
	return func(argv []string) (string, error) {
		args := strings.Join(argv, " ")
		switch {
		case strings.Contains(args, "devices -l"):
			return "serial device usb:1-4.2 transport_id:7\n", nil
		case strings.Contains(args, "id -u"):
			return "0", nil
		case strings.Contains(args, "uname -r"):
			return expectedKernel, nil
		case strings.Contains(args, "halo-cellbridge-route.pid") && strings.Contains(args, "echo ready"):
			*ownedChecks++
			if *ownedChecks == 1 {
				return "missing", context.DeadlineExceeded
			}
			return "ready", nil
		case strings.Contains(args, "pcmC0D4p"):
			return "ready", nil
		case strings.Contains(args, "asound/cards"):
			return "0 [" + expectedCardName + "]", nil
		case strings.Contains(args, "echo missing"):
			return "missing", nil
		case strings.Contains(args, "sha256sum"):
			return fixtureRemoteSums(args), nil
		case strings.Contains(args, "# halo-qdc507-stop"):
			return "killed", nil
		case strings.Contains(args, "alsaucm"):
			return "halo-track calibration created 11 1100 /usr/bin/alsaucm_test", nil
		case strings.Contains(args, "ttyGS0"):
			return "ready", nil
		case strings.Contains(args, "nohup") && strings.Contains(args, "mavo-pcm-bridge"):
			return "halo-track route created 22 2200 " + remoteBridge + " --voice-route-session --verbose", nil
		case strings.Contains(args, "cp -f"):
			return "installed", nil
		case strings.Contains(args, "/proc/11/stat"):
			return "1100", nil
		case strings.Contains(args, "/proc/22/stat"):
			return "2200", nil
		case strings.Contains(args, "/proc/11/cmdline"):
			return "/usr/bin/alsaucm_test", nil
		case strings.Contains(args, "/proc/22/cmdline"):
			return remoteBridge + "\n--voice-route-session\n--verbose", nil
		case strings.Contains(args, "kill -TERM 11") || strings.Contains(args, "kill -TERM 22"):
			return "killed", nil
		case strings.Contains(args, "halo-cellbridge-route.pid"):
			return "", errors.New("No such file")
		default:
			return "", nil
		}
	}
}

func fixtureRemoteSums(args string) string {
	var b strings.Builder
	for _, field := range strings.Fields(args) {
		base := filepath.Base(field)
		for _, art := range trustedRuntimeArtifacts {
			if base == art.name {
				fmt.Fprintf(&b, "%s  %s\n", art.hash, field)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func TestPrepareBootstrapReady(t *testing.T) {
	resetClaims()
	t.Cleanup(resetClaims)
	previous := append([]runtimeArtifact(nil), trustedRuntimeArtifacts...)
	restoreHashes(t, previous)
	dir := writeRuntime(t, nil)
	ownedChecks := 0
	runner := &scriptRunner{reply: bootstrapReply(&ownedChecks)}
	status, err := Prepare(context.Background(), Config{
		OptIn: true, Bootstrap: true, DeviceType: "dji_4g", USBPath: "1-4.2",
		Model: "not-used", VendorID: "2ca3", ProductID: "4006",
		RuntimeDir: dir, ADBPath: "adb", ADBSocket: "tcp:127.0.0.1:5038", Runner: runner,
	})
	if err != nil || !status.Ready || status.Transport != "7" || status.Runtime == nil {
		t.Fatalf("status=%+v err=%v calls=%d", status, err, len(runner.calls))
	}
	var created int
	for _, proc := range status.Runtime.Processes() {
		if proc.Created && proc.StopOnClose && proc.Owner == processOwner {
			created++
		}
	}
	if created != 2 {
		t.Fatalf("processes=%+v", status.Runtime.Processes())
	}
	var pushed int
	for _, argv := range runner.calls {
		local, remote, ok := pushPair(argv)
		if !ok {
			continue
		}
		pushed++
		if !strings.Contains(local, "halo-qdc507-snap-") || strings.Contains(local, dir) {
			t.Fatalf("push source = %s dest %s", local, remote)
		}
		if !strings.Contains(remote, "/stage-") {
			t.Fatalf("push dest = %s", remote)
		}
	}
	if pushed != len(trustedRuntimeArtifacts) {
		t.Fatalf("pushes=%d", pushed)
	}
	for _, env := range runner.envs {
		for _, item := range env {
			if item == "HOME=/root" {
				t.Fatal("HOME=/root was injected")
			}
		}
	}
	for _, argv := range runner.calls {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "pkill") || strings.Contains(joined, "killall") {
			t.Fatalf("unsafe argv %q", joined)
		}
	}
	beforeClose := len(runner.calls)
	if err := status.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	kills := 0
	for _, argv := range runner.calls[beforeClose:] {
		shell := shellCommand(argv)
		if isBareKill(shell) {
			t.Fatalf("bare kill across adb commands: %s", shell)
		}
		for _, line := range strings.Split(shell, "\n") {
			if err := validateKillLine(line); err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(line) == `kill -TERM "$pid"` {
				kills++
			}
		}
	}
	if kills != 2 {
		t.Fatalf("kills=%d", kills)
	}
	after := len(runner.calls)
	if err := status.Runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != after {
		t.Fatal("second Close issued more commands")
	}
}

func pushPair(argv []string) (string, string, bool) {
	for i, arg := range argv {
		if arg == "push" && i+2 < len(argv) {
			return argv[i+1], argv[i+2], true
		}
	}
	return "", "", false
}

func TestStatusRuntimeOmittedFromJSON(t *testing.T) {
	body, err := json.Marshal(Status{Ready: true, Transport: "7", Runtime: &Runtime{transport: "7"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"Ready":true,"Reason":"","Transport":"7"}` {
		t.Fatalf("json = %s", body)
	}
}

func TestRuntimeDirectoryRejectsGroupOrWorldWritable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o707); err != nil {
		t.Fatal(err)
	}
	runner := &scriptRunner{}
	_, err := Prepare(context.Background(), Config{
		OptIn: true, DeviceType: "dji_4g", USBPath: "1-1.2", RuntimeDir: dir, ADBPath: "adb", Runner: runner,
	})
	if !errors.Is(err, errRuntimePerm) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("adb called for a writable directory")
	}
	previous := append([]runtimeArtifact(nil), trustedRuntimeArtifacts...)
	restoreHashes(t, previous)
	private := writeRuntime(t, nil)
	if err := os.Chmod(filepath.Join(private, "mavo-pcm-bridge.armv7"), 0o606); err != nil {
		t.Fatal(err)
	}
	runner = &scriptRunner{}
	_, err = Prepare(context.Background(), Config{
		OptIn: true, DeviceType: "dji_4g", USBPath: "1-1.2", RuntimeDir: private, ADBPath: "adb", Runner: runner,
	})
	if !errors.Is(err, errRuntimePerm) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("adb called for a writable file")
	}
}

func TestRuntimePathRejectsSymlink(t *testing.T) {
	previous := append([]runtimeArtifact(nil), trustedRuntimeArtifacts...)
	restoreHashes(t, previous)
	dir := writeRuntime(t, nil)
	target := filepath.Join(dir, "mavo-pcm-bridge.armv7")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", target); err != nil {
		t.Fatal(err)
	}
	runner := &scriptRunner{}
	_, err := Prepare(context.Background(), Config{
		OptIn: true, DeviceType: "dji_4g", USBPath: "1-1.2", RuntimeDir: dir, ADBPath: "adb", Runner: runner,
	})
	if !errors.Is(err, errRuntimeNotRegular) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("adb called for a symlink")
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	runner = &scriptRunner{}
	_, err = Prepare(context.Background(), Config{
		OptIn: true, DeviceType: "dji_4g", USBPath: "1-1.2", RuntimeDir: link, ADBPath: "adb", Runner: runner,
	})
	if err == nil || !strings.Contains(err.Error(), "真实目录") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("adb called for a directory symlink")
	}
}

func TestFixedAllowlistHashes(t *testing.T) {
	want := map[string]string{
		"mavo-pcm-bridge.armv7": "88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc",
		"qdc507_aprv3.ko":       "3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a",
		"qdc507_voice.ko":       "ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c",
	}
	if len(trustedRuntimeArtifacts) != len(want) {
		t.Fatalf("artifacts=%d", len(trustedRuntimeArtifacts))
	}
	for _, art := range trustedRuntimeArtifacts {
		if art.hash != want[art.name] || art.mode == "" {
			t.Fatalf("allowlist changed for %s: %+v", art.name, art)
		}
	}
}

func TestPrepareReaderIsNotQDC507(t *testing.T) {
	status, err := Prepare(context.Background(), Config{OptIn: true, DeviceType: "usb_sim_reader", USBPath: "1-1"})
	if err == nil || status.Ready || !strings.Contains(status.Reason, "usb_sim_reader") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
