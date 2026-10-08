package server

import (
	"context"
	"errors"
	"strings"

	"vocat/internal/cellbridge/cellular"
	"vocat/internal/device"
	"vocat/internal/pcsc"
	"vocat/internal/store"
)

// cellBridgeGate authorises one cellular controller. It re-reads the current
// device, SIM and RF policy on every call so a long audio prepare cannot
// dial a card or modem that was swapped underneath it.
type cellBridgeGate struct {
	server        *Server
	deviceID      string
	usbGeneration string
}

func (gate *cellBridgeGate) Authorize(ctx context.Context) (cellular.Authorization, error) {
	if gate == nil || gate.server == nil || gate.server.store == nil {
		return cellular.Authorization{}, errors.New("cellular call policy gate is not configured")
	}
	config, err := gate.server.store.Device(ctx, gate.deviceID)
	if err != nil {
		return cellular.Authorization{}, errors.New("大疆模块配置不可用")
	}
	auth := cellular.Authorization{DeviceID: config.ID}
	if isReaderDevice(config) {
		auth.Kind = "reader"
		auth.Reason = "SIM 读卡器不使用蜂窝 AT"
		return auth, nil
	}
	if config.VoWiFiEnabled {
		auth.Kind = "vowifi"
		auth.Reason = "VoWiFi 已启用，不会因为 IMS 未就绪改用蜂窝通话"
		return auth, nil
	}
	entry, physicalID, present := gate.server.physicalForConfig(config)
	if !present || physicalID == "" {
		return cellular.Authorization{}, errors.New("大疆模块不在线")
	}
	if entry.Candidate.HardwareKind == pcsc.HardwareKind || config.DeviceType == store.DeviceTypeUSBSIMReader {
		auth.Kind = "reader"
		auth.Reason = "SIM 读卡器不使用蜂窝 AT"
		return auth, nil
	}
	if config.DeviceType != store.DeviceTypeDJI4G {
		auth.Kind = "other"
		auth.PhysicalID = physicalID
		auth.Reason = "当前设备不是大疆蜂窝模块"
		return auth, nil
	}
	// A known generation that disappeared or changed is a different module,
	// even when the physical id and ICCID strings were reused.
	if gate.usbGeneration != "" && entry.Candidate.USBGeneration != gate.usbGeneration {
		return cellular.Authorization{}, errors.New("USB 设备已更换，已停止对原模块的呼叫")
	}
	auth.USBGeneration = entry.Candidate.USBGeneration
	iccid := ""
	imsi := ""
	mode := 0
	modeKnown := false
	if entry.Snapshot != nil {
		iccid = strings.TrimSpace(entry.Snapshot.ICCID)
		imsi = strings.TrimSpace(entry.Snapshot.IMSI)
		mode = entry.Snapshot.OperatingMode
		modeKnown = entry.Snapshot.ModeKnown
	}
	auth.PhysicalID = physicalID
	auth.ICCID = iccid
	if reason := device.RegionBlockReason(imsi); reason != "" {
		return cellular.Authorization{}, errors.New(reason)
	}
	if modeKnown && (mode == 0 || mode == 4) {
		return cellular.Authorization{}, errors.New("飞行模式已打开，蜂窝通话不会绕过射频保护")
	}
	if iccid != "" {
		policy, policyErr := gate.server.store.CardPolicy(ctx, iccid)
		switch {
		case policyErr == nil && policy.VoWiFiEnabled:
			auth.Kind = "vowifi"
			auth.Reason = "这张 SIM 的策略是 VoWiFi，不会改用蜂窝 AT"
			return auth, nil
		case policyErr == nil && policy.AirplaneEnabled:
			return cellular.Authorization{}, errors.New("SIM 卡策略处于飞行模式，蜂窝通话不会绕过")
		case policyErr != nil && !errors.Is(policyErr, store.ErrNotFound):
			return cellular.Authorization{}, policyErr
		}
	}
	// Voice does not require the cellular data session to be enabled.
	auth.Kind = "cellular"
	return auth, nil
}

func isReaderDevice(config store.Device) bool {
	if config.DeviceType == store.DeviceTypeUSBSIMReader {
		return true
	}
	backend := strings.ToLower(strings.TrimSpace(config.DeviceBackend))
	transport := strings.ToLower(strings.TrimSpace(config.ESIMTransport))
	return backend == "pcsc" || transport == "pcsc"
}
