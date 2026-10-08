package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/cellbridge/qdc507"
	"vocat/internal/cellbridge/voice"
	"vocat/internal/store"
)

// cellularAudio is the local voice path consumed by the cellular controller.
// Prepare only reserves the module route. Start is what opens capture-first ALSA.
type cellularAudio interface {
	Prepare(context.Context, string) error
	Start(context.Context, string) error
	ReadPCM([]int16) (int, error)
	WritePCM([]int16) error
	Stop(context.Context) error
}

type cellBridgeAudioSpec struct {
	DeviceID       string
	CaptureDevice  string
	PlaybackDevice string
	RuntimeDir     string
	ADBPath        string
	ADBSocket      string
	Bootstrap      bool
}

var usbBusPortPattern = regexp.MustCompile(`^[0-9]+-[0-9.]+$`)

// canonicalUSBBusPort converts a sysfs device path such as
// /sys/bus/usb/devices/1-1.2 into the bus-port form qdc507 requires.
// The full sysfs string is not a legal QDC507 USB path.
func canonicalUSBBusPort(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return "", errors.New("模块 USB 路径为空")
	}
	base := raw
	if strings.Contains(raw, "/") {
		base = filepath.Base(raw)
	}
	if colon := strings.Index(base, ":"); colon > 0 {
		base = base[:colon]
	}
	if !usbBusPortPattern.MatchString(base) {
		return "", fmt.Errorf("无法从 %q 得到 QDC507 USB 路径", raw)
	}
	return base, nil
}

// cellBridgeRouteVerifyInterval is how long a ready route may be shown
// without checking that its prepared processes are still running.
const cellBridgeRouteVerifyInterval = 15 * time.Second

// shouldSignalRuntime reports whether closing a QDC runtime may touch the
// device currently enumerated at the prepared USB port. A known generation
// that is now empty or different is another physical device and must not
// be signalled. An unknown pin still allows cleanup.
func shouldSignalRuntime(pinned, current string) bool {
	if pinned != "" && pinned != current {
		return false
	}
	return true
}

var alsaIndexPattern = regexp.MustCompile(`^(?:plug)?hw:(\d+)(?:,(\d+))?$`)
var alsaCardPattern = regexp.MustCompile(`^(?:plug)?hw:CARD=([A-Za-z0-9_-]+)(?:,DEV=(\d+))?$`)

func parseALSACardRef(name string) (index int, id string, err error) {
	name = strings.TrimSpace(name)
	switch strings.ToLower(name) {
	case "", "default", "sysdefault", "pulse", "null", "dmix", "dsnoop",
		"surround21", "surround40", "surround41", "surround50", "surround51", "surround71":
		return 0, "", fmt.Errorf("ALSA 设备 %q 是默认或虚拟设备，不能当作模块音频", name)
	}
	if match := alsaIndexPattern.FindStringSubmatch(name); match != nil {
		parsed, convErr := strconv.Atoi(match[1])
		if convErr != nil {
			return 0, "", fmt.Errorf("ALSA 设备 %q 无法对应到具体声卡", name)
		}
		return parsed, "", nil
	}
	if match := alsaCardPattern.FindStringSubmatch(name); match != nil {
		return -1, match[1], nil
	}
	return 0, "", fmt.Errorf("ALSA 设备 %q 无法对应到具体声卡", name)
}

func usbBusPortFromSysPath(path string) (string, error) {
	found := ""
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		base := part
		if colon := strings.Index(base, ":"); colon > 0 {
			base = base[:colon]
		}
		if usbBusPortPattern.MatchString(base) {
			found = base
		}
	}
	if found == "" {
		return "", fmt.Errorf("声卡路径 %s 不属于 USB 设备", path)
	}
	return found, nil
}

func cardUSBBusPort(root, cardName string) (string, error) {
	if root == "" {
		root = "/sys"
	}
	link := filepath.Join(root, "class", "sound", cardName, "device")
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		target, readErr := os.Readlink(link)
		if readErr != nil {
			return "", fmt.Errorf("声卡 %s 没有可核对的 USB 设备节点", cardName)
		}
		if filepath.IsAbs(target) {
			resolved = target
		} else {
			resolved = filepath.Clean(filepath.Join(filepath.Dir(link), target))
		}
	}
	return usbBusPortFromSysPath(resolved)
}

