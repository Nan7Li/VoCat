// Adapted from CellBridge gateway/internal/sip/server.go
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: one Halo backend is fixed for the life of the server.
// REGISTER is Digest authenticated and bound to the UDP source. A new
// INVITE reserves a session before Dial and never hangs up a call it does
// not own. MESSAGE is rejected instead of being reported as sent.

package sip

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vocat/internal/vowifi"
)

const (
	defaultListen    = "0.0.0.0:5060"
	defaultRTPListen = "0.0.0.0:40000"

	dirOutbound = "outbound"
	dirInbound  = "inbound"

	endBye        = "bye"
	endCancel     = "cancel"
	endTimeout    = "timeout"
	endAckTimeout = "ack-timeout"
	endCodec      = "codec"
	endBadSDP     = "bad-sdp"
	endReject     = "reject"
	endExternal   = "external"
	endRemoteGone = "remote-gone"
	endFailed     = "failed"
	endShutdown   = "shutdown"
)

var (
	errBadMessage = errors.New("sip: malformed message")
	errBadSDP     = errors.New("sip: unacceptable sdp")
	errTimeout    = errors.New("sip: timed out waiting for active media")
	errGone       = errors.New("sip: call ended")
	errCodec      = errors.New("sip: backend codec is not bridgeable")

	// cfg* are copied into Server at New. Tests swap them before New.
	cfgAckWait    atomic.Int64
	cfgInviteWait atomic.Int64
	cfgT1         atomic.Int64
	cfgNonceLife  atomic.Int64
)

func init() {
	cfgAckWait.Store(int64(32 * time.Second))
	cfgInviteWait.Store(int64(32 * time.Second))
	cfgT1.Store(int64(500 * time.Millisecond))
	cfgNonceLife.Store(int64(5 * time.Minute))
}

// Backend is one already-selected Halo line. The SIP server never picks a
// device and never opens the application database or an AT port.
type Backend interface {
	Calls(context.Context) ([]vowifi.Call, error)
	Dial(context.Context, string) (vowifi.Call, error)
	Answer(context.Context, string) (vowifi.Call, error)
	Hangup(context.Context, string) error
	OpenMedia(context.Context, string, string) (vowifi.CallMedia, func(), error)
}

// Options configures one SIP listener. Password is kept off Status.
type Options struct {
	ListenAddr    string
	AdvertisedIP  string
	Username      string
	Password      string
	RTPListenAddr string
	Backend       Backend
	DialTimeout   time.Duration
	PollInterval  time.Duration
}

// Status is safe to show. It has no password and no Authorization header.
type Status struct {
	Registered   bool      `json:"registered"`
	Active       int       `json:"active"`
	LocalRTPAddr string    `json:"localRTPAddr"`
	ListenAddr   string    `json:"listenAddr,omitempty"`
	Peer         string    `json:"peer,omitempty"`
	Expires      time.Time `json:"expires,omitempty"`
}

type registration struct {
	username   string
	addr       *net.UDPAddr
	contact    string
	requestURI string
	expires    time.Time
}

type serverTx struct {
	remote  *net.UDPAddr
	last    []byte
	created time.Time
	touched time.Time
}

// Server is one UDP SIP gateway bound to one Backend.
type Server struct {
	opts        Options
	username    string
	password    string
	realm       string
	advertised  string
	dialTimeout time.Duration
	pollEvery   time.Duration
	ackWait     time.Duration
	inviteWait  time.Duration
	nonceLife   time.Duration
	t1          time.Duration
	t2          time.Duration

	conn    *net.UDPConn
	rtp     *net.UDPConn
	addr    net.Addr
	rtpAddr *net.UDPAddr
	rtpText string

	mu        sync.Mutex
	closed    bool
	running   bool
	stopping  bool
	cancel    context.CancelFunc
	runCtx    context.Context
	done      chan struct{}
	reg       *registration
	sessions  map[string]*session
	byBackend map[string]*session
	txs       map[txKey]*serverTx
	nonces    map[string]*nonceState
	hung      map[string]struct{}
	rtpSess   *session
	loops     sync.WaitGroup
	workers   sync.WaitGroup
}

