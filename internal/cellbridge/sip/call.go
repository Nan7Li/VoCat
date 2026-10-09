// Adapted from CellBridge gateway/internal/sip/session.go
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: the backend call id, peer, and media owner are fixed when
// the dialog is created. CANCEL and BYE must match that peer. Failure
// cleanup hangs up only a call this dialog dialed, answered, or is still
// offering while it rings.

package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"vocat/internal/vowifi"
)

type session struct {
	direction          string
	sipCallID          string
	backendID          string
	owner              string
	number             string
	peer               *net.UDPAddr
	localTag           string
	remoteTag          string
	localFrom          string
	localTo            string
	initialInviteURI   string
	remoteDialogTarget string
	inviteCSeq         int
	inviteBranch       string
	inviteVia          []string
	inviteFrom         string
	inviteTo           string
	txKey              txKey
	inviteRaw          []byte
	lastResp           []byte

	rtpRemote *net.UDPAddr
	media     vowifi.CallMedia
	release   func()
	mediaOn   bool
	rtpInit   bool
	ssrc      uint32
	txSeq     uint16
	txTS      uint32
	seqInit   bool
	lastSeq   uint16

	ctx             context.Context
	cancel          context.CancelFunc
	dialCancel      context.CancelFunc
	created         time.Time
	acceptedAt      time.Time
	deadAt          time.Time
	nextInvite      time.Time
	next200         time.Time
	inviteDelay     time.Duration
	finalDelay      time.Duration
	nextCSeq        int
	ackCh           chan struct{}
	proceed         chan struct{}
	workerStarted   bool
	dead            bool
	finalSent       bool
	dialogConfirmed bool
	answerStarted   bool
	ownAnswered     bool
	ackSent         bool
	pendingBye      bool
	signalMu        sync.Mutex
	ackSeen         bool
	peerBye         bool
	byeSent         bool
	cancelSent      bool
	gotProvisional  bool
	endReason       string
	doneOnce        sync.Once
}

func (s *Server) handleInvite(msg *sipMessage, remote *net.UDPAddr) {
	if s.resendCached(msg, remote) {
		return
	}
	if msg.branch == "" || msg.callID == "" || msg.fromTag == "" || msg.cseqMethod != "INVITE" || len(msg.vias) == 0 {
		s.reply(msg, remote, 400, "Bad Request")
		return
	}
	if !s.authenticate(msg, remote) {
		return
	}
	if !s.registeredPeer(remote) {
		s.reply(msg, remote, 403, "Forbidden")
		return
	}
	s.mu.Lock()
	if existing := s.sessions[msg.callID]; existing != nil {
		same := addrEqual(existing.peer, remote)
		last := append([]byte(nil), existing.lastResp...)
		s.mu.Unlock()
		if same && len(last) > 0 {
			s.writeTo(remote, last)
			return
		}
		s.reply(msg, remote, 403, "Forbidden")
		return
	}
	s.mu.Unlock()

	rtpIP, rtpPort, err := validateAudioSDP(msg.body, remote.IP)
	if err != nil {
		s.reply(msg, remote, 488, "Not Acceptable Here")
		return
	}
	number := uriUser(msg.requestURI)
	if !validNumber(number) {
		s.reply(msg, remote, 400, "Bad Request")
		return
	}
	if s.localBusy() {
		s.reply(msg, remote, 486, "Busy Here")
		return
	}
	busy, unknown := s.backendBusy()
	if busy {
		s.reply(msg, remote, 486, "Busy Here")
		return
	}
	if unknown {
		s.reply(msg, remote, 480, "Temporarily Unavailable")
		return
	}

	sess := s.newOutbound(msg, remote, number, rtpIP, rtpPort)
	s.mu.Lock()
	if s.stopping || s.localBusyLocked() {
		s.mu.Unlock()
		s.reply(msg, remote, 486, "Busy Here")
		return
	}
	if _, exists := s.sessions[sess.sipCallID]; exists {
		s.mu.Unlock()
		s.reply(msg, remote, 486, "Busy Here")
		return
	}
	s.sessions[sess.sipCallID] = sess
	sess.workerStarted = true
	s.mu.Unlock()
	s.sendSession(sess, 100, "Trying", nil, "", false)
	if !s.worker(func() {
		defer s.finishSession(sess)
		s.runOutbound(sess)
	}) {
		s.finishSession(sess)
	}
}

func (s *Server) newOutbound(msg *sipMessage, remote *net.UDPAddr, number string, rtpIP net.IP, rtpPort int) *session {
	ctx, cancel := context.WithCancel(s.runCtx)
	key, _ := makeTxKey(msg, remote)
	target := dialogURI(msg.contact)
	if target == "" {
		target = msg.requestURI
	}
	return &session{
		direction:          dirOutbound,
		sipCallID:          msg.callID,
		owner:              "cellbridge-sip/" + msg.callID,
		number:             number,
		peer:               cloneAddr(remote),
		localTag:           randomHex(6),
		remoteTag:          msg.fromTag,
		localFrom:          msg.to,
		localTo:            msg.from,
		initialInviteURI:   msg.requestURI,
		remoteDialogTarget: target,
		inviteCSeq:         msg.cseqNum,
		inviteBranch:       msg.branch,
		inviteVia:          append([]string(nil), msg.vias...),
		inviteFrom:         msg.from,
		inviteTo:           msg.to,
		txKey:              key,
		rtpRemote:          &net.UDPAddr{IP: cloneIP(rtpIP), Port: rtpPort},
		ctx:                ctx,
		cancel:             cancel,
		created:            time.Now(),
		ackCh:              make(chan struct{}, 1),
		proceed:            make(chan struct{}, 1),
	}
}

