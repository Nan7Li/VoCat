package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"vocat/internal/cellbridge/cellular"
	"vocat/internal/cellbridge/sip"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

// callBinding pins a call to the backend that created it. Later IMS or
// configuration changes must not move answer, hangup, or media onto another
// transport, and must not send ATA/ATH to a different modem.
type callBinding struct {
	deviceID      string
	callID        string
	transport     string
	physicalID    string
	iccid         string
	usbGeneration string
	ended         bool
	at            time.Time
}

func callBindingKey(deviceID, callID string) string {
	return deviceID + "\n" + callID
}

func (s *Server) rememberCall(binding callBinding) {
	if binding.deviceID == "" || binding.callID == "" || binding.transport == "" {
		return
	}
	if binding.at.IsZero() {
		binding.at = time.Now()
	}
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	if s.cellBridgeBindings == nil {
		s.cellBridgeBindings = make(map[string]callBinding)
	}
	key := callBindingKey(binding.deviceID, binding.callID)
	if existing, ok := s.cellBridgeBindings[key]; ok {
		// A call keeps the transport and the USB generation captured when it
		// was created. Later IMS readiness must not turn it into an AT call,
		// and a partial update must not forget the generation.
		if existing.transport != "" && binding.transport != existing.transport {
			binding.transport = existing.transport
		}
		if existing.usbGeneration != "" {
			binding.usbGeneration = existing.usbGeneration
		}
		if existing.physicalID != "" {
			binding.physicalID = existing.physicalID
		}
		if existing.iccid != "" {
			binding.iccid = existing.iccid
		}
	}
	s.cellBridgeBindings[key] = binding
}

func (s *Server) dropDeviceBindings(deviceID, transport string) {
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	for key, binding := range s.cellBridgeBindings {
		if binding.deviceID != deviceID {
			continue
		}
		if transport != "" && binding.transport != transport {
			continue
		}
		binding.ended = true
		s.cellBridgeBindings[key] = binding
	}
}

func (s *Server) bindingForAction(deviceID, callID string) (callBinding, bool) {
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	if callID != "" {
		binding, ok := s.cellBridgeBindings[callBindingKey(deviceID, callID)]
		return binding, ok
	}
	var latest callBinding
	found := false
	for _, binding := range s.cellBridgeBindings {
		if binding.deviceID != deviceID || binding.ended {
			continue
		}
		if !found || binding.at.After(latest.at) {
			latest = binding
			found = true
		}
	}
	return latest, found
}

func (s *Server) syncCellularBindings(line *cellBridgeLine) {
	if line == nil || line.controller == nil {
		return
	}
	for _, call := range line.controller.Calls() {
		s.rememberCall(callBinding{
			deviceID:      line.deviceID,
			callID:        call.ID,
			transport:     "cellular",
			physicalID:    line.physicalID,
			iccid:         line.iccid,
			usbGeneration: line.usbGeneration,
			ended:         call.EndedAt != nil,
		})
	}
}

func (s *Server) explicitCellularTarget(config store.Device) bool {
	if config.DeviceType != store.DeviceTypeDJI4G || config.VoWiFiEnabled || isReaderDevice(config) {
		return false
	}
	settings := s.loadCellBridgeConfig(context.Background())
	return settings.CellularEnabled && settings.CellularDeviceID == config.ID
}

func (s *Server) imsReady(deviceID string) bool {
	if s.vowifi == nil || deviceID == "" {
		return false
	}
	state, err := s.vowifi.State(deviceID)
	return err == nil && state.IMSReady
}

// routeNewCall chooses a transport for a call that does not exist yet.
// An existing call keeps the binding captured at creation.
func (s *Server) routeNewCall(config store.Device) (string, string) {
	if isReaderDevice(config) {
		if s.imsReady(config.ID) {
			return "vowifi", ""
		}
		return "unavailable", "SIM 读卡器需要 IMS 就绪后才能通话，不会改用蜂窝 AT"
	}
	if s.explicitCellularTarget(config) {
		return "cellular", ""
	}
	if config.VoWiFiEnabled {
		if s.imsReady(config.ID) {
			return "vowifi", ""
		}
		return "unavailable", "VoWiFi 已启用，IMS 尚未就绪，不会改用蜂窝通话"
	}
	if s.imsReady(config.ID) {
		return "vowifi", ""
	}
	return "legacy", ""
}

