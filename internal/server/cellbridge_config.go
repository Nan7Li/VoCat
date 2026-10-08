package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"vocat/internal/cellbridge/voice"
	"vocat/internal/store"
)

const cellBridgeSettingKey = "cellbridge"

// cellBridgeConfig is the persisted bridge setup. Password stays in the
// database document and is never copied into API responses or logs.
type cellBridgeConfig struct {
	SIPEnabled       bool   `json:"sip_enabled"`
	SIPDeviceID      string `json:"sip_device_id"`
	ListenAddr       string `json:"listen_addr"`
	AdvertisedIP     string `json:"advertised_ip"`
	RTPListenAddr    string `json:"rtp_listen_addr"`
	Username         string `json:"username"`
	Password         string `json:"password,omitempty"`
	CellularEnabled  bool   `json:"cellular_enabled"`
	CellularDeviceID string `json:"cellular_device_id"`
	CaptureDevice    string `json:"capture_device"`
	PlaybackDevice   string `json:"playback_device"`
	RuntimeDir       string `json:"runtime_dir"`
	ADBPath          string `json:"adb_path"`
	ADBSocket        string `json:"adb_socket"`
	Bootstrap        bool   `json:"bootstrap"`
}

type cellBridgePublicConfig struct {
	SIPEnabled       bool   `json:"sip_enabled"`
	SIPDeviceID      string `json:"sip_device_id"`
	ListenAddr       string `json:"listen_addr"`
	AdvertisedIP     string `json:"advertised_ip"`
	RTPListenAddr    string `json:"rtp_listen_addr"`
	Username         string `json:"username"`
	HasPassword      bool   `json:"has_password"`
	CellularEnabled  bool   `json:"cellular_enabled"`
	CellularDeviceID string `json:"cellular_device_id"`
	CaptureDevice    string `json:"capture_device"`
	PlaybackDevice   string `json:"playback_device"`
	RuntimeDir       string `json:"runtime_dir"`
	ADBPath          string `json:"adb_path"`
	ADBSocket        string `json:"adb_socket"`
	Bootstrap        bool   `json:"bootstrap"`
}

type cellBridgeSIPStatus struct {
	Running       bool   `json:"running"`
	Registered    bool   `json:"registered"`
	ListenAddr    string `json:"listen_addr,omitempty"`
	RTPListenAddr string `json:"rtp_listen_addr,omitempty"`
	AdvertisedIP  string `json:"advertised_ip,omitempty"`
	Username      string `json:"username,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type cellBridgeCellularStatus struct {
	Ready    bool   `json:"ready"`
	DeviceID string `json:"device_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type cellBridgeStatus struct {
	Phase    string                   `json:"phase"`
	Reason   string                   `json:"reason,omitempty"`
	SIP      cellBridgeSIPStatus      `json:"sip"`
	Cellular cellBridgeCellularStatus `json:"cellular"`
}

func defaultCellBridgeConfig() cellBridgeConfig {
	return cellBridgeConfig{
		ListenAddr:     "0.0.0.0:5060",
		RTPListenAddr:  "0.0.0.0:40000",
		Username:       "halo",
		CaptureDevice:  "plughw:1,0",
		PlaybackDevice: "plughw:1,0",
		ADBPath:        "adb",
		ADBSocket:      "tcp:127.0.0.1:5038",
	}
}

func (config cellBridgeConfig) public() cellBridgePublicConfig {
	return cellBridgePublicConfig{
		SIPEnabled:       config.SIPEnabled,
		SIPDeviceID:      config.SIPDeviceID,
		ListenAddr:       config.ListenAddr,
		AdvertisedIP:     config.AdvertisedIP,
		RTPListenAddr:    config.RTPListenAddr,
		Username:         config.Username,
		HasPassword:      config.Password != "",
		CellularEnabled:  config.CellularEnabled,
		CellularDeviceID: config.CellularDeviceID,
		CaptureDevice:    config.CaptureDevice,
		PlaybackDevice:   config.PlaybackDevice,
		RuntimeDir:       config.RuntimeDir,
		ADBPath:          config.ADBPath,
		ADBSocket:        config.ADBSocket,
		Bootstrap:        config.Bootstrap,
	}
}

func (config cellBridgeConfig) normalized() cellBridgeConfig {
	config.SIPDeviceID = strings.TrimSpace(config.SIPDeviceID)
	config.ListenAddr = strings.TrimSpace(config.ListenAddr)
	config.AdvertisedIP = strings.TrimSpace(config.AdvertisedIP)
	config.RTPListenAddr = strings.TrimSpace(config.RTPListenAddr)
	config.Username = strings.TrimSpace(config.Username)
	config.CellularDeviceID = strings.TrimSpace(config.CellularDeviceID)
	config.CaptureDevice = strings.TrimSpace(config.CaptureDevice)
	config.PlaybackDevice = strings.TrimSpace(config.PlaybackDevice)
	config.RuntimeDir = strings.TrimSpace(config.RuntimeDir)
	config.ADBPath = strings.TrimSpace(config.ADBPath)
	config.ADBSocket = strings.TrimSpace(config.ADBSocket)
	if config.ListenAddr == "" {
		config.ListenAddr = "0.0.0.0:5060"
	}
	if config.RTPListenAddr == "" {
		config.RTPListenAddr = "0.0.0.0:40000"
	}
	if config.Username == "" {
		config.Username = "halo"
	}
	if config.CaptureDevice == "" {
		config.CaptureDevice = "plughw:1,0"
	}
	if config.PlaybackDevice == "" {
		config.PlaybackDevice = "plughw:1,0"
	}
	if config.ADBPath == "" {
		config.ADBPath = "adb"
	}
	return config
}