type dialResult struct {
	call vowifi.Call
	err  error
}

type dialSlot struct {
	mu     sync.Mutex
	taken  bool
	ready  bool
	result dialResult
}

func (s *Server) runOutbound(sess *session) {
	deadline := time.Now().Add(s.dialTimeout)
	dialCtx, dialCancel := context.WithDeadline(sess.ctx, deadline)
	s.mu.Lock()
	sess.dialCancel = dialCancel
	s.mu.Unlock()
	defer dialCancel()

	// Dial runs on its own worker so a backend that ignores ctx cannot hold
	// the SIP budget. Close waits for that worker. A late success is hung up
	// and never turned into 180 or 408.
	slot := &dialSlot{}
	finished := make(chan struct{})
	if !s.worker(func() {
		call, err := s.opts.Backend.Dial(dialCtx, sess.number)
		slot.mu.Lock()
		slot.ready = true
		slot.result = dialResult{call: call, err: err}
		taken := slot.taken
		slot.mu.Unlock()
		close(finished)
		if taken {
			s.settleLateDial(call, err)
		}
	}) {
		return
	}

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-finished:
		slot.mu.Lock()
		res := slot.result
		slot.mu.Unlock()
		if !time.Now().Before(deadline) {
			s.failExpiredDial(sess, res.call, res.err)
			return
		}
		s.acceptDial(sess, res.call, res.err, deadline)
	case <-timer.C:
		slot.mu.Lock()
		if slot.ready {
			res := slot.result
			slot.mu.Unlock()
			s.failExpiredDial(sess, res.call, res.err)
			return
		}
		slot.taken = true
		slot.mu.Unlock()
		s.sendExpired(sess)
	case <-sess.ctx.Done():
		slot.mu.Lock()
		if slot.ready {
			res := slot.result
			slot.mu.Unlock()
			s.acceptDial(sess, res.call, res.err, deadline)
			return
		}
		slot.taken = true
		slot.mu.Unlock()
	}
}

func (s *Server) settleLateDial(call vowifi.Call, err error) {
	if call.ID == "" {
		return
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return
	}
	s.hangupCall(call.ID)
}

func (s *Server) sendExpired(sess *session) {
	if s.reason(sess) != "" || sess.ctx.Err() != nil {
		return
	}
	s.sendFinal(sess, 504, "Server Time-out")
	s.noteReason(sess, endFailed)
}

func (s *Server) failExpiredDial(sess *session, call vowifi.Call, err error) {
	s.settleLateDial(call, err)
	if s.reason(sess) != "" || sess.ctx.Err() != nil {
		return
	}
	s.sendFinal(sess, 504, "Server Time-out")
	s.noteReason(sess, endFailed)
}

func (s *Server) acceptDial(sess *session, call vowifi.Call, err error, deadline time.Time) {
	if err == nil && !time.Now().Before(deadline) {
		s.failExpiredDial(sess, call, nil)
		return
	}
	if s.reason(sess) != "" || sess.ctx.Err() != nil {
		if call.ID != "" && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			s.hangupCall(call.ID)
		}
		return
	}
	if err != nil {
		if call.ID != "" && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			s.hangupCall(call.ID)
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		code, text := 500, "Server Error"
		if errors.Is(err, context.DeadlineExceeded) {
			code, text = 504, "Server Time-out"
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "busy") {
			code, text = 486, "Busy Here"
		}
		s.sendFinal(sess, code, text)
		s.noteReason(sess, endFailed)
		return
	}
	if call.ID == "" {
		s.sendFinal(sess, 500, "Server Error")
		s.noteReason(sess, endFailed)
		return
	}
	s.mu.Lock()
	sess.backendID = call.ID
	s.byBackend[call.ID] = sess
	s.mu.Unlock()

	s.sendProgress(sess, 180, "Ringing")
	if _, err := s.waitReady(sess, deadline); err != nil {
		if s.reason(sess) == endCancel || errors.Is(sess.ctx.Err(), context.Canceled) && s.reason(sess) != "" {
			s.hangupCall(call.ID)
			return
		}
		if errors.Is(sess.ctx.Err(), context.Canceled) {
			s.hangupCall(call.ID)
			return
		}
		switch {
		case errors.Is(err, errCodec):
			s.sendFinal(sess, 488, "Not Acceptable Here")
			s.noteReason(sess, endCodec)
		case errors.Is(err, errGone):
			s.sendFinal(sess, 480, "Temporarily Unavailable")
			s.noteReason(sess, endRemoteGone)
		default:
			s.sendFinal(sess, 408, "Request Timeout")
			s.noteReason(sess, endTimeout)
		}
		return
	}
	if s.reason(sess) != "" || sess.ctx.Err() != nil {
		s.hangupCall(call.ID)
		return
	}
	if !s.sendFinal(sess, 200, "OK") {
		s.hangupCall(call.ID)
		return
	}
	timer := time.NewTimer(s.ackWait)
	defer timer.Stop()
	select {
	case <-sess.ackCh:
	case <-timer.C:
		s.noteReason(sess, endAckTimeout)
		return
	case <-sess.ctx.Done():
		return
	}
	if err := s.bridge(sess); err != nil && s.reason(sess) == "" && !errors.Is(err, context.Canceled) {
		s.noteReason(sess, endFailed)
	}
}

