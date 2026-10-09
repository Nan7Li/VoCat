package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"vocat/internal/cellbridge/cellular"
	"vocat/internal/cellbridge/sip"
	"vocat/internal/store"
)

const cellBridgePollInterval = 400 * time.Millisecond

// cellBridgeLine is the cellular controller bound to one device generation.
type cellBridgeLine struct {
	controller    *cellular.Controller
	audio         cellularAudio
	deviceID      string
	physicalID    string
	iccid         string
	usbGeneration string
}

// cellBridgeSIP is one SIP listener and the backend it was created with.
// done is closed when Run returns, including a listener that died on its own.
type cellBridgeSIP struct {
	server  *sip.Server
	backend sip.Backend
	cancel  context.CancelFunc
	done    <-chan struct{}
}

func (current cellBridgeSIP) listenerStopped() bool {
	if current.done == nil {
		return false
	}
	select {
	case <-current.done:
		return true
	default:
		return false
	}
}

// RunCellBridge applies the saved configuration and keeps the bridge alive
// until the process poll context ends. An HTTP request context never owns
// the SIP socket or a cellular call.
func (s *Server) RunCellBridge(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.wakeCellBridge()
	defer s.stopCellBridge()
	ticker := time.NewTicker(cellBridgePollInterval)
	defer ticker.Stop()
	refresh := time.NewTicker(15 * time.Second)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.cellBridgeWakeChan():
			s.applyCellBridge(ctx)
		case <-ticker.C:
			s.pollCellBridge(ctx)
		case <-refresh.C:
			s.refreshCellBridge(ctx)
		}
	}
}

func (s *Server) cellBridgeWakeChan() <-chan struct{} {
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	if s.cellBridgeWake == nil {
		s.cellBridgeWake = make(chan struct{}, 1)
	}
	return s.cellBridgeWake
}

func (s *Server) wakeCellBridge() {
	s.cellBridgeMu.Lock()
	if s.cellBridgeWake == nil {
		s.cellBridgeWake = make(chan struct{}, 1)
	}
	s.cellBridgeDirty = true
	wake := s.cellBridgeWake
	s.cellBridgeMu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Server) noteCellBridgeApplying(config cellBridgeConfig) {
	if !config.SIPEnabled && !config.CellularEnabled {
		return
	}
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	if s.cellBridgeStatus.Phase == "ready" || s.cellBridgeStatus.Phase == "failed" {
		s.cellBridgeStatus.Phase = "applying"
		s.cellBridgeStatus.Reason = ""
	} else if s.cellBridgeStatus.Phase == "" || s.cellBridgeStatus.Phase == "disabled" {
		s.cellBridgeStatus.Phase = "applying"
		s.cellBridgeStatus.Reason = ""
	}
}

func (s *Server) cellBridgeStatusSnapshot() cellBridgeStatus {
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	status := s.cellBridgeStatus
	if status.Phase == "" {
		status.Phase = "disabled"
	}
	return status
}

func (s *Server) applyCellBridge(ctx context.Context) {
	s.cellBridgeMu.Lock()
	if s.cellBridgeApplying {
		s.cellBridgeDirty = true
		s.cellBridgeMu.Unlock()
		return
	}
	s.cellBridgeApplying = true
	s.cellBridgeDirty = false
	s.cellBridgeMu.Unlock()
	defer func() {
		s.cellBridgeMu.Lock()
		dirty := s.cellBridgeDirty
		s.cellBridgeApplying = false
		s.cellBridgeDirty = false
		s.cellBridgeMu.Unlock()
		if dirty {
			s.wakeCellBridge()
		}
	}()

	config := s.loadCellBridgeConfig(ctx)
	s.reconcileCellBridge(ctx, config)
}

