// Adapted from CellBridge gateway/internal/sip/auth.go
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: the upstream check treated any Authorization header as
// success. This file implements RFC 2617 Digest (qop=auth) with a random
// expiring nonce, constant-time comparison, and nc replay protection.

package sip

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

const digestRealm = "halo"

type digestCreds struct {
	username  string
	realm     string
	nonce     string
	uri       string
	response  string
	algorithm string
	cnonce    string
	nc        string
	qop       string
}

// ncKey is one username and cnonce. The highest accepted nc for that pair
// must strictly increase. A new cnonce may start at any nc greater than zero.
type ncKey struct {
	user   string
	cnonce string
}

type nonceState struct {
	value   string
	peer    *net.UDPAddr
	expires time.Time
	created time.Time
	maxNC   map[ncKey]uint64
}

func (s *Server) authenticate(msg *sipMessage, remote *net.UDPAddr) bool {
	raw, ok := msg.first("authorization")
	if !ok || strings.TrimSpace(raw) == "" {
		s.challenge(msg, remote, false)
		return false
	}
	creds, err := parseDigest(raw)
	if err != nil {
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}
	nc := strings.ToLower(creds.nc)
	ncValue, ncOK := parseNC(nc)
	s.mu.Lock()
	state := s.nonces[creds.nonce]
	peerOK := state != nil && addrEqual(state.peer, remote)
	fresh := state != nil && time.Now().Before(state.expires)
	s.mu.Unlock()
	if state == nil || !fresh {
		s.challenge(msg, remote, true)
		return false
	}
	if !ncOK {
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}

	// Compare every input the password hash depends on. A short-circuit
	// would let a forged header fail faster than a wrong password.
	userOK := constEqual(creds.username, s.username)
	realmOK := constEqual(creds.realm, s.realm)
	uriOK := constEqual(creds.uri, msg.requestURI)
	algoOK := creds.algorithm == "" || strings.EqualFold(creds.algorithm, "MD5")
	qopOK := strings.EqualFold(creds.qop, "auth")
	cnonceOK := creds.cnonce != "" && !strings.ContainsAny(creds.cnonce, "\"\r\n")
	expect := digestResponse(s.username, s.realm, s.password, creds.nonce, nc, creds.cnonce, "auth", msg.method, msg.requestURI)
	respOK := constHexEqual(creds.response, expect)
	fieldsOK := userOK & realmOK & uriOK & boolBit(algoOK && qopOK && cnonceOK) & respOK
	if fieldsOK != 1 {
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}
	if msg.method == "REGISTER" && (msg.toUser != s.username || msg.fromUser != s.username) {
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}
	if msg.method == "INVITE" && msg.fromUser != s.username {
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}
	if !peerOK {
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}
	return s.commitNonce(msg, remote, creds, ncValue)
}

func (s *Server) commitNonce(msg *sipMessage, remote *net.UDPAddr, creds digestCreds, ncValue uint64) bool {
	s.mu.Lock()
	state := s.nonces[creds.nonce]
	if state == nil || !time.Now().Before(state.expires) {
		s.mu.Unlock()
		s.challenge(msg, remote, true)
		return false
	}
	if !addrEqual(state.peer, remote) {
		s.mu.Unlock()
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}
	if state.maxNC == nil {
		state.maxNC = make(map[ncKey]uint64)
	}
	key := ncKey{user: creds.username, cnonce: creds.cnonce}
	prev, seen := state.maxNC[key]
	if seen && ncValue <= prev {
		s.mu.Unlock()
		s.reject(msg, remote, 403, "Forbidden")
		return false
	}
	if !seen && len(state.maxNC) >= 64 {
		s.mu.Unlock()
		s.challenge(msg, remote, true)
		return false
	}
	state.maxNC[key] = ncValue
	s.mu.Unlock()
	return true
}

func (s *Server) challenge(msg *sipMessage, remote *net.UDPAddr, stale bool) {
	nonce := s.freshNonce(remote, stale)
	staleText := ""
	if stale {
		staleText = ", stale=true"
	}
	header := `WWW-Authenticate: Digest realm="` + s.realm + `", nonce="` + nonce + `", algorithm=MD5, qop="auth"` + staleText
	s.reject(msg, remote, 401, "Unauthorized", header)
}