func (s *Server) handleAck(msg *sipMessage, remote *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[msg.callID]
	if sess == nil || sess.direction != dirOutbound {
		return
	}
	if !addrEqual(sess.peer, remote) || msg.cseqNum != sess.inviteCSeq {
		return
	}
	if msg.toTag != sess.localTag || msg.fromTag != sess.remoteTag {
		return
	}
	if sess.ackSeen {
		return
	}
	sess.ackSeen = true
	select {
	case sess.ackCh <- struct{}{}:
	default:
	}
}

func (s *Server) handleBye(msg *sipMessage, remote *net.UDPAddr) {
	if s.resendCached(msg, remote) {
		return
	}
	s.mu.Lock()
	sess := s.sessions[msg.callID]
	if sess == nil || !dialogMatch(sess, msg, remote) {
		s.mu.Unlock()
		s.reply(msg, remote, 481, "Call/Transaction Does Not Exist")
		return
	}
	sess.peerBye = true
	if sess.endReason == "" {
		sess.endReason = endBye
	}
	cancel := sess.cancel
	s.mu.Unlock()
	s.reply(msg, remote, 200, "OK")
	if cancel != nil {
		cancel()
	}
}

func (s *Server) handleCancel(msg *sipMessage, remote *net.UDPAddr) {
	if s.resendCached(msg, remote) {
		return
	}
	s.mu.Lock()
	sess := s.sessions[msg.callID]
	if sess == nil || sess.direction != dirOutbound || sess.dead {
		s.mu.Unlock()
		s.reply(msg, remote, 481, "Call/Transaction Does Not Exist")
		return
	}
	if !addrEqual(sess.peer, remote) || msg.branch != sess.inviteBranch || msg.cseqNum != sess.inviteCSeq {
		s.mu.Unlock()
		s.reply(msg, remote, 481, "Call/Transaction Does Not Exist")
		return
	}
	if sess.dialogConfirmed || sess.finalSent {
		s.mu.Unlock()
		s.reply(msg, remote, 200, "OK")
		return
	}
	sess.finalSent = true
	if sess.endReason == "" {
		sess.endReason = endCancel
	}
	cancel := sess.cancel
	dialCancel := sess.dialCancel
	s.mu.Unlock()
	s.reply(msg, remote, 200, "OK")
	s.send487(sess)
	if dialCancel != nil {
		dialCancel()
	}
	if cancel != nil {
		cancel()
	}
}

func (s *Server) handleResponse(msg *sipMessage, remote *net.UDPAddr) {
	if msg.cseqMethod != "INVITE" && msg.cseqMethod != "BYE" && msg.cseqMethod != "CANCEL" {
		return
	}
	s.mu.Lock()
	sess := s.sessions[msg.callID]
	if sess == nil || !addrEqual(sess.peer, remote) || sess.direction != dirInbound {
		s.mu.Unlock()
		return
	}
	if msg.cseqMethod != "INVITE" || msg.cseqNum != sess.inviteCSeq {
		s.mu.Unlock()
		return
	}
	if msg.branch != "" && sess.inviteBranch != "" && msg.branch != sess.inviteBranch {
		s.mu.Unlock()
		return
	}
	if msg.status < 200 {
		sess.gotProvisional = true
		s.mu.Unlock()
		return
	}
	if msg.status >= 300 {
		if msg.toTag != "" && sess.remoteTag == "" {
			sess.remoteTag = msg.toTag
		}
		s.mu.Unlock()
		s.sendAck(sess, true)
		s.noteReason(sess, endReject)
		s.cancelSession(sess)
		return
	}
	if msg.toTag != "" && sess.remoteTag == "" {
		sess.remoteTag = msg.toTag
	}
	if uri := dialogURI(msg.contact); uri != "" {
		sess.remoteDialogTarget = uri
	}
	late := !sess.answerStarted && (sess.dead || sess.endReason != "")
	if sess.answerStarted || late {
		s.mu.Unlock()
		s.sendAck(sess, false)
		if late {
			s.sendBYE(sess)
		}
		return
	}
	ip, port, err := validateAudioSDP(msg.body, remote.IP)
	if err != nil {
		sess.dialogConfirmed = true
		if sess.endReason == "" {
			sess.endReason = endBadSDP
		}
		cancel := sess.cancel
		s.mu.Unlock()
		s.sendAck(sess, false)
		if cancel != nil {
			cancel()
		}
		return
	}
	sess.dialogConfirmed = true
	sess.answerStarted = true
	sess.gotProvisional = true
	sess.rtpRemote = &net.UDPAddr{IP: cloneIP(ip), Port: port}
	s.mu.Unlock()
	s.sendAck(sess, false)
	select {
	case sess.proceed <- struct{}{}:
	default:
	}
}