func (s *Server) callAudioCapability(config store.Device) (bool, string, string) {
	if s.explicitCellularTarget(config) {
		status := s.cellBridgeStatusSnapshot()
		if status.Cellular.DeviceID != config.ID || !status.Cellular.Ready {
			reason := status.Cellular.Reason
			if reason == "" {
				reason = "大疆通话音频未就绪"
			}
			return false, reason, "cellular"
		}
		return true, "", "cellular"
	}
	if isReaderDevice(config) || config.VoWiFiEnabled {
		if s.imsReady(config.ID) {
			return true, "", "vowifi"
		}
		return false, "IMS 尚未就绪", "vowifi"
	}
	return false, "", ""
}

func (s *Server) serveCellBridgeCallList(w http.ResponseWriter, config store.Device) bool {
	if controller := s.cellularControllerFor(config.ID); controller != nil && s.explicitCellularTarget(config) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"device_id": config.ID,
			"transport": "cellular",
			"calls":     controller.Calls(),
		}})
		return true
	}
	route, reason := s.routeNewCall(config)
	switch route {
	case "cellular":
		if reason == "" {
			reason = "大疆通话音频未就绪"
		}
		writeError(w, http.StatusConflict, "call_unavailable", reason)
		return true
	case "unavailable":
		writeError(w, http.StatusConflict, "call_unavailable", reason)
		return true
	default:
		return false
	}
}

func (s *Server) dispatchBoundCallAction(w http.ResponseWriter, r *http.Request, config store.Device, physicalID, action, number, callID string, duration time.Duration) bool {
	if action == "dial" {
		route, reason := s.routeNewCall(config)
		switch route {
		case "legacy", "vowifi":
			return false
		case "cellular":
			s.serveCellularDial(w, r, config, number, duration)
			return true
		default:
			writeError(w, http.StatusConflict, "call_unavailable", reason)
			return true
		}
	}
	if binding, ok := s.bindingForAction(config.ID, callID); ok {
		switch binding.transport {
		case "cellular":
			s.serveCellularAction(w, config, action, callID)
			return true
		case "vowifi":
			s.serveBoundVoWiFiAction(w, config, action, callID)
			return true
		case "legacy":
			s.serveLegacyBoundAction(w, r, config, binding, action, number)
			return true
		}
	}
	if s.cellularHasCall(config.ID, callID) {
		s.serveCellularAction(w, config, action, callID)
		return true
	}
	return false
}

func (s *Server) cellularHasCall(deviceID, callID string) bool {
	controller := s.cellularControllerFor(deviceID)
	if controller == nil {
		return false
	}
	for _, call := range controller.Calls() {
		if callID == "" {
			if call.EndedAt == nil {
				return true
			}
			continue
		}
		if call.ID == callID {
			return true
		}
	}
	return false
}

func (s *Server) cellularDialAllowed(deviceID string) bool {
	config := s.loadCellBridgeConfig(context.Background())
	return config.CellularEnabled && config.CellularDeviceID == deviceID
}

func (s *Server) sipDialAllowed(deviceID string) bool {
	config := s.loadCellBridgeConfig(context.Background())
	return config.SIPEnabled && config.SIPDeviceID == deviceID
}

func (s *Server) rememberCellular(deviceID, callID string, ended bool) {
	physicalID, iccid, generation := s.lineIdentity(deviceID)
	s.rememberCall(callBinding{
		deviceID: deviceID, callID: callID, transport: "cellular",
		physicalID: physicalID, iccid: iccid, usbGeneration: generation, ended: ended,
	})
}