func resolveALSACard(root, name string) (string, error) {
	index, id, err := parseALSACardRef(name)
	if err != nil {
		return "", err
	}
	if root == "" {
		root = "/sys"
	}
	sound := filepath.Join(root, "class", "sound")
	if id != "" {
		entries, readErr := os.ReadDir(sound)
		if readErr != nil {
			return "", fmt.Errorf("无法读取声卡列表以核对 %s: %w", name, readErr)
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "card") {
				continue
			}
			raw, idErr := os.ReadFile(filepath.Join(sound, entry.Name(), "id"))
			if idErr != nil {
				continue
			}
			if strings.TrimSpace(string(raw)) == id {
				return entry.Name(), nil
			}
		}
		return "", fmt.Errorf("找不到 ALSA 声卡 %s", id)
	}
	card := "card" + strconv.Itoa(index)
	info, statErr := os.Stat(filepath.Join(sound, card))
	if statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("找不到 ALSA 声卡 %s", card)
	}
	return card, nil
}

// matchALSAUSB checks that both ALSA devices belong to the module USB port.
// Seeing arecord on the host is not enough: another microphone is rejected.
func matchALSAUSB(root, capture, playback, usbPort string) error {
	if usbPort == "" {
		return errors.New("所选模块没有可核对的 USB 路径")
	}
	captureCard, err := resolveALSACard(root, capture)
	if err != nil {
		return err
	}
	playbackCard, err := resolveALSACard(root, playback)
	if err != nil {
		return err
	}
	captureUSB, err := cardUSBBusPort(root, captureCard)
	if err != nil {
		return fmt.Errorf("录音设备 %s: %w", capture, err)
	}
	playbackUSB, err := cardUSBBusPort(root, playbackCard)
	if err != nil {
		return fmt.Errorf("播放设备 %s: %w", playback, err)
	}
	if captureUSB != usbPort {
		return fmt.Errorf("录音设备 %s 属于 USB %s，不是所选模块的 %s", capture, captureUSB, usbPort)
	}
	if playbackUSB != usbPort {
		return fmt.Errorf("播放设备 %s 属于 USB %s，不是所选模块的 %s", playback, playbackUSB, usbPort)
	}
	return nil
}

// bridgeAudio is the production cellular audio engine. Tests may replace the
// whole engine through Server.cellBridgeAudioFactory. A nil USB checker uses
// matchALSAUSB; production does not skip that check.
type bridgeAudio struct {
	server     *Server
	spec       cellBridgeAudioSpec
	probe      func(context.Context) error
	prepare    func(context.Context, qdc507.Config) (qdc507.Status, error)
	check      func(capture, playback, usbPort string) error
	usbLookup  func(context.Context) (string, string, error)
	ownerCheck func(*qdc507.Runtime) bool
	live       func(*qdc507.Runtime) bool

	prepareMu     sync.Mutex
	prepareCancel context.CancelFunc
	mu            sync.Mutex
	closed        bool
	runtime       *qdc507.Runtime
	owners        []*qdc507.Runtime
	alsa          *voice.Audio
	usb           string
	generation    string
	ready         bool
	reason        string
	verifiedAt    time.Time
}

func newBridgeAudio(server *Server, spec cellBridgeAudioSpec) *bridgeAudio {
	return &bridgeAudio{server: server, spec: spec}
}

func (audio *bridgeAudio) Status(ctx context.Context) (bool, string) {
	if err := audio.ensureRoute(ctx); err != nil {
		return false, err.Error()
	}
	audio.mu.Lock()
	defer audio.mu.Unlock()
	return audio.ready, audio.reason
}

func (audio *bridgeAudio) Prepare(ctx context.Context, _ string) error {
	return audio.ensureRoute(ctx)
}

func (audio *bridgeAudio) Start(ctx context.Context, callID string) error {
	if err := audio.ensureRoute(ctx); err != nil {
		return err
	}
	audio.mu.Lock()
	if audio.closed {
		audio.mu.Unlock()
		return errors.New("大疆音频已经关闭")
	}
	alsa := audio.alsa
	capture := audio.spec.CaptureDevice
	playback := audio.spec.PlaybackDevice
	audio.mu.Unlock()
	if alsa == nil {
		opened, err := voice.OpenALSA(capture, playback)
		if err != nil {
			return err
		}
		audio.mu.Lock()
		if audio.closed || ctx.Err() != nil {
			audio.mu.Unlock()
			_ = opened.Close()
			return errors.New("大疆音频已经关闭或取消")
		}
		if audio.alsa == nil {
			audio.alsa = opened
		} else {
			_ = opened.Close()
		}
		alsa = audio.alsa
		audio.mu.Unlock()
	}
	return alsa.Start(ctx, callID)
}