func (s *Server) maybeInbound(calls []vowifi.Call) {
	s.mu.Lock()
	if s.stopping || s.reg == nil || !time.Now().Before(s.reg.expires) || s.localBusyLocked() {
		s.mu.Unlock()
		return
	}
	peer := cloneAddr(s.reg.addr)
	target := s.reg.requestURI
	s.mu.Unlock()
	var chosen *vowifi.Call
	for i := range calls {
		if incomingRinging(calls[i]) {
			chosen = &calls[i]
			break
		}
	}
	if chosen == nil || peer == nil {
		return
	}
	sess := s.newInbound(*chosen, peer, target)
	s.mu.Lock()
	if s.stopping || s.localBusyLocked() {
		if sess.cancel != nil {
			sess.cancel()
		}
		s.mu.Unlock()
		return
	}
	if prev := s.byBackend[chosen.ID]; prev != nil {
		if sess.cancel != nil {
			sess.cancel()
		}
		s.mu.Unlock()
		return
	}
	s.sessions[sess.sipCallID] = sess
	s.byBackend[sess.backendID] = sess
	sess.workerStarted = true
	raw := s.buildInviteLocked(sess)
	sess.inviteRaw = raw
	sess.inviteDelay = s.t1
	sess.nextInvite = time.Now().Add(s.t1)
	s.mu.Unlock()
	s.writeTo(peer, raw)
	if !s.worker(func() {
		defer s.finishSession(sess)
		s.runInbound(sess)
	}) {
		s.finishSession(sess)
	}
}

func (s *Server) newInbound(call vowifi.Call, peer *net.UDPAddr, target string) *session {
	ctx, cancel := context.WithCancel(s.runCtx)
	number := sanitizeUser(call.Number)
	callID := randomHex(8) + "@" + s.advertised
	from := "<sip:" + number + "@" + s.advertised + ">"
	to := "<sip:" + s.username + "@" + s.advertised + ">"
	if target == "" {
		target = "sip:" + s.username + "@" + s.viaHost()
	}
	return &session{
		direction:          dirInbound,
		sipCallID:          callID,
		backendID:          call.ID,
		owner:              "cellbridge-sip/" + callID,
		number:             number,
		peer:               peer,
		localTag:           randomHex(6),
		localFrom:          from,
		localTo:            to,
		initialInviteURI:   target,
		remoteDialogTarget: target,
		inviteCSeq:         1,
		inviteBranch:       "z9hG4bK" + randomHex(8),
		ctx:                ctx,
		cancel:             cancel,
		created:            time.Now(),
		ackCh:              make(chan struct{}, 1),
		proceed:            make(chan struct{}, 1),
	}
}

func (s *Server) runInbound(sess *session) {
	select {
	case <-sess.ctx.Done():
		return
	case <-sess.proceed:
	}
	if s.reason(sess) != "" {
		return
	}
	_, err := s.opts.Backend.Answer(sess.ctx, sess.backendID)
	if s.reason(sess) != "" || sess.ctx.Err() != nil {
		if err == nil {
			s.mu.Lock()
			sess.ownAnswered = true
			s.mu.Unlock()
		}
		return
	}
	if err != nil {
		if st, live, ok := s.lookupState(sess.backendID); ok && live && normState(st) == "active" {
			s.noteReason(sess, endExternal)
		} else if ok && !live {
			s.noteReason(sess, endRemoteGone)
		} else {
			s.noteReason(sess, endFailed)
		}
		return
	}
	s.mu.Lock()
	sess.ownAnswered = true
	s.mu.Unlock()
	if _, err := s.waitReady(sess, time.Time{}); err != nil {
		if errors.Is(sess.ctx.Err(), context.Canceled) {
			return
		}
		switch {
		case errors.Is(err, errCodec):
			s.noteReason(sess, endCodec)
		case errors.Is(err, errGone):
			s.noteReason(sess, endRemoteGone)
		default:
			s.noteReason(sess, endTimeout)
		}
		return
	}
	if err := s.bridge(sess); err != nil && s.reason(sess) == "" && !errors.Is(err, context.Canceled) {
		s.noteReason(sess, endFailed)
	}
}

