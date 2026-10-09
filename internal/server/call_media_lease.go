package server

import (
	"errors"
	"strings"
)

var errCallMediaBusy = errors.New("通话音频已被其他客户端占用")

type callMediaLeaseKey struct{ deviceID, callID string }
type callMediaLease struct{ owner string }

// Both the authenticated browser WebSocket and SIP backend use this lease.
// IMS exposes one downlink channel, so a second reader must be rejected.
func (s *Server) acquireCallMediaLease(deviceID, callID, owner string) (func(), error) {
	key := callMediaLeaseKey{deviceID: deviceID, callID: callID}
	lease := &callMediaLease{owner: owner}
	s.callMediaLeaseMu.Lock()
	if s.callMediaLeases == nil {
		s.callMediaLeases = make(map[callMediaLeaseKey]*callMediaLease)
	}
	if s.callMediaLeases[key] != nil {
		s.callMediaLeaseMu.Unlock()
		return nil, errCallMediaBusy
	}
	s.callMediaLeases[key] = lease
	s.callMediaLeaseMu.Unlock()
	return func() {
		s.callMediaLeaseMu.Lock()
		if s.callMediaLeases[key] == lease {
			delete(s.callMediaLeases, key)
		}
		s.callMediaLeaseMu.Unlock()
	}, nil
}

func callMediaCodecSupported(codec string) bool {
	switch strings.ToUpper(strings.TrimSpace(codec)) {
	case "PCMU", "PCMA":
		return true
	default:
		return false
	}
}