func (audio *bridgeAudio) ReadPCM(samples []int16) (int, error) {
	audio.mu.Lock()
	alsa := audio.alsa
	audio.mu.Unlock()
	if alsa == nil {
		return 0, errors.New("大疆音频尚未启动")
	}
	return alsa.ReadPCM(samples)
}

func (audio *bridgeAudio) WritePCM(samples []int16) error {
	audio.mu.Lock()
	alsa := audio.alsa
	audio.mu.Unlock()
	if alsa == nil {
		return errors.New("大疆音频尚未启动")
	}
	return alsa.WritePCM(samples)
}

func (audio *bridgeAudio) Stop(ctx context.Context) error {
	audio.mu.Lock()
	alsa := audio.alsa
	audio.mu.Unlock()
	if alsa == nil {
		return nil
	}
	return alsa.Stop(ctx)
}

func (audio *bridgeAudio) Close(ctx context.Context) error {
	// The caller context is often the process poll context and is already
	// cancelled when the bridge stops. Cleanup must still finish.
	_ = ctx
	audio.mu.Lock()
	audio.closed = true
	cancel := audio.prepareCancel
	generation := audio.generation
	deviceID := audio.spec.DeviceID
	alsa := audio.alsa
	audio.alsa = nil
	audio.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if alsa != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = alsa.Stop(stopCtx)
		stopCancel()
	}
	// Wait until an in-flight prepare has decided not to publish.
	audio.prepareMu.Lock()
	handles := audio.detachHandles()
	audio.prepareMu.Unlock()
	if alsa != nil {
		_ = alsa.Close()
	}
	current := ""
	if audio.server != nil {
		current = audio.server.usbGeneration(deviceID)
	}
	return releaseRuntimes(handles, shouldSignalRuntime(generation, current))
}

func (audio *bridgeAudio) ensureRoute(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	audio.prepareMu.Lock()
	defer audio.prepareMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	audio.mu.Lock()
	if audio.closed {
		audio.mu.Unlock()
		return errors.New("大疆音频已经关闭")
	}
	audio.mu.Unlock()

	usb, generation, err := audio.moduleUSB(ctx)
	if err != nil {
		audio.fail(err.Error())
		return err
	}
	audio.mu.Lock()
	same := audio.ready && audio.runtime != nil && audio.usb == usb && audio.generation == generation
	stale := audio.verifiedAt.IsZero() || time.Since(audio.verifiedAt) >= cellBridgeRouteVerifyInterval
	oldUSB := audio.usb
	oldGen := audio.generation
	had := audio.runtime != nil || len(audio.owners) > 0
	audio.mu.Unlock()
	if same && (!stale || audio.confirmLive()) {
		if stale {
			audio.mu.Lock()
			if !audio.closed && audio.ready {
				audio.verifiedAt = time.Now()
			}
			audio.mu.Unlock()
		}
		return nil
	}
	// The USB identity moved. Drop the old handles without signalling a
	// different generation, then prepare the module that is here now.
	if had && (oldUSB != usb || oldGen != generation) {
		audio.retire(shouldSignalRuntime(oldGen, generation))
	}
	if err := audio.usbCheck(usb); err != nil {
		audio.fail(err.Error())
		return err
	}
	probe := audio.probe
	if probe == nil {
		probe = voice.Probe
	}
	if err := probe(ctx); err != nil {
		audio.fail(err.Error())
		return err
	}
	cfg := qdc507.Config{
		OptIn:      true,
		Bootstrap:  audio.spec.Bootstrap,
		DeviceType: store.DeviceTypeDJI4G,
		USBPath:    usb,
		RuntimeDir: audio.spec.RuntimeDir,
		ADBPath:    audio.spec.ADBPath,
		ADBSocket:  audio.spec.ADBSocket,
	}
	prepare := audio.prepare
	if prepare == nil {
		prepare = qdc507.Prepare
	}
	prepCtx, cancel := context.WithCancel(ctx)
	audio.mu.Lock()
	if audio.closed {
		audio.mu.Unlock()
		cancel()
		return errors.New("大疆音频已经关闭")
	}
	audio.prepareCancel = cancel
	audio.mu.Unlock()
	status, prepareErr := prepare(prepCtx, cfg)
	cancel()
	audio.mu.Lock()
	audio.prepareCancel = nil
	closed := audio.closed
	audio.mu.Unlock()

	// A long prepare can outlive the module it started on. Recheck the USB
	// path, ALSA ownership and generation before anything is published.
	usbNow, genNow, idErr := audio.moduleUSB(context.Background())
	var alsaErr error
	if idErr == nil {
		alsaErr = audio.usbCheck(usbNow)
	}
	mismatch := idErr != nil || alsaErr != nil || usbNow != usb || genNow != generation
	if closed || mismatch || prepareErr != nil || !status.Ready || status.Runtime == nil {
		if status.Runtime != nil {
			_ = releaseRuntimes([]*qdc507.Runtime{status.Runtime}, idErr == nil && usbNow == usb && genNow == generation)
		}
		if !closed && mismatch && (had || oldGen != "" || oldUSB != "") {
			audio.retire(false)
		}
		if closed {
			return errors.New("大疆音频已经关闭")
		}
		reason := ""
		if idErr != nil {
			reason = idErr.Error()
		} else if alsaErr != nil {
			reason = alsaErr.Error()
		} else if status.Reason != "" {
			reason = status.Reason
		} else if prepareErr != nil {
			reason = prepareErr.Error()
		}
		if reason == "" {
			reason = "QDC507 语音路由未就绪"
		}
		audio.fail(reason)
		return errors.New(reason)
	}
	audio.adoptPrepared(status.Runtime, usbNow, genNow)
	audio.mu.Lock()
	closed = audio.closed
	audio.mu.Unlock()
	if closed {
		return errors.New("大疆音频已经关闭")
	}
	return nil
}