func (s *Server) waitReady(sess *session, deadline time.Time) (vowifi.Call, error) {
	if deadline.IsZero() {
		deadline = time.Now().Add(s.dialTimeout)
	}
	for {
		if err := sess.ctx.Err(); err != nil {
			return vowifi.Call{}, err
		}
		if time.Now().After(deadline) {
			return vowifi.Call{}, errTimeout
		}
		call, live, err := s.lookupCall(sess.backendID)
		if err == nil {
			if !live || call.EndedAt != nil || normState(call.State) == "failed" || normState(call.State) == "ended" {
				return vowifi.Call{}, errGone
			}
			if normState(call.State) == "active" && call.MediaReady {
				if !supportedCodec(call.Codec) {
					return call, errCodec
				}
				return call, nil
			}
		}
		timer := time.NewTimer(s.pollEvery)
		select {
		case <-sess.ctx.Done():
			timer.Stop()
			return vowifi.Call{}, sess.ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Server) bridge(sess *session) error {
	if err := sess.ctx.Err(); err != nil {
		return err
	}
	media, release, err := s.opts.Backend.OpenMedia(sess.ctx, sess.backendID, sess.owner)
	if release != nil && (err != nil || media == nil || !supportedCodec(media.Codec())) {
		release()
		release = nil
	}
	if err != nil {
		return err
	}
	if media == nil || !supportedCodec(media.Codec()) {
		return errCodec
	}
	s.mu.Lock()
	if sess.dead || sess.ctx.Err() != nil || sess.endReason != "" {
		s.mu.Unlock()
		if release != nil {
			release()
		}
		return errors.New("sip: session ended before media")
	}
	sess.media = media
	sess.release = release
	sess.mediaOn = true
	s.rtpSess = sess
	if !sess.rtpInit {
		sess.ssrc = randomUint32()
		if sess.ssrc == 0 || sess.ssrc == 0x12345678 {
			sess.ssrc = 0xA1B2C3D4
		}
		sess.txSeq = uint16(randomUint32())
		sess.txTS = randomUint32()
		sess.rtpInit = true
	}
	s.mu.Unlock()
	s.pumpDown(sess)
	if err := sess.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Server) pumpDown(sess *session) {
	var pending []int16
	frame := make([]int16, rtpFrame)
	for {
		samples, err := sess.media.ReadPCM(sess.ctx)
		if err != nil {
			return
		}
		pending = append(pending, samples...)
		for len(pending) >= rtpFrame {
			copy(frame, pending[:rtpFrame])
			pending = append([]int16(nil), pending[rtpFrame:]...)
			s.writeRTP(sess, encodePCMU(frame))
		}
	}
}

func (s *Server) writeRTP(sess *session, payload []byte) {
	s.mu.Lock()
	if sess.dead || !sess.mediaOn || sess.rtpRemote == nil || s.rtp == nil {
		s.mu.Unlock()
		return
	}
	sess.txSeq++
	seq := sess.txSeq
	sess.txTS += rtpFrame
	ts := sess.txTS
	ssrc := sess.ssrc
	remote := cloneAddr(sess.rtpRemote)
	s.mu.Unlock()
	_, _ = s.rtp.WriteToUDP(buildRTP(seq, ts, ssrc, payload), remote)
}

func (s *Server) onRTP(addr *net.UDPAddr, packet []byte) {
	pt, seq, payload, ok := parseRTP(packet)
	if !ok || pt != 0 || len(payload) == 0 || len(payload) > rtpMaxAudio {
		return
	}
	s.mu.Lock()
	sess := s.rtpSess
	if sess == nil || sess.dead || !sess.mediaOn || sess.media == nil || !addrEqual(sess.rtpRemote, addr) {
		s.mu.Unlock()
		return
	}
	if sess.seqInit && int16(seq-sess.lastSeq) <= 0 {
		s.mu.Unlock()
		return
	}
	sess.seqInit = true
	sess.lastSeq = seq
	media := sess.media
	s.mu.Unlock()
	_ = media.WritePCM(decodePCMU(payload))
}

func (s *Server) maintain() {
	s.mu.Lock()
	s.evictNoncesLocked()
	s.evictTxLocked()
	if s.reg != nil && !time.Now().Before(s.reg.expires) {
		s.reg = nil
	}
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()

	calls, err := s.backendCalls()
	callsOK := err == nil
	now := time.Now()
	for _, sess := range sessions {
		s.maintainSession(sess, calls, callsOK, now)
	}
	if callsOK {
		s.maybeInbound(calls)
	}
}

func (s *Server) maintainSession(sess *session, calls []vowifi.Call, callsOK bool, now time.Time) {
	s.mu.Lock()
	if sess.dead {
		if now.Sub(sess.deadAt) > 8*time.Second {
			delete(s.sessions, sess.sipCallID)
			if sess.backendID != "" && s.byBackend[sess.backendID] == sess {
				delete(s.byBackend, sess.backendID)
			}
		}
		s.mu.Unlock()
		return
	}
	dir := sess.direction
	backendID := sess.backendID
	answerStarted := sess.answerStarted
	dialog := sess.dialogConfirmed
	ackSeen := sess.ackSeen
	gotProv := sess.gotProvisional
	endReason := sess.endReason
	created := sess.created
	nextInvite := sess.nextInvite
	rawInvite := append([]byte(nil), sess.inviteRaw...)
	peer := cloneAddr(sess.peer)
	last := append([]byte(nil), sess.lastResp...)
	next200 := sess.next200
	acceptedAt := sess.acceptedAt
	s.mu.Unlock()
	if endReason != "" {
		return
	}

	if dir == dirInbound && !answerStarted && callsOK {
		state, live := findCall(calls, backendID)
		if !live {
			s.cancelUnanswered(sess, endRemoteGone)
			return
		}
		if normState(state) == "active" {
			s.cancelUnanswered(sess, endExternal)
			return
		}
	}
	if dir == dirInbound && !answerStarted && !dialog && len(rawInvite) > 0 {
		if !gotProv && !nextInvite.IsZero() && now.After(nextInvite) {
			s.writeTo(peer, rawInvite)
			s.mu.Lock()
			delay := sess.inviteDelay * 2
			if delay <= 0 || delay > s.t2 {
				delay = s.t2
			}
			sess.inviteDelay = delay
			sess.nextInvite = now.Add(delay)
			s.mu.Unlock()
		}
		if now.Sub(created) > s.inviteWait {
			s.cancelUnanswered(sess, endTimeout)
			return
		}
	}
	if dir == dirOutbound && dialog && !ackSeen && len(last) > 0 && !next200.IsZero() && now.After(next200) {
		s.writeTo(peer, last)
		s.mu.Lock()
		delay := sess.finalDelay * 2
		if delay <= 0 || delay > s.t2 {
			delay = s.t2
		}
		sess.finalDelay = delay
		sess.next200 = now.Add(delay)
		s.mu.Unlock()
	}
	if dir == dirOutbound && dialog && !ackSeen && !acceptedAt.IsZero() && now.Sub(acceptedAt) > s.ackWait {
		s.stop(sess, endAckTimeout)
		return
	}
	if dialog && callsOK && backendID != "" && (answerStarted || dir == dirOutbound) {
		_, live := findCall(calls, backendID)
		if !live {
			s.stop(sess, endRemoteGone)
		}
	}
}

func (s *Server) finishSession(sess *session) {
	sess.doneOnce.Do(func() {
		if sess.cancel != nil {
			sess.cancel()
		}
		s.mu.Lock()
		release := sess.release
		sess.release = nil
		sess.media = nil
		sess.mediaOn = false
		if s.rtpSess == sess {
			s.rtpSess = nil
		}
		if sess.endReason == "" {
			if s.runCtx != nil && s.runCtx.Err() != nil {
				sess.endReason = endShutdown
			} else {
				sess.endReason = endFailed
			}
		}
		reason := sess.endReason
		id := sess.backendID
		dir := sess.direction
		answered := sess.ownAnswered
		confirmed := sess.dialogConfirmed
		peerBye := sess.peerBye
		sess.dead = true
		sess.deadAt = time.Now()
		s.mu.Unlock()

		if release != nil {
			release()
		}
		ringing := false
		if id != "" {
			if state, live, ok := s.lookupState(id); ok && live && ringingState(state) {
				ringing = true
			}
		}
		if id != "" && shouldHangup(dir, answered, reason, ringing) {
			s.hangupCall(id)
		}
		if confirmed && !peerBye {
			s.sendBYE(sess)
		}
	})
}

func shouldHangup(dir string, answered bool, reason string, ringing bool) bool {
	switch reason {
	case endExternal, endRemoteGone:
		return false
	}
	if dir == dirOutbound || answered {
		return true
	}
	if !ringing {
		return false
	}
	switch reason {
	case endReject, endTimeout, endBadSDP, endCancel, endShutdown, endBye, endFailed, endAckTimeout, endCodec:
		return true
	default:
		return false
	}
}

func (s *Server) hangupCall(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	if _, ok := s.hung[id]; ok {
		s.mu.Unlock()
		return
	}
	s.hung[id] = struct{}{}
	if len(s.hung) > 512 {
		s.hung = map[string]struct{}{id: struct{}{}}
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.opts.Backend.Hangup(ctx, id)
}

func (s *Server) lookupCall(id string) (vowifi.Call, bool, error) {
	calls, err := s.backendCalls()
	if err != nil {
		return vowifi.Call{}, false, err
	}
	for _, call := range calls {
		if call.ID == id {
			return call, callLive(call), nil
		}
	}
	return vowifi.Call{}, false, nil
}

func (s *Server) lookupState(id string) (string, bool, bool) {
	call, live, err := s.lookupCall(id)
	if err != nil {
		return "", false, false
	}
	return call.State, live, true
}

func (s *Server) backendBusy() (busy bool, unknown bool) {
	calls, err := s.backendCalls()
	if err != nil {
		return false, true
	}
	for _, call := range calls {
		if callLive(call) {
			return true, false
		}
	}
	return false, false
}

func (s *Server) localBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localBusyLocked()
}

func (s *Server) localBusyLocked() bool {
	for _, sess := range s.sessions {
		if sess != nil && !sess.dead {
			return true
		}
	}
	return false
}

func (s *Server) sendSession(sess *session, code int, reason string, extra []string, body string, final bool) bool {
	s.mu.Lock()
	if final && (sess.finalSent || sess.dead || sess.endReason == endCancel) {
		s.mu.Unlock()
		return false
	}
	if final && sess.endReason != "" && code < 400 {
		s.mu.Unlock()
		return false
	}
	raw := s.renderInviteResponse(sess, code, reason, extra, body)
	if final {
		sess.finalSent = true
	}
	if code >= 200 && code < 300 {
		sess.dialogConfirmed = true
		sess.acceptedAt = time.Now()
		sess.finalDelay = s.t1
		sess.next200 = time.Now().Add(s.t1)
	}
	sess.lastResp = raw
	if tx := s.txs[sess.txKey]; tx != nil {
		tx.last = append([]byte(nil), raw...)
		tx.touched = time.Now()
	} else {
		s.storeTxLocked(sess.txKey, sess.peer, raw)
	}
	peer := cloneAddr(sess.peer)
	s.mu.Unlock()
	s.writeTo(peer, raw)
	return true
}

func (s *Server) sendProgress(sess *session, code int, reason string) {
	s.sendSession(sess, code, reason, []string{s.contactLine()}, "", false)
}

func (s *Server) sendFinal(sess *session, code int, reason string) bool {
	extra := []string{s.contactLine(), "Allow: REGISTER, INVITE, ACK, BYE, CANCEL, OPTIONS"}
	body := ""
	if code == 200 {
		extra = append(extra, "Content-Type: application/sdp")
		body = s.offerSDP()
	}
	return s.sendSession(sess, code, reason, extra, body, true)
}

func (s *Server) send487(sess *session) {
	raw := s.renderInviteResponse(sess, 487, "Request Terminated", []string{s.contactLine()}, "")
	s.mu.Lock()
	sess.lastResp = raw
	if tx := s.txs[sess.txKey]; tx != nil {
		tx.last = append([]byte(nil), raw...)
		tx.touched = time.Now()
	} else {
		s.storeTxLocked(sess.txKey, sess.peer, raw)
	}
	peer := cloneAddr(sess.peer)
	s.mu.Unlock()
	s.writeTo(peer, raw)
}

func (s *Server) renderInviteResponse(sess *session, code int, reason string, extra []string, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", code, reason)
	if len(sess.inviteVia) == 0 {
		fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=%s\r\n", s.viaHost(), sess.inviteBranch)
	}
	for _, via := range sess.inviteVia {
		fmt.Fprintf(&b, "Via: %s\r\n", via)
	}
	fmt.Fprintf(&b, "From: %s\r\n", sess.inviteFrom)
	fmt.Fprintf(&b, "To: %s\r\n", ensureTag(sess.inviteTo, sess.localTag))
	fmt.Fprintf(&b, "Call-ID: %s\r\n", sess.sipCallID)
	fmt.Fprintf(&b, "CSeq: %d INVITE\r\n", sess.inviteCSeq)
	for _, line := range extra {
		if strings.TrimSpace(line) != "" {
			fmt.Fprintf(&b, "%s\r\n", line)
		}
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return []byte(b.String())
}

func (s *Server) buildInviteLocked(sess *session) []byte {
	body := s.offerSDP()
	var b strings.Builder
	fmt.Fprintf(&b, "INVITE %s SIP/2.0\r\n", sess.initialInviteURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=%s;rport\r\n", s.viaHost(), sess.inviteBranch)
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s\r\n", ensureTag(sess.localFrom, sess.localTag))
	fmt.Fprintf(&b, "To: %s\r\n", sess.localTo)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", sess.sipCallID)
	fmt.Fprintf(&b, "CSeq: %d INVITE\r\n", sess.inviteCSeq)
	fmt.Fprintf(&b, "Contact: <sip:%s@%s>\r\n", s.username, s.viaHost())
	b.WriteString("Content-Type: application/sdp\r\n")
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return []byte(b.String())
}

func (s *Server) sendAck(sess *session, non2xx bool) {
	sess.signalMu.Lock()
	s.mu.Lock()
	branch := "z9hG4bK" + randomHex(6)
	uri := sess.remoteDialogTarget
	if non2xx {
		branch = sess.inviteBranch
		uri = sess.initialInviteURI
	}
	if uri == "" {
		uri = sess.initialInviteURI
	}
	raw := s.buildDialogRequestLocked(sess, "ACK", sess.inviteCSeq, branch, uri)
	peer := cloneAddr(sess.peer)
	s.mu.Unlock()
	s.writeTo(peer, raw)
	s.mu.Lock()
	if !non2xx {
		sess.ackSent = true
	}
	pending := sess.pendingBye && sess.ackSent
	s.mu.Unlock()
	sess.signalMu.Unlock()
	if pending {
		s.sendBYE(sess)
	}
}

func (s *Server) sendBYE(sess *session) {
	sess.signalMu.Lock()
	defer sess.signalMu.Unlock()
	s.mu.Lock()
	// A concurrent cleanup can see the dialog before the response handler
	// has sent its first 2xx ACK. Defer BYE until that ACK is on the socket.
	if sess.direction == dirInbound && !sess.ackSent {
		sess.pendingBye = true
		s.mu.Unlock()
		return
	}
	if sess.byeSent || sess.remoteTag == "" || sess.localTag == "" {
		s.mu.Unlock()
		return
	}
	sess.byeSent = true
	cseq := sess.inviteCSeq + 1
	if sess.nextCSeq > cseq {
		cseq = sess.nextCSeq
	}
	sess.nextCSeq = cseq + 1
	uri := sess.remoteDialogTarget
	if uri == "" {
		uri = sess.initialInviteURI
	}
	raw := s.buildDialogRequestLocked(sess, "BYE", cseq, "z9hG4bK"+randomHex(6), uri)
	peer := cloneAddr(sess.peer)
	s.mu.Unlock()
	s.writeTo(peer, raw)
}

func (s *Server) sendCancel(sess *session) {
	s.mu.Lock()
	if sess.cancelSent || sess.dialogConfirmed || sess.dead {
		s.mu.Unlock()
		return
	}
	sess.cancelSent = true
	// Early CANCEL carries the INVITE branch and CSeq and no remote tag yet.
	to := sess.localTo
	if sess.remoteTag != "" {
		to = ensureTag(sess.localTo, sess.remoteTag)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CANCEL %s SIP/2.0\r\n", sess.initialInviteURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=%s;rport\r\n", s.viaHost(), sess.inviteBranch)
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s\r\n", ensureTag(sess.localFrom, sess.localTag))
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", sess.sipCallID)
	fmt.Fprintf(&b, "CSeq: %d CANCEL\r\n", sess.inviteCSeq)
	b.WriteString("Content-Length: 0\r\n\r\n")
	raw := []byte(b.String())
	peer := cloneAddr(sess.peer)
	s.mu.Unlock()
	s.writeTo(peer, raw)
}

func (s *Server) buildDialogRequestLocked(sess *session, method string, cseq int, branch, uri string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", method, uri)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=%s;rport\r\n", s.viaHost(), branch)
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s\r\n", ensureTag(sess.localFrom, sess.localTag))
	fmt.Fprintf(&b, "To: %s\r\n", ensureTag(sess.localTo, sess.remoteTag))
	fmt.Fprintf(&b, "Call-ID: %s\r\n", sess.sipCallID)
	fmt.Fprintf(&b, "CSeq: %d %s\r\n", cseq, method)
	if method != "ACK" {
		fmt.Fprintf(&b, "Contact: <sip:%s@%s>\r\n", s.username, s.viaHost())
	}
	b.WriteString("Content-Length: 0\r\n\r\n")
	return []byte(b.String())
}

func (s *Server) reason(sess *session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sess.endReason
}

// Mark cancellation while the INVITE is still unanswered, before sending
// CANCEL. A late 200 must take the ACK/BYE path, never start backend Answer.
func (s *Server) cancelUnanswered(sess *session, reason string) {
	s.mu.Lock()
	if sess.dead || sess.answerStarted || sess.endReason != "" {
		s.mu.Unlock()
		return
	}
	sess.endReason = reason
	s.mu.Unlock()
	s.sendCancel(sess)
	s.cancelSession(sess)
}

func (s *Server) noteReason(sess *session, reason string) {
	s.mu.Lock()
	if sess.endReason == "" {
		sess.endReason = reason
	}
	s.mu.Unlock()
}

func (s *Server) stop(sess *session, reason string) {
	s.noteReason(sess, reason)
	s.cancelSession(sess)
}

func (s *Server) cancelSession(sess *session) {
	s.mu.Lock()
	cancel := sess.cancel
	dialCancel := sess.dialCancel
	s.mu.Unlock()
	if dialCancel != nil {
		dialCancel()
	}
	if cancel != nil {
		cancel()
	}
}

func dialogMatch(sess *session, msg *sipMessage, remote *net.UDPAddr) bool {
	if sess.localTag == "" || sess.remoteTag == "" || !addrEqual(sess.peer, remote) {
		return false
	}
	return msg.fromTag == sess.remoteTag && msg.toTag == sess.localTag
}

func callLive(call vowifi.Call) bool {
	if call.EndedAt != nil {
		return false
	}
	switch normState(call.State) {
	case "", "ended", "failed", "idle":
		return false
	default:
		return true
	}
}

func incomingRinging(call vowifi.Call) bool {
	if !callLive(call) {
		return false
	}
	switch normState(call.Direction) {
	case "incoming", "inbound", "in":
	default:
		return false
	}
	return ringingState(call.State)
}

func ringingState(state string) bool {
	switch normState(state) {
	case "ringing", "incoming", "early", "alerting":
		return true
	default:
		return false
	}
}

func normState(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func supportedCodec(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PCMU", "PCMA":
		return true
	default:
		return false
	}
}

func validNumber(value string) bool {
	if len(value) < 2 || len(value) > 32 {
		return false
	}
	for index, r := range value {
		switch {
		case r >= '0' && r <= '9', r == '*', r == '#':
		case index == 0 && r == '+':
		default:
			return false
		}
	}
	return true
}

func sanitizeUser(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\r\n\"<>@;,") {
		return "unknown"
	}
	return value
}

func findCall(calls []vowifi.Call, id string) (string, bool) {
	for _, call := range calls {
		if call.ID == id {
			return call.State, callLive(call)
		}
	}
	return "", false
}