func (s *Server) freshNonce(remote *net.UDPAddr, forceNew bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !forceNew {
		if state := s.reusableNonceLocked(remote); state != nil {
			return state.value
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read failing is exceptional. Mix the clock so two failures
		// in the same process do not share a nonce.
		buf = []byte(time.Now().Format(time.RFC3339Nano))
	}
	value := hex.EncodeToString(buf)
	s.nonces[value] = &nonceState{
		value:   value,
		peer:    cloneAddr(remote),
		expires: time.Now().Add(s.nonceLife),
		created: time.Now(),
		maxNC:   make(map[ncKey]uint64),
	}
	s.evictNoncesLocked()
	return value
}

func (s *Server) reusableNonceLocked(remote *net.UDPAddr) *nonceState {
	if remote == nil {
		return nil
	}
	now := time.Now()
	var best *nonceState
	for _, state := range s.nonces {
		if state == nil || !now.Before(state.expires) || !addrEqual(state.peer, remote) {
			continue
		}
		if best == nil || state.created.After(best.created) {
			best = state
		}
	}
	return best
}

func (s *Server) evictNoncesLocked() {
	now := time.Now()
	for key, state := range s.nonces {
		if now.After(state.expires) {
			delete(s.nonces, key)
		}
	}
	for len(s.nonces) > 16 {
		var oldest string
		var at time.Time
		for key, state := range s.nonces {
			if oldest == "" || state.created.Before(at) {
				oldest = key
				at = state.created
			}
		}
		delete(s.nonces, oldest)
	}
}

func (s *Server) registeredPeer(remote *net.UDPAddr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reg == nil || !time.Now().Before(s.reg.expires) {
		return false
	}
	return addrEqual(s.reg.addr, remote)
}

func parseDigest(value string) (digestCreds, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || !strings.EqualFold(scheme, "Digest") {
		return digestCreds{}, errBadDigest
	}
	items, err := parseDirectives(rest)
	if err != nil {
		return digestCreds{}, err
	}
	creds := digestCreds{
		username:  items["username"],
		realm:     items["realm"],
		nonce:     items["nonce"],
		uri:       items["uri"],
		response:  items["response"],
		algorithm: items["algorithm"],
		cnonce:    items["cnonce"],
		nc:        items["nc"],
		qop:       items["qop"],
	}
	if creds.username == "" || creds.realm == "" || creds.nonce == "" || creds.uri == "" || creds.response == "" {
		return digestCreds{}, errBadDigest
	}
	return creds, nil
}

func parseDirectives(value string) (map[string]string, error) {
	out := make(map[string]string)
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ' ' || value[index] == '\t' || value[index] == ',') {
			index++
		}
		if index >= len(value) {
			break
		}
		start := index
		for index < len(value) && value[index] != '=' && value[index] != ',' {
			index++
		}
		if index >= len(value) || value[index] != '=' {
			return nil, errBadDigest
		}
		key := strings.ToLower(strings.TrimSpace(value[start:index]))
		index++
		for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
			index++
		}
		var b strings.Builder
		if index < len(value) && value[index] == '"' {
			index++
			closed := false
			for index < len(value) && !closed {
				switch value[index] {
				case '\\':
					index++
					if index >= len(value) {
						return nil, errBadDigest
					}
					b.WriteByte(value[index])
					index++
				case '"':
					index++
					closed = true
				default:
					b.WriteByte(value[index])
					index++
				}
			}
			if !closed {
				return nil, errBadDigest
			}
		} else {
			start = index
			for index < len(value) && value[index] != ',' {
				index++
			}
			b.WriteString(strings.TrimSpace(value[start:index]))
		}
		if key == "" {
			return nil, errBadDigest
		}
		out[key] = b.String()
	}
	return out, nil
}

func digestResponse(username, realm, password, nonce, nc, cnonce, qop, method, uri string) string {
	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	return md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

func md5hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func parseNC(value string) (uint64, bool) {
	if !validNC(value) {
		return 0, false
	}
	n, err := strconv.ParseUint(value, 16, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}

func validNC(value string) bool {
	if len(value) != 8 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func constEqual(a, b string) int {
	ab := []byte(a)
	bb := []byte(b)
	if len(ab) != len(bb) {
		var pad [64]byte
		n := copy(pad[:], ab)
		if n == 0 {
			n = 1
		}
		subtle.ConstantTimeCompare(pad[:n], pad[:n])
		return 0
	}
	return subtle.ConstantTimeCompare(ab, bb)
}

func constHexEqual(got, want string) int {
	gb, err1 := hex.DecodeString(got)
	wb, err2 := hex.DecodeString(want)
	if err1 != nil || err2 != nil || len(gb) != len(wb) || len(wb) != md5.Size {
		var dummy [md5.Size]byte
		subtle.ConstantTimeCompare(dummy[:], dummy[:])
		return 0
	}
	return subtle.ConstantTimeCompare(gb, wb)
}

func boolBit(ok bool) int {
	if ok {
		return 1
	}
	return 0
}

var errBadDigest = errors.New("sip: malformed digest")
