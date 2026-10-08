package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

const maxCallMediaMessage = 16 << 10

// handleCallMedia upgrades an authenticated same-origin request to a binary
// PCM bridge. Each WebSocket message contains little-endian signed 16-bit,
// 8 kHz, mono samples. RTP and codec details remain inside the IMS provider.
func (s *Server) handleCallMedia(w http.ResponseWriter, r *http.Request, config store.Device) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	callID := strings.TrimSpace(r.URL.Query().Get("call_id"))
	if callID == "" || len(callID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_call_id", "call_id is required")
		return true
	}
	if binding, ok := s.bindingForAction(config.ID, callID); ok {
		switch binding.transport {
		case "cellular":
			return s.serveBrowserPCM(w, r, config, callID, s.openCellularBrowserMedia(config.ID, callID, binding))
		case "vowifi":
			return s.serveBrowserPCM(w, r, config, callID, s.openVoWiFiBrowserMedia(config.ID, callID))
		default:
			writeError(w, http.StatusNotImplemented, "call_media_unavailable", "这条通话没有浏览器音频")
			return true
		}
	}
	if s.cellularHasCall(config.ID, callID) {
		return s.serveBrowserPCM(w, r, config, callID, s.openCellularBrowserMedia(config.ID, callID, callBinding{transport: "cellular"}))
	}
	if s.callTransport(config.ID) != "vowifi" {
		writeError(w, http.StatusNotImplemented, "call_media_unavailable", "browser audio is only available for an active VoWiFi IMS call")
		return true
	}
	return s.serveBrowserPCM(w, r, config, callID, s.openVoWiFiBrowserMedia(config.ID, callID))
}

func (s *Server) openCellularBrowserMedia(deviceID, callID string, binding callBinding) func(context.Context) (vowifi.CallMedia, func(), error) {
	return func(context.Context) (vowifi.CallMedia, func(), error) {
		if binding.ended {
			return nil, nil, errors.New("原蜂窝通话已经结束")
		}
		controller := s.cellularControllerFor(deviceID)
		if controller == nil {
			return nil, nil, errors.New("原蜂窝通话已经结束")
		}
		return controller.OpenMedia(callID, "browser")
	}
}

func (s *Server) openVoWiFiBrowserMedia(deviceID, callID string) func(context.Context) (vowifi.CallMedia, func(), error) {
	return func(ctx context.Context) (vowifi.CallMedia, func(), error) {
		controller, ok := s.vowifi.(VoWiFiCallMediaController)
		if !ok {
			return nil, nil, errors.New("the active IMS session does not expose RTP media")
		}
		media, err := controller.CallMedia(ctx, deviceID, callID)
		return media, nil, err
	}
}

func (s *Server) serveBrowserPCM(w http.ResponseWriter, r *http.Request, config store.Device, callID string, open func(context.Context) (vowifi.CallMedia, func(), error)) bool {
	releaseLease, err := s.acquireCallMediaLease(config.ID, callID, "browser")
	if err != nil {
		writeError(w, http.StatusConflict, "call_media_busy", err.Error())
		return true
	}
	defer releaseLease()
	media, releaseMedia, err := open(r.Context())
	if releaseMedia != nil {
		defer releaseMedia()
	}
	if err != nil {
		writeError(w, http.StatusConflict, "call_media_unavailable", err.Error())
		return true
	}
	if media == nil || !callMediaCodecSupported(media.Codec()) {
		writeError(w, http.StatusNotImplemented, "call_codec_unsupported", "当前通话编码无法转换为浏览器音频")
		return true
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return true
	}
	connection.SetReadLimit(maxCallMediaMessage)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer connection.Close(websocket.StatusNormalClosure, "call media closed")

	downlink := make(chan error, 1)
	go func() {
		defer cancel()
		for {
			samples, readErr := media.ReadPCM(ctx)
			if readErr != nil {
				downlink <- readErr
				return
			}
			payload := make([]byte, len(samples)*2)
			for index, sample := range samples {
				binary.LittleEndian.PutUint16(payload[index*2:], uint16(sample))
			}
			if writeErr := connection.Write(ctx, websocket.MessageBinary, payload); writeErr != nil {
				downlink <- writeErr
				return
			}
		}
	}()

	for {
		select {
		case err := <-downlink:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				s.logger.Debug("call media downlink closed", "device_id", config.ID, "call_id", callID, "error", err)
			}
			return true
		default:
		}
		messageType, payload, readErr := connection.Read(ctx)
		if readErr != nil {
			return true
		}
		if messageType != websocket.MessageBinary || len(payload) == 0 || len(payload)%2 != 0 {
			continue
		}
		samples := make([]int16, len(payload)/2)
		for index := range samples {
			samples[index] = int16(binary.LittleEndian.Uint16(payload[index*2:]))
		}
		if err := media.WritePCM(samples); err != nil {
			return true
		}
	}
}