// New binds the SIP and RTP sockets. Run starts the loops and Close releases them.
func New(opts Options) (*Server, error) {
	if opts.Backend == nil {
		return nil, errors.New("sip: backend is required")
	}
	username := strings.TrimSpace(opts.Username)
	if username == "" || opts.Password == "" {
		return nil, errors.New("sip: username and password are required")
	}
	if strings.ContainsAny(username, " \t\r\n\"<>") || strings.ContainsAny(opts.Password, "\r\n") {
		return nil, errors.New("sip: invalid credentials")
	}
	if opts.DialTimeout < 0 || opts.PollInterval < 0 {
		return nil, errors.New("sip: negative timeout")
	}
	listen := opts.ListenAddr
	if listen == "" {
		listen = defaultListen
	}
	rtpListen := opts.RTPListenAddr
	if rtpListen == "" {
		rtpListen = defaultRTPListen
	}
	sipAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return nil, fmt.Errorf("sip: listen address: %w", err)
	}
	rtpAddr, err := net.ResolveUDPAddr("udp", rtpListen)
	if err != nil {
		return nil, fmt.Errorf("sip: rtp address: %w", err)
	}
	conn, err := net.ListenUDP("udp", sipAddr)
	if err != nil {
		return nil, fmt.Errorf("sip: listen: %w", err)
	}
	rtpConn, err := net.ListenUDP("udp", rtpAddr)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sip: rtp listen: %w", err)
	}
	local := conn.LocalAddr().(*net.UDPAddr)
	rtpLocal := rtpConn.LocalAddr().(*net.UDPAddr)
	advertised := strings.TrimSpace(opts.AdvertisedIP)
	if advertised == "" {
		if local.IP == nil || local.IP.IsUnspecified() {
			_ = conn.Close()
			_ = rtpConn.Close()
			return nil, errors.New("sip: advertised IP is required")
		}
		advertised = normalizeIP(local.IP).String()
	}
	if net.ParseIP(advertised) == nil {
		_ = conn.Close()
		_ = rtpConn.Close()
		return nil, errors.New("sip: advertised IP is invalid")
	}
	dialTimeout := opts.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 45 * time.Second
	}
	poll := opts.PollInterval
	if poll == 0 {
		poll = 200 * time.Millisecond
	}
	ackWait := time.Duration(cfgAckWait.Load())
	if ackWait <= 0 {
		ackWait = 32 * time.Second
	}
	inviteWait := time.Duration(cfgInviteWait.Load())
	if inviteWait <= 0 {
		inviteWait = 32 * time.Second
	}
	t1 := time.Duration(cfgT1.Load())
	if t1 <= 0 {
		t1 = 500 * time.Millisecond
	}
	nonceLife := time.Duration(cfgNonceLife.Load())
	if nonceLife <= 0 {
		nonceLife = 5 * time.Minute
	}
	t2 := t1 * 8
	if t2 > 4*time.Second {
		t2 = 4 * time.Second
	}
	password := opts.Password
	opts.Password = ""
	return &Server{
		opts:        opts,
		username:    username,
		password:    password,
		realm:       digestRealm,
		advertised:  advertised,
		dialTimeout: dialTimeout,
		pollEvery:   poll,
		ackWait:     ackWait,
		inviteWait:  inviteWait,
		nonceLife:   nonceLife,
		t1:          t1,
		t2:          t2,
		conn:        conn,
		rtp:         rtpConn,
		addr:        local,
		rtpAddr:     rtpLocal,
		rtpText:     rtpLocal.String(),
		done:        make(chan struct{}),
		sessions:    make(map[string]*session),
		byBackend:   make(map[string]*session),
		txs:         make(map[txKey]*serverTx),
		nonces:      make(map[string]*nonceState),
		hung:        make(map[string]struct{}),
	}, nil
}

// Run reads SIP until ctx is cancelled or Close is called.
func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("sip: server is closed")
	}
	if s.running {
		s.mu.Unlock()
		return errors.New("sip: server is already running")
	}
	s.running = true
	runCtx, cancel := context.WithCancel(ctx)
	s.runCtx = runCtx
	s.cancel = cancel
	s.mu.Unlock()

	defer s.finishRun()
	s.loops.Add(2)
	go s.rtpLoop()
	go s.pollLoop()
	return s.readLoop()
}

// Close stops the loops, hangs up calls this server owns, and releases sockets.
// It is safe to call more than once.
func (s *Server) Close() error {
	s.mu.Lock()
	running := s.running
	cancel := s.cancel
	s.closed = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.rtp != nil {
		_ = s.rtp.Close()
	}
	if running {
		<-s.done
	}
	return nil
}

// Addr is the bound SIP address. It stays valid after Close.
func (s *Server) Addr() net.Addr {
	if s == nil {
		return nil
	}
	return s.addr
}

// Status reports registration and active dialogs without the password.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Status{LocalRTPAddr: s.rtpText}
	if s.addr != nil {
		status.ListenAddr = s.addr.String()
	}
	if s.reg != nil && time.Now().Before(s.reg.expires) {
		status.Registered = true
		status.Expires = s.reg.expires
		if s.reg.addr != nil {
			status.Peer = s.reg.addr.String()
		}
	}
	for _, sess := range s.sessions {
		if sess != nil && !sess.dead {
			status.Active++
		}
	}
	return status
}