func (s *Server) lineIdentity(deviceID string) (physicalID, iccid, generation string) {
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	if s.cellBridgeCtrl == nil || s.cellBridgeCtrl.deviceID != deviceID {
		return "", "", ""
	}
	line := s.cellBridgeCtrl
	return line.physicalID, line.iccid, line.usbGeneration
}

func (s *Server) serveCellularDial(w http.ResponseWriter, r *http.Request, config store.Device, number string, duration time.Duration) {
	if !s.cellularDialAllowed(config.ID) {
		writeError(w, http.StatusConflict, "call_unavailable", "蜂窝通话已关闭")
		return
	}
	controller := s.cellularControllerFor(config.ID)
	if controller == nil {
		writeError(w, http.StatusConflict, "call_unavailable", "大疆通话音频未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	call, err := controller.Dial(ctx, number, true)
	if err != nil {
		writeError(w, http.StatusConflict, "call_unavailable", err.Error())
		return
	}
	physicalID, iccid, generation := s.lineIdentity(config.ID)
	s.rememberCall(callBinding{
		deviceID: config.ID, callID: call.ID, transport: "cellular",
		physicalID: physicalID, iccid: iccid, usbGeneration: generation,
	})
	if duration > 0 {
		go s.hangupCellularAfter(config.ID, call.ID, duration)
	}
	s.recordAudit(r.Context(), "admin", "call.dial", "device", config.ID, "success", "cellular")
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{
		"accepted": true, "action": "dial", "number": number, "call_id": call.ID,
		"duration_seconds": int(duration / time.Second), "transport": "cellular", "call": call,
	}})
}