func (audio *bridgeAudio) confirmLive() bool {
	audio.mu.Lock()
	rt := audio.runtime
	hook := audio.live
	audio.mu.Unlock()
	if rt == nil {
		return false
	}
	if hook != nil {
		return hook(rt)
	}
	// These PIDs belong to the module, not the Halo host. A stale route
	// must go through Prepare's ADB checks before becoming ready again.
	return false
}

func (audio *bridgeAudio) runtimeIsOwner(rt *qdc507.Runtime) bool {
	if rt == nil {
		return false
	}
	if audio.ownerCheck != nil {
		return audio.ownerCheck(rt)
	}
	for _, proc := range rt.Processes() {
		if proc.StopOnClose {
			return true
		}
	}
	return false
}

// adoptPrepared keeps every handle that actually owns a calibration or route
// process. A later Prepare that only adopted the same processes must not
// replace that owner and then Close it. Handles with a new PID stay in the
// owner set so the final cleanup can still stop the previous calibration.
func (audio *bridgeAudio) adoptPrepared(rt *qdc507.Runtime, usb, generation string) {
	if rt == nil {
		return
	}
	owner := audio.runtimeIsOwner(rt)
	audio.mu.Lock()
	if audio.closed {
		audio.mu.Unlock()
		usbNow, genNow, err := audio.moduleUSB(context.Background())
		_ = releaseRuntimes([]*qdc507.Runtime{rt}, err == nil && usbNow == usb && genNow == generation)
		return
	}
	var extra *qdc507.Runtime
	if owner {
		audio.owners = append(audio.owners, rt)
		if audio.runtime != nil && audio.runtime != rt && !containsRuntime(audio.owners, audio.runtime) {
			extra = audio.runtime
		}
		audio.runtime = rt
	} else if len(audio.owners) > 0 {
		extra = rt
		if audio.runtime == nil || !containsRuntime(audio.owners, audio.runtime) {
			audio.runtime = audio.owners[len(audio.owners)-1]
		}
	} else {
		if audio.runtime != nil && audio.runtime != rt {
			extra = audio.runtime
		}
		audio.runtime = rt
	}
	audio.usb = usb
	audio.generation = generation
	audio.ready = true
	audio.reason = ""
	audio.verifiedAt = time.Now()
	audio.mu.Unlock()
	if extra != nil {
		_ = extra.Abandon()
	}
}

func (audio *bridgeAudio) retire(signal bool) {
	_ = releaseRuntimes(audio.detachHandles(), signal)
}