func (s *Server) reconcileCellBridge(ctx context.Context, config cellBridgeConfig) {
	s.cellBridgeMu.Lock()
	currentLine := s.cellBridgeCtrl
	currentSIP := s.cellBridgeSIP
	s.cellBridgeMu.Unlock()

	needCellular := config.CellularEnabled && config.CellularDeviceID != ""
	cellularSame := needCellular && s.cellularLineCurrent(ctx, currentLine, config)
	if currentLine != nil && !cellularSame {
		s.stopCellularLine(currentLine)
		currentLine = nil
	}
	var cellularReason string
	cellularReady := false
	if needCellular {
		if currentLine == nil {
			currentLine, cellularReason = s.startCellularLine(ctx, config)
		}
		if currentLine != nil {
			cellularReady, cellularReason = s.cellularLineReady(ctx, currentLine)
		} else if cellularReason == "" {
			cellularReason = "大疆通话音频未能启动"
		}
	}

	var sipErr string
	sipStale := currentSIP.server != nil && (!config.SIPEnabled || !sipConfigMatches(currentSIP, config) || currentSIP.listenerStopped() || s.sipBackendStale(currentSIP, config, currentLine))
	if sipStale {
		s.stopSIP(currentSIP)
		currentSIP = cellBridgeSIP{}
	}
	if config.SIPEnabled && currentSIP.server == nil {
		backend := s.sipBackend(config, currentLine)
		started, err := s.startSIP(ctx, config, backend)
		if err != nil {
			sipErr = err.Error()
		} else {
			currentSIP = started
		}
	}

	s.cellBridgeMu.Lock()
	s.cellBridgeCtrl = currentLine
	s.cellBridgeSIP = currentSIP
	s.cellBridgeStatus = buildCellBridgeStatus(config, currentSIP, sipErr, cellularReady, cellularReason)
	s.cellBridgeMu.Unlock()
}

func (s *Server) cellularLineCurrent(ctx context.Context, line *cellBridgeLine, config cellBridgeConfig) bool {
	if line == nil || line.deviceID != config.CellularDeviceID || !audioSpecMatches(line, config) {
		return false
	}
	device, err := s.store.Device(ctx, config.CellularDeviceID)
	if err != nil || device.VoWiFiEnabled || isReaderDevice(device) || device.DeviceType != store.DeviceTypeDJI4G {
		return false
	}
	entry, physicalID, present := s.physicalForConfig(device)
	if !present {
		return false
	}
	if line.physicalID != "" && physicalID != "" && line.physicalID != physicalID {
		return false
	}
	// Known generation to empty, a new generation, or a generation that
	// appeared after the line was built all need a new controller. Keeping
	// the first gate pin would reject every future call.
	if line.usbGeneration != entry.Candidate.USBGeneration {
		return false
	}
	currentICCID := ""
	if entry.Snapshot != nil {
		currentICCID = strings.TrimSpace(entry.Snapshot.ICCID)
	}
	if line.iccid != "" && currentICCID != line.iccid {
		return false
	}
	return true
}

func audioSpecMatches(line *cellBridgeLine, config cellBridgeConfig) bool {
	if line == nil || line.audio == nil {
		return false
	}
	spec, ok := cellAudioSpec(line.audio)
	if !ok {
		return line.deviceID == config.CellularDeviceID
	}
	return spec.DeviceID == config.CellularDeviceID &&
		spec.CaptureDevice == config.CaptureDevice &&
		spec.PlaybackDevice == config.PlaybackDevice &&
		spec.RuntimeDir == config.RuntimeDir &&
		spec.ADBPath == config.ADBPath &&
		spec.ADBSocket == config.ADBSocket &&
		spec.Bootstrap == config.Bootstrap
}

func cellAudioSpec(audio cellularAudio) (cellBridgeAudioSpec, bool) {
	switch engine := audio.(type) {
	case *bridgeAudio:
		return engine.spec, true
	case interface{ CellBridgeSpec() cellBridgeAudioSpec }:
		return engine.CellBridgeSpec(), true
	default:
		return cellBridgeAudioSpec{}, false
	}
}

func (s *Server) startCellularLine(ctx context.Context, config cellBridgeConfig) (*cellBridgeLine, string) {
	device, err := s.store.Device(ctx, config.CellularDeviceID)
	if err != nil {
		return nil, "大疆模块配置不可用"
	}
	if isReaderDevice(device) || device.DeviceType != store.DeviceTypeDJI4G {
		return nil, "大疆通话音频只能用于大疆模块"
	}
	if device.VoWiFiEnabled {
		return nil, "该模块已启用 VoWiFi，不会因为 IMS 未就绪改用蜂窝音频"
	}
	entry, physicalID, present := s.physicalForConfig(device)
	if !present || physicalID == "" {
		return nil, "大疆模块不在线"
	}
	generation := entry.Candidate.USBGeneration
	iccid := ""
	if entry.Snapshot != nil {
		iccid = strings.TrimSpace(entry.Snapshot.ICCID)
	}
	spec := cellBridgeAudioSpec{
		DeviceID:       config.CellularDeviceID,
		CaptureDevice:  config.CaptureDevice,
		PlaybackDevice: config.PlaybackDevice,
		RuntimeDir:     config.RuntimeDir,
		ADBPath:        config.ADBPath,
		ADBSocket:      config.ADBSocket,
		Bootstrap:      config.Bootstrap,
	}
	audio := s.newCellAudio(spec)
	gate := &cellBridgeGate{server: s, deviceID: config.CellularDeviceID, usbGeneration: generation}
	controller := &cellular.Controller{
		DeviceID:      config.CellularDeviceID,
		Gate:          gate,
		AT:            s.devices,
		Audio:         audio,
		Saver:         cellBridgeSaver{server: s, deviceID: config.CellularDeviceID},
		RecordingsDir: s.recordingsDir,
		OnIncoming: func(number string) {
			s.notifyCellularIncoming(config.CellularDeviceID, number)
		},
	}
	line := &cellBridgeLine{
		controller:    controller,
		audio:         audio,
		deviceID:      config.CellularDeviceID,
		physicalID:    physicalID,
		iccid:         iccid,
		usbGeneration: generation,
	}
	ready, reason := s.cellularLineReady(ctx, line)
	if !ready && reason == "" {
		reason = "大疆通话音频未就绪"
	}
	if !ready {
		return line, reason
	}
	return line, ""
}