func (s *Server) serveCellularAction(w http.ResponseWriter, config store.Device, action, callID string) {
	controller := s.cellularControllerFor(config.ID)
	if controller == nil {
		writeError(w, http.StatusConflict, "call_unavailable", "原蜂窝通话已经结束")
		return
	}
	if binding, ok := s.bindingForAction(config.ID, callID); ok && binding.transport == "cellular" && binding.ended {
		writeError(w, http.StatusConflict, "call_unavailable", "原蜂窝通话已经结束")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result any
	var err error
	switch action {
	case "answer":
		callID, err = resolveCellularCallID(controller, callID)
		if err == nil {
			result, err = controller.Answer(ctx, callID)
		}
	case "hangup":
		callID, err = resolveCellularCallID(controller, callID)
		if err == nil {
			err = controller.Hangup(ctx, callID)
		}
	default:
		writeError(w, http.StatusNotFound, "not_found", "call action not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "call_unavailable", err.Error())
		return
	}
	if action == "hangup" && callID != "" {
		if binding, ok := s.bindingForAction(config.ID, callID); ok {
			binding.ended = true
			s.rememberCall(binding)
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{
		"accepted": true, "action": action, "call_id": callID, "transport": "cellular", "call": result,
	}})
}

func resolveCellularCallID(controller *cellular.Controller, callID string) (string, error) {
	if callID != "" {
		return callID, nil
	}
	for _, call := range controller.Calls() {
		if call.EndedAt == nil {
			return call.ID, nil
		}
	}
	return "", errors.New("no cellular call")
}

func (s *Server) hangupCellularAfter(deviceID, callID string, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	<-timer.C
	controller := s.cellularControllerFor(deviceID)
	if controller == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = controller.Hangup(ctx, callID)
}

func (s *Server) serveBoundVoWiFiAction(w http.ResponseWriter, config store.Device, action, callID string) {
	controller, ok := s.vowifi.(VoWiFiCallController)
	if !ok {
		writeError(w, http.StatusConflict, "call_unavailable", "原 IMS 通话已经结束")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result any
	var err error
	switch action {
	case "answer":
		callID, err = resolveVoWiFiCallID(controller, config.ID, callID, "ringing")
		if err == nil {
			result, err = controller.AnswerCall(ctx, config.ID, callID)
		}
	case "hangup":
		callID, err = resolveVoWiFiCallID(controller, config.ID, callID, "")
		if err == nil {
			err = controller.HangupCall(ctx, config.ID, callID)
		}
	default:
		writeError(w, http.StatusNotFound, "not_found", "call action not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "vowifi_call_failed", err.Error())
		return
	}
	if callID != "" {
		s.rememberCall(callBinding{deviceID: config.ID, callID: callID, transport: "vowifi"})
	}
	if calls, listErr := controller.Calls(config.ID); listErr == nil {
		for _, call := range calls {
			s.upsertCallRecord(ctx, config.ID, call)
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{
		"accepted": true, "action": action, "call_id": callID, "transport": "vowifi", "call": result,
	}})
}

func (s *Server) serveLegacyBoundAction(w http.ResponseWriter, r *http.Request, config store.Device, binding callBinding, action, number string) {
	if binding.ended {
		writeError(w, http.StatusConflict, "call_unavailable", "原蜂窝通话已经结束")
		return
	}
	if binding.transport == "vowifi" {
		writeError(w, http.StatusConflict, "call_unavailable", "原 IMS 通话不会改用蜂窝 AT")
		return
	}
	// The physical id and ICCID can be reused by the next module on this
	// port. A captured generation that is empty or different now must not
	// receive ATA or ATH.
	if binding.usbGeneration != "" && s.usbGeneration(config.ID) != binding.usbGeneration {
		binding.ended = true
		s.rememberCall(binding)
		writeError(w, http.StatusConflict, "call_unavailable", "USB 设备已更换，已停止对原模块的呼叫")
		return
	}
	command := "ATA"
	if action == "hangup" {
		command = "ATH"
	}
	target := binding.physicalID
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	response, err := s.devices.ExecuteAT(ctx, target, command)
	cancel()
	if err != nil {
		s.writeDeviceError(w, err)
		return
	}
	if !response.OK() {
		writeError(w, http.StatusBadGateway, "call_rejected", "modem did not accept the call action")
		return
	}
	if action == "hangup" {
		s.noteLegacyHangup(config.ID, target)
		binding.ended = true
		s.rememberCall(binding)
	} else if action == "answer" {
		s.noteLegacyAccepted(binding.usbGeneration, config.ID, target)
	}
	s.upsertCellularCallRecord(r.Context(), config.ID, number, action)
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{
		"accepted": true, "action": action, "call_id": binding.callID, "transport": "legacy",
	}})
}

func (s *Server) sipBackendStale(current cellBridgeSIP, config cellBridgeConfig, line *cellBridgeLine) bool {
	if current.listenerStopped() {
		return true
	}
	device, err := s.store.Device(context.Background(), config.SIPDeviceID)
	if err != nil {
		return true
	}
	wantsCellular := line != nil && line.controller != nil && line.deviceID == config.SIPDeviceID && !device.VoWiFiEnabled && !isReaderDevice(device)
	switch backend := current.backend.(type) {
	case *cellularSIPBackend:
		if backend.deviceID != config.SIPDeviceID || backend.controller == nil || line == nil {
			return true
		}
		return !wantsCellular || backend.controller != line.controller
	case *vowifiSIPBackend:
		return backend.deviceID != config.SIPDeviceID || wantsCellular
	default:
		return true
	}
}

// cellularSIPBackend is the SIP line captured for one cellular controller.
// It does not follow later IMS readiness or a replaced device.
type cellularSIPBackend struct {
	server     *Server
	deviceID   string
	controller *cellular.Controller
	options    sip.Options
}

func (backend *cellularSIPBackend) SIPOptions() sip.Options { return backend.options }

func (backend *cellularSIPBackend) Calls(context.Context) ([]vowifi.Call, error) {
	if backend.controller == nil {
		return nil, errors.New("原蜂窝通话已经结束")
	}
	calls := backend.controller.Calls()
	for _, call := range calls {
		backend.server.rememberCellular(backend.deviceID, call.ID, call.EndedAt != nil)
	}
	return calls, nil
}

func (backend *cellularSIPBackend) Dial(ctx context.Context, number string) (vowifi.Call, error) {
	if backend.server == nil || backend.controller == nil {
		return vowifi.Call{}, errors.New("大疆通话音频未就绪")
	}
	release, admitErr := backend.server.admitCallMutation()
	if admitErr != nil {
		return vowifi.Call{}, admitErr
	}
	defer release()
	// The saved switches are checked here so a listener that has not yet been
	// restarted still refuses a new call after SIP or cellular was disabled.
	// Hangup keeps using this controller and does not consult the new switches.
	if !backend.server.sipDialAllowed(backend.deviceID) {
		return vowifi.Call{}, errors.New("SIP 线路已关闭")
	}
	if !backend.server.cellularDialAllowed(backend.deviceID) {
		return vowifi.Call{}, errors.New("蜂窝通话已关闭")
	}
	call, err := backend.controller.Dial(ctx, number, true)
	if err != nil {
		return vowifi.Call{}, err
	}
	backend.server.rememberCellular(backend.deviceID, call.ID, false)
	return call, nil
}

func (backend *cellularSIPBackend) Answer(ctx context.Context, callID string) (vowifi.Call, error) {
	if backend.controller == nil {
		return vowifi.Call{}, errors.New("原蜂窝通话已经结束")
	}
	if backend.server != nil {
		release, admitErr := backend.server.admitCallMutation()
		if admitErr != nil {
			return vowifi.Call{}, admitErr
		}
		defer release()
	}
	call, err := backend.controller.Answer(ctx, callID)
	if err == nil {
		backend.server.rememberCellular(backend.deviceID, call.ID, false)
	}
	return call, err
}

func (backend *cellularSIPBackend) Hangup(ctx context.Context, callID string) error {
	if backend.controller == nil {
		return errors.New("原蜂窝通话已经结束")
	}
	return backend.controller.Hangup(ctx, callID)
}

func (backend *cellularSIPBackend) OpenMedia(ctx context.Context, callID, owner string) (vowifi.CallMedia, func(), error) {
	if backend.controller == nil || backend.server == nil {
		return nil, nil, errors.New("原蜂窝通话已经结束")
	}
	releaseOp, admitErr := backend.server.admitCallMutation()
	if admitErr != nil {
		return nil, nil, admitErr
	}
	finished := false
	defer func() {
		if !finished {
			releaseOp()
		}
	}()
	releaseLease, err := backend.server.acquireCallMediaLease(backend.deviceID, callID, "sip:"+owner)
	if err != nil {
		return nil, nil, err
	}
	media, release, err := backend.controller.OpenMedia(callID, owner)
	if err != nil || media == nil || !callMediaCodecSupported(media.Codec()) {
		if release != nil {
			release()
		}
		releaseLease()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("当前通话编码无法转换为 PCM")
	}
	finished = true
	return media, func() {
		release()
		releaseLease()
		releaseOp()
	}, nil
}

// vowifiSIPBackend is the SIP line captured for one reader or IMS device.
// Calls and actions sync the existing IMS history. They do not open a modem.
type vowifiSIPBackend struct {
	server   *Server
	deviceID string
	options  sip.Options
}

func (backend *vowifiSIPBackend) SIPOptions() sip.Options { return backend.options }

func (backend *vowifiSIPBackend) requireIMS() (VoWiFiCallController, error) {
	if backend.server == nil || backend.server.vowifi == nil || !backend.server.imsReady(backend.deviceID) {
		return nil, errors.New("IMS 未就绪，SIP 线路不可用")
	}
	controller, ok := backend.server.vowifi.(VoWiFiCallController)
	if !ok {
		return nil, errors.New("IMS 未就绪，SIP 线路不可用")
	}
	return controller, nil
}

func (backend *vowifiSIPBackend) sync(ctx context.Context, controller VoWiFiCallController) {
	calls, err := controller.Calls(backend.deviceID)
	if err != nil {
		return
	}
	for _, call := range calls {
		backend.server.upsertCallRecord(ctx, backend.deviceID, call)
		if call.ID != "" {
			backend.server.rememberCall(callBinding{
				deviceID: backend.deviceID, callID: call.ID, transport: "vowifi", ended: call.EndedAt != nil,
			})
		}
	}
}

func (backend *vowifiSIPBackend) Calls(ctx context.Context) ([]vowifi.Call, error) {
	controller, err := backend.requireIMS()
	if err != nil {
		return nil, err
	}
	calls, err := controller.Calls(backend.deviceID)
	if err != nil {
		return nil, err
	}
	for _, call := range calls {
		backend.server.upsertCallRecord(ctx, backend.deviceID, call)
		if call.ID != "" {
			backend.server.rememberCall(callBinding{
				deviceID: backend.deviceID, callID: call.ID, transport: "vowifi", ended: call.EndedAt != nil,
			})
		}
	}
	return calls, nil
}

func (backend *vowifiSIPBackend) Dial(ctx context.Context, number string) (vowifi.Call, error) {
	if backend.server == nil || !backend.server.sipDialAllowed(backend.deviceID) {
		return vowifi.Call{}, errors.New("SIP 线路已关闭")
	}
	release, admitErr := backend.server.admitCallMutation()
	if admitErr != nil {
		return vowifi.Call{}, admitErr
	}
	defer release()
	controller, err := backend.requireIMS()
	if err != nil {
		return vowifi.Call{}, err
	}
	call, err := controller.DialCall(ctx, backend.deviceID, number)
	backend.sync(ctx, controller)
	if err != nil {
		return vowifi.Call{}, err
	}
	backend.server.rememberCall(callBinding{deviceID: backend.deviceID, callID: call.ID, transport: "vowifi"})
	return call, nil
}

func (backend *vowifiSIPBackend) Answer(ctx context.Context, callID string) (vowifi.Call, error) {
	if backend.server != nil {
		release, admitErr := backend.server.admitCallMutation()
		if admitErr != nil {
			return vowifi.Call{}, admitErr
		}
		defer release()
	}
	controller, err := backend.requireIMS()
	if err != nil {
		return vowifi.Call{}, err
	}
	call, err := controller.AnswerCall(ctx, backend.deviceID, callID)
	backend.sync(ctx, controller)
	return call, err
}

func (backend *vowifiSIPBackend) Hangup(ctx context.Context, callID string) error {
	controller, err := backend.requireIMS()
	if err != nil {
		return err
	}
	err = controller.HangupCall(ctx, backend.deviceID, callID)
	backend.sync(ctx, controller)
	return err
}

func (backend *vowifiSIPBackend) OpenMedia(ctx context.Context, callID, owner string) (vowifi.CallMedia, func(), error) {
	if backend.server == nil {
		return nil, nil, errors.New("IMS 未就绪，SIP 线路不可用")
	}
	releaseOp, admitErr := backend.server.admitCallMutation()
	if admitErr != nil {
		return nil, nil, admitErr
	}
	finished := false
	defer func() {
		if !finished {
			releaseOp()
		}
	}()
	if _, err := backend.requireIMS(); err != nil {
		return nil, nil, err
	}
	mediaController, ok := backend.server.vowifi.(VoWiFiCallMediaController)
	if !ok {
		return nil, nil, errors.New("IMS 未就绪，SIP 线路不可用")
	}
	releaseLease, err := backend.server.acquireCallMediaLease(backend.deviceID, callID, "sip:"+owner)
	if err != nil {
		return nil, nil, err
	}
	media, err := mediaController.CallMedia(ctx, backend.deviceID, callID)
	if err != nil || media == nil || !callMediaCodecSupported(media.Codec()) {
		releaseLease()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("当前通话编码无法转换为 PCM")
	}
	finished = true
	return media, func() {
		releaseLease()
		releaseOp()
	}, nil
}

// cellBridgeSaver writes one cellular snapshot into the existing call history.
// The controller produces the WAV path; this does not invent a recording.
type cellBridgeSaver struct {
	server   *Server
	deviceID string
}

func (saver cellBridgeSaver) Save(ctx context.Context, call vowifi.Call) {
	if saver.server == nil {
		return
	}
	saver.server.saveCallSnapshot(ctx, saver.deviceID, call, "cellular")
}