func (audio *bridgeAudio) detachHandles() []*qdc507.Runtime {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	handles := append([]*qdc507.Runtime{}, audio.owners...)
	if audio.runtime != nil {
		handles = append(handles, audio.runtime)
	}
	audio.owners = nil
	audio.runtime = nil
	audio.ready = false
	audio.verifiedAt = time.Time{}
	return uniqueRuntimes(handles)
}

func containsRuntime(handles []*qdc507.Runtime, rt *qdc507.Runtime) bool {
	for _, handle := range handles {
		if handle == rt {
			return true
		}
	}
	return false
}

func uniqueRuntimes(handles []*qdc507.Runtime) []*qdc507.Runtime {
	out := make([]*qdc507.Runtime, 0, len(handles))
	for _, rt := range handles {
		if rt == nil || containsRuntime(out, rt) {
			continue
		}
		out = append(out, rt)
	}
	return out
}

func releaseRuntimes(handles []*qdc507.Runtime, signal bool) error {
	if len(handles) == 0 {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	for _, rt := range handles {
		var releaseErr error
		if signal {
			releaseErr = rt.Close(cleanupCtx)
		} else {
			releaseErr = rt.Abandon()
		}
		if releaseErr != nil && err == nil {
			err = releaseErr
		}
	}
	return err
}

func (audio *bridgeAudio) fail(reason string) {
	audio.mu.Lock()
	audio.ready = false
	audio.reason = reason
	audio.mu.Unlock()
}

func (audio *bridgeAudio) moduleUSB(ctx context.Context) (string, string, error) {
	if audio.usbLookup != nil {
		return audio.usbLookup(ctx)
	}
	if audio.server == nil || audio.server.store == nil {
		return "", "", errors.New("大疆模块配置不可用")
	}
	device, err := audio.server.store.Device(ctx, audio.spec.DeviceID)
	if err != nil {
		return "", "", errors.New("大疆模块配置不可用")
	}
	if device.DeviceType != store.DeviceTypeDJI4G || isReaderDevice(device) {
		return "", "", errors.New("所选设备不是大疆蜂窝模块")
	}
	entry, _, present := audio.server.physicalForConfig(device)
	if !present {
		return "", "", errors.New("大疆模块不在线")
	}
	// The stored USB path is editable configuration. Only the discovered
	// candidate path may select the module that receives audio.
	raw := strings.TrimSpace(entry.Candidate.USBPath)
	if raw == "" {
		return "", "", errors.New("模块 USB 路径为空")
	}
	usb, err := canonicalUSBBusPort(raw)
	if err != nil {
		return "", "", err
	}
	return usb, entry.Candidate.USBGeneration, nil
}

func (audio *bridgeAudio) usbCheck(usb string) error {
	check := audio.check
	if check == nil && audio.server != nil && audio.server.cellBridgeALSACheck != nil {
		check = audio.server.cellBridgeALSACheck
	}
	if check == nil {
		root := ""
		if audio.server != nil {
			root = audio.server.cellBridgeALSARoot
		}
		return matchALSAUSB(root, audio.spec.CaptureDevice, audio.spec.PlaybackDevice, usb)
	}
	return check(audio.spec.CaptureDevice, audio.spec.PlaybackDevice, usb)
}

func (s *Server) checkALSAUSB(capture, playback, usbPort string) error {
	if s != nil && s.cellBridgeALSACheck != nil {
		return s.cellBridgeALSACheck(capture, playback, usbPort)
	}
	root := ""
	if s != nil {
		root = s.cellBridgeALSARoot
	}
	return matchALSAUSB(root, capture, playback, usbPort)
}

func (s *Server) usbGeneration(deviceID string) string {
	if s == nil || s.store == nil || strings.TrimSpace(deviceID) == "" || s.devices == nil {
		return ""
	}
	device, err := s.store.Device(context.Background(), deviceID)
	if err != nil {
		return ""
	}
	entry, _, present := s.physicalForConfig(device)
	if !present {
		return ""
	}
	return entry.Candidate.USBGeneration
}

func (s *Server) newCellAudio(spec cellBridgeAudioSpec) cellularAudio {
	if s.cellBridgeAudioFactory != nil {
		if audio := s.cellBridgeAudioFactory(spec); audio != nil {
			return audio
		}
	}
	return newBridgeAudio(s, spec)
}