func (s *Server) loadCellBridgeConfig(ctx context.Context) cellBridgeConfig {
	config := defaultCellBridgeConfig()
	if s.store == nil {
		return config
	}
	setting, err := s.store.AppSetting(ctx, cellBridgeSettingKey)
	if err != nil {
		return config
	}
	if json.Unmarshal(setting.Value, &config) != nil {
		return defaultCellBridgeConfig()
	}
	savedPassword := config.Password
	config = config.normalized()
	config.Password = savedPassword
	return config
}

func (s *Server) saveCellBridgeConfig(ctx context.Context, config cellBridgeConfig) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return s.store.UpsertAppSetting(ctx, store.AppSetting{
		Key:       cellBridgeSettingKey,
		Value:     raw,
		Sensitive: true,
	})
}

func (s *Server) handleCellBridgeSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		config := s.loadCellBridgeConfig(r.Context())
		s.writeCellBridgeDocument(w, config, s.cellBridgeStatusSnapshot())
	case http.MethodPut:
		var request cellBridgeConfig
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		current := s.loadCellBridgeConfig(r.Context())
		next, err := s.resolveCellBridgePut(r.Context(), current, request)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cellbridge", err.Error())
			return
		}
		if err := s.saveCellBridgeConfig(r.Context(), next); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.noteCellBridgeApplying(next)
		s.wakeCellBridge()
		s.writeCellBridgeDocument(w, next, s.cellBridgeStatusSnapshot())
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) writeCellBridgeDocument(w http.ResponseWriter, config cellBridgeConfig, status cellBridgeStatus) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"config": config.public(),
			"status": status,
		},
	})
}

func (s *Server) resolveCellBridgePut(ctx context.Context, current, request cellBridgeConfig) (cellBridgeConfig, error) {
	next := request.normalized()
	password := request.Password
	if password == "" || password == store.SecretMask {
		password = current.Password
	}
	if strings.ContainsAny(password, "\r\n") {
		return cellBridgeConfig{}, errors.New("SIP 密码含有不支持的换行")
	}
	next.Password = password
	if _, _, err := net.SplitHostPort(next.ListenAddr); err != nil {
		return cellBridgeConfig{}, errors.New("SIP 监听地址需要是 host:port")
	}
	if _, _, err := net.SplitHostPort(next.RTPListenAddr); err != nil {
		return cellBridgeConfig{}, errors.New("音频 UDP 监听地址需要是 host:port")
	}
	if next.SIPEnabled {
		if next.Username == "" || strings.ContainsAny(next.Username, " \t\"<>") {
			return cellBridgeConfig{}, errors.New("请填写 SIP 账号")
		}
		if next.Password == "" {
			return cellBridgeConfig{}, errors.New("请设置 SIP 密码")
		}
		if net.ParseIP(next.AdvertisedIP) == nil {
			return cellBridgeConfig{}, errors.New("请填写客户端能访问的 Halo IP，作为 SIP 连接地址")
		}
		if next.SIPDeviceID == "" {
			return cellBridgeConfig{}, errors.New("请选择 SIP 通话线路")
		}
		if _, err := s.requireStoredDevice(ctx, next.SIPDeviceID); err != nil {
			return cellBridgeConfig{}, err
		}
	}
	if next.CellularEnabled {
		if next.CellularDeviceID == "" {
			return cellBridgeConfig{}, errors.New("请选择大疆音频设备")
		}
		device, err := s.requireStoredDevice(ctx, next.CellularDeviceID)
		if err != nil {
			return cellBridgeConfig{}, err
		}
		if device.DeviceType != store.DeviceTypeDJI4G {
			return cellBridgeConfig{}, errors.New("大疆通话音频只能选择大疆模块")
		}
		if err := voice.ValidateDevice(next.CaptureDevice); err != nil {
			return cellBridgeConfig{}, errors.New("ALSA 录音设备名称无效")
		}
		if err := voice.ValidateDevice(next.PlaybackDevice); err != nil {
			return cellBridgeConfig{}, errors.New("ALSA 播放设备名称无效")
		}
	}
	return next, nil
}

func (s *Server) requireStoredDevice(ctx context.Context, id string) (store.Device, error) {
	if s.store == nil {
		return store.Device{}, errors.New("设备存储不可用")
	}
	device, err := s.store.Device(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Device{}, errors.New("选择的设备不存在")
	}
	if err != nil {
		return store.Device{}, err
	}
	return device, nil
}