func (s *Server) finishRun() {
	s.mu.Lock()
	s.stopping = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.rtp != nil {
		_ = s.rtp.Close()
	}
	s.loops.Wait()
	s.workers.Wait()
	s.sweep()
	close(s.done)
}

func (s *Server) sweep() {
	s.mu.Lock()
	left := make([]*session, 0)
	for _, sess := range s.sessions {
		if sess != nil && !sess.dead {
			left = append(left, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range left {
		s.noteReason(sess, endShutdown)
		s.finishSession(sess)
	}
}

func (s *Server) readLoop() error {
	buf := make([]byte, 65535)
	for {
		if err := s.runCtx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if s.runCtx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		packet := append([]byte(nil), buf[:n]...)
		s.handlePacket(packet, addr)
	}
}

func (s *Server) rtpLoop() {
	defer s.loops.Done()
	buf := make([]byte, 2048)
	for {
		if s.runCtx.Err() != nil {
			return
		}
		_ = s.rtp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, addr, err := s.rtp.ReadFromUDP(buf)
		if err != nil {
			if s.runCtx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		packet := append([]byte(nil), buf[:n]...)
		s.onRTP(addr, packet)
	}
}

func (s *Server) pollLoop() {
	defer s.loops.Done()
	ticker := time.NewTicker(s.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.runCtx.Done():
			return
		case <-ticker.C:
			s.maintain()
		}
	}
}

func (s *Server) worker(fn func()) bool {
	s.mu.Lock()
	if s.stopping || s.closed {
		s.mu.Unlock()
		return false
	}
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		fn()
	}()
	return true
}

func (s *Server) handlePacket(packet []byte, remote *net.UDPAddr) {
	if len(packet) == 0 || len(packet) > 20000 || remote == nil {
		return
	}
	msg, err := parseMessage(packet)
	if err != nil {
		return
	}
	if msg.isResponse {
		s.handleResponse(msg, remote)
		return
	}
	switch msg.method {
	case "REGISTER":
		s.handleRegister(msg, remote)
	case "INVITE":
		s.handleInvite(msg, remote)
	case "ACK":
		s.handleAck(msg, remote)
	case "BYE":
		s.handleBye(msg, remote)
	case "CANCEL":
		s.handleCancel(msg, remote)
	case "OPTIONS":
		if s.resendCached(msg, remote) {
			return
		}
		s.reply(msg, remote, 200, "OK", "Allow: REGISTER, INVITE, ACK, BYE, CANCEL, OPTIONS")
	case "MESSAGE":
		if s.resendCached(msg, remote) {
			return
		}
		s.reply(msg, remote, 501, "Not Implemented", "Allow: REGISTER, INVITE, ACK, BYE, CANCEL, OPTIONS")
	default:
		if s.resendCached(msg, remote) {
			return
		}
		s.reply(msg, remote, 501, "Not Implemented", "Allow: REGISTER, INVITE, ACK, BYE, CANCEL, OPTIONS")
	}
}

func (s *Server) handleRegister(msg *sipMessage, remote *net.UDPAddr) {
	if s.resendCached(msg, remote) {
		return
	}
	if msg.branch == "" || msg.callID == "" || msg.cseqMethod != "REGISTER" || len(msg.vias) == 0 {
		s.reply(msg, remote, 400, "Bad Request")
		return
	}
	if !s.authenticate(msg, remote) {
		return
	}
	expires := 3600
	if msg.expiresSet {
		expires = msg.expires
	}
	if expires < 0 {
		expires = 0
	}
	if expires > 3600 {
		expires = 3600
	}
	contact := msg.contact
	if expires > 0 && (contact == "" || strings.TrimSpace(contact) == "*") {
		s.reply(msg, remote, 400, "Bad Request")
		return
	}
	user, _ := parseNameAddr(contact)
	target := dialogURI(contact)
	if expires > 0 && (user != s.username || target == "") {
		s.reply(msg, remote, 403, "Forbidden")
		return
	}
	s.mu.Lock()
	if expires == 0 {
		s.reg = nil
	} else {
		s.reg = &registration{
			username:   s.username,
			addr:       cloneAddr(remote),
			contact:    contact,
			requestURI: target,
			expires:    time.Now().Add(time.Duration(expires) * time.Second),
		}
	}
	s.mu.Unlock()
	extra := []string{"Expires: " + strconv.Itoa(expires)}
	if contact != "" {
		extra = append([]string{"Contact: " + contact}, extra...)
	}
	s.reply(msg, remote, 200, "OK", extra...)
}

func (s *Server) reply(msg *sipMessage, remote *net.UDPAddr, code int, reason string, extra ...string) {
	raw := buildResponse(msg, code, reason, "", extra, "")
	s.remember(msg, remote, raw)
	s.writeTo(remote, raw)
}

func (s *Server) reject(msg *sipMessage, remote *net.UDPAddr, code int, reason string, extra ...string) {
	s.reply(msg, remote, code, reason, extra...)
}

func (s *Server) remember(msg *sipMessage, remote *net.UDPAddr, raw []byte) {
	if msg == nil || msg.method == "" || msg.method == "ACK" {
		return
	}
	key, ok := makeTxKey(msg, remote)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeTxLocked(key, remote, raw)
}

func (s *Server) storeTxLocked(key txKey, remote *net.UDPAddr, raw []byte) {
	if key.callID == "" || key.branch == "" || key.method == "" {
		return
	}
	tx := s.txs[key]
	if tx != nil && !addrEqual(tx.remote, remote) {
		// The key already includes the peer. Refuse a mismatched socket anyway.
		return
	}
	if tx == nil {
		tx = &serverTx{remote: cloneAddr(remote), created: time.Now()}
		s.txs[key] = tx
	}
	tx.last = append([]byte(nil), raw...)
	tx.touched = time.Now()
	s.evictTxLocked()
}

func (s *Server) evictTxLocked() {
	now := time.Now()
	for key, tx := range s.txs {
		if now.Sub(tx.touched) > 64*time.Second {
			delete(s.txs, key)
		}
	}
	for len(s.txs) > 128 {
		var oldest txKey
		var at time.Time
		found := false
		for key, tx := range s.txs {
			if !found || tx.created.Before(at) {
				oldest = key
				at = tx.created
				found = true
			}
		}
		if !found {
			return
		}
		delete(s.txs, oldest)
	}
}

// resendCached answers a UDP retransmission. It runs before nc replay
// protection so a lost 200 is not mistaken for a reused nonce count.
func (s *Server) resendCached(msg *sipMessage, remote *net.UDPAddr) bool {
	key, ok := makeTxKey(msg, remote)
	if !ok || msg.method == "" || msg.method == "ACK" {
		return false
	}
	s.mu.Lock()
	tx := s.txs[key]
	if tx == nil || !addrEqual(tx.remote, remote) || len(tx.last) == 0 {
		s.mu.Unlock()
		return false
	}
	raw := append([]byte(nil), tx.last...)
	s.mu.Unlock()
	s.writeTo(remote, raw)
	return true
}

func (s *Server) writeTo(addr *net.UDPAddr, raw []byte) {
	if s.conn == nil || addr == nil || len(raw) == 0 {
		return
	}
	_, _ = s.conn.WriteToUDP(raw, addr)
}

func (s *Server) viaHost() string {
	port := 5060
	if udp, ok := s.addr.(*net.UDPAddr); ok && udp.Port != 0 {
		port = udp.Port
	}
	return net.JoinHostPort(s.advertised, strconv.Itoa(port))
}

func (s *Server) contactLine() string {
	return "Contact: <sip:" + s.username + "@" + s.viaHost() + ">"
}

func (s *Server) offerSDP() string {
	ip := net.ParseIP(s.advertised)
	family := "IP4"
	if ip != nil && ip.To4() == nil {
		family = "IP6"
	}
	port := 0
	if s.rtpAddr != nil {
		port = s.rtpAddr.Port
	}
	return fmt.Sprintf("v=0\r\no=- 0 1 IN %s %s\r\ns=Halo\r\nc=IN %s %s\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=ptime:20\r\na=sendrecv\r\n",
		family, s.advertised, family, s.advertised, port)
}

func (s *Server) backendCalls() ([]vowifi.Call, error) {
	parent := s.runCtx
	if parent == nil || parent.Err() != nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	return s.opts.Backend.Calls(ctx)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		binary.LittleEndian.PutUint64(buf, uint64(time.Now().UnixNano()))
	}
	return hex.EncodeToString(buf)
}

func randomUint32() uint32 {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return binary.BigEndian.Uint32(buf[:])
}

func cloneAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{IP: cloneIP(normalizeIP(addr.IP)), Port: addr.Port, Zone: addr.Zone}
}

func addrEqual(a, b *net.UDPAddr) bool {
	if a == nil || b == nil || a.Port != b.Port {
		return false
	}
	return sameIP(a.IP, b.IP)
}