func (s *Server) cellularLineReady(ctx context.Context, line *cellBridgeLine) (bool, string) {
	if line == nil || line.audio == nil {
		return false, "大疆通话音频未配置"
	}
	if reporter, ok := line.audio.(interface {
		Status(context.Context) (bool, string)
	}); ok {
		return reporter.Status(ctx)
	}
	return false, "音频引擎没有报告就绪状态"
}

func (s *Server) stopCellularLine(line *cellBridgeLine) {
	if line == nil {
		return
	}
	if line.controller != nil {
		for _, call := range line.controller.Calls() {
			if call.EndedAt != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = line.controller.Hangup(ctx, call.ID)
			cancel()
		}
	}
	if closer, ok := line.audio.(interface{ Close(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = closer.Close(ctx)
		cancel()
	}
	s.dropDeviceBindings(line.deviceID, "cellular")
}

func sipConfigMatches(current cellBridgeSIP, config cellBridgeConfig) bool {
	if current.server == nil || !config.SIPEnabled {
		return false
	}
	options, ok := current.backend.(interface{ SIPOptions() sip.Options })
	if !ok {
		return false
	}
	opts := options.SIPOptions()
	return opts.ListenAddr == config.ListenAddr &&
		opts.AdvertisedIP == config.AdvertisedIP &&
		opts.RTPListenAddr == config.RTPListenAddr &&
		opts.Username == config.Username &&
		opts.Password == config.Password
}

func (s *Server) startSIP(ctx context.Context, config cellBridgeConfig, backend sip.Backend) (cellBridgeSIP, error) {
	if backend == nil {
		return cellBridgeSIP{}, errors.New("SIP 线路不可用")
	}
	server, err := sip.New(sip.Options{
		ListenAddr:    config.ListenAddr,
		AdvertisedIP:  config.AdvertisedIP,
		Username:      config.Username,
		Password:      config.Password,
		RTPListenAddr: config.RTPListenAddr,
		Backend:       backend,
	})
	if err != nil {
		return cellBridgeSIP{}, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Run(runCtx)
	}()
	return cellBridgeSIP{server: server, backend: backend, cancel: cancel, done: done}, nil
}

func (s *Server) stopSIP(current cellBridgeSIP) {
	if current.cancel != nil {
		current.cancel()
	}
	if current.server != nil {
		_ = current.server.Close()
	}
}

func (s *Server) stopCellBridge() {
	s.cellBridgeMu.Lock()
	line := s.cellBridgeCtrl
	sipServer := s.cellBridgeSIP
	s.cellBridgeCtrl = nil
	s.cellBridgeSIP = cellBridgeSIP{}
	s.cellBridgeStatus = cellBridgeStatus{Phase: "disabled"}
	s.cellBridgeMu.Unlock()
	s.stopSIP(sipServer)
	s.stopCellularLine(line)
}

func (s *Server) pollCellBridge(ctx context.Context) {
	s.cellBridgeMu.Lock()
	line := s.cellBridgeCtrl
	s.cellBridgeMu.Unlock()
	if line != nil && line.controller != nil {
		pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := line.controller.Poll(pollCtx)
		cancel()
		if err != nil && s.logger != nil && ctx.Err() == nil {
			s.logger.Debug("cellular call poll failed", "device_id", line.deviceID, "error", err)
		}
		s.syncCellularBindings(line)
	}
	s.wakeCellBridgeIfDrifted(ctx, line)
}

// wakeCellBridgeIfDrifted asks the reconcile loop to rebuild a line or SIP
// listener whose device, generation, or socket no longer matches. A missing
// line is left for the periodic refresh so an offline module does not restart
// on every poll.
func (s *Server) wakeCellBridgeIfDrifted(ctx context.Context, line *cellBridgeLine) {
	s.cellBridgeMu.Lock()
	sipState := s.cellBridgeSIP
	s.cellBridgeMu.Unlock()
	config := s.loadCellBridgeConfig(ctx)
	if line != nil && !s.cellularLineCurrent(ctx, line, config) {
		s.wakeCellBridge()
		return
	}
	if config.SIPEnabled && sipState.server != nil && (sipState.listenerStopped() || s.sipBackendStale(sipState, config, line)) {
		s.wakeCellBridge()
	}
}

func (s *Server) refreshCellBridge(ctx context.Context) {
	// Refresh re-reads the saved config and the live device identity. It is
	// not a cached audio Status: a hot-plug, a failed SIP socket, or a reader
	// that came back must rebuild the line or listener.
	s.applyCellBridge(ctx)
}

func buildCellBridgeStatus(config cellBridgeConfig, sipServer cellBridgeSIP, sipErr string, cellularReady bool, cellularReason string) cellBridgeStatus {
	status := cellBridgeStatus{Phase: "disabled"}
	if sipServer.server != nil {
		live := sipServer.server.Status()
		status.SIP.Running = true
		status.SIP.Registered = live.Registered
		status.SIP.ListenAddr = live.ListenAddr
		status.SIP.RTPListenAddr = firstNonEmpty(live.LocalRTPAddr, config.RTPListenAddr)
		status.SIP.AdvertisedIP = config.AdvertisedIP
		status.SIP.Username = config.Username
	}
	if sipErr != "" {
		status.SIP.Reason = sipErr
		status.SIP.Running = false
	}
	if config.CellularEnabled {
		status.Cellular.DeviceID = config.CellularDeviceID
		status.Cellular.Ready = cellularReady
		status.Cellular.Reason = cellularReason
	}
	switch {
	case !config.SIPEnabled && !config.CellularEnabled:
		status.Phase = "disabled"
	case (config.SIPEnabled && sipErr != "") || (config.CellularEnabled && !cellularReady):
		status.Phase = "failed"
		reasons := make([]string, 0, 2)
		if sipErr != "" {
			reasons = append(reasons, sipErr)
		}
		if config.CellularEnabled && !cellularReady && cellularReason != "" {
			reasons = append(reasons, cellularReason)
		}
		status.Reason = strings.Join(reasons, "；")
	default:
		status.Phase = "ready"
	}
	return status
}

func (s *Server) cellularControllerFor(deviceID string) *cellular.Controller {
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	if s.cellBridgeCtrl == nil || s.cellBridgeCtrl.deviceID != deviceID {
		return nil
	}
	return s.cellBridgeCtrl.controller
}

func (s *Server) notifyCellularIncoming(deviceID, number string) {
	if s.store == nil {
		return
	}
	name := deviceID
	if config, err := s.store.Device(context.Background(), deviceID); err == nil && strings.TrimSpace(config.Name) != "" {
		name = config.Name
	}
	s.NotifyIncomingCall(context.Background(), IncomingCallNotification{
		DeviceID:    deviceID,
		DeviceName:  name,
		DeviceLabel: name,
		Caller:      number,
		Time:        time.Now().UTC(),
		Environment: "cellular",
	})
}

func (s *Server) sipBackend(config cellBridgeConfig, line *cellBridgeLine) sip.Backend {
	device, err := s.store.Device(context.Background(), config.SIPDeviceID)
	if err != nil {
		return nil
	}
	if line != nil && line.deviceID == config.SIPDeviceID && line.controller != nil && !device.VoWiFiEnabled && !isReaderDevice(device) {
		return &cellularSIPBackend{server: s, deviceID: config.SIPDeviceID, controller: line.controller, options: sipOptions(config)}
	}
	return &vowifiSIPBackend{server: s, deviceID: config.SIPDeviceID, options: sipOptions(config)}
}

func sipOptions(config cellBridgeConfig) sip.Options {
	return sip.Options{
		ListenAddr:    config.ListenAddr,
		AdvertisedIP:  config.AdvertisedIP,
		Username:      config.Username,
		Password:      config.Password,
		RTPListenAddr: config.RTPListenAddr,
	}
}
