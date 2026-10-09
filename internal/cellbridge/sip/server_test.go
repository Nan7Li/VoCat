package sip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

const testPassword = "pw-DO-NOT-LEAK"

type fakeBackend struct {
	mu             sync.Mutex
	calls          []vowifi.Call
	dials          int
	answers        []string
	hangups        []string
	owners         []string
	opens          int
	releases       int
	blockDial      bool
	lateOK         bool
	dialLie        bool
	ignoreCtx      bool
	deadlineWithID bool
	entered        chan struct{}
	release        chan struct{}
	mediaCodec     string
	media          *fakeMedia
}

func newFake() *fakeBackend {
	return &fakeBackend{entered: make(chan struct{}, 4), release: make(chan struct{})}
}

// webLiveCall is an already-active web session. SIP cleanup of a failed dial
// must not hang it up.
func webLiveCall() vowifi.Call {
	return vowifi.Call{
		ID: "web-live", Number: "+15550001", Direction: "outgoing", State: "active",
		MediaReady: true, Codec: "PCMU", StartedAt: time.Now(),
	}
}

func (f *fakeBackend) Calls(context.Context) ([]vowifi.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]vowifi.Call(nil), f.calls...), nil
}

func (f *fakeBackend) Dial(ctx context.Context, number string) (vowifi.Call, error) {
	f.mu.Lock()
	f.dials++
	id := fmt.Sprintf("out-%d", f.dials)
	block := f.blockDial
	late := f.lateOK
	lie := f.dialLie
	ignore := f.ignoreCtx
	deadlineID := f.deadlineWithID
	f.mu.Unlock()
	call := vowifi.Call{ID: id, Number: number, Direction: "outgoing", State: "dialing", StartedAt: time.Now()}
	if deadlineID {
		f.add(call)
		f.add(webLiveCall())
		return call, context.DeadlineExceeded
	}
	if block {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		if ignore {
			f.add(webLiveCall())
			<-f.release
			f.add(call)
			return call, nil
		}
		select {
		case <-ctx.Done():
			if !late {
				return vowifi.Call{}, ctx.Err()
			}
			f.add(call)
			return call, nil
		case <-f.release:
		}
	}
	f.add(call)
	if lie {
		call.State = "active"
		call.MediaReady = true
		call.Codec = "PCMU"
	}
	return call, nil
}

func (f *fakeBackend) Answer(_ context.Context, id string) (vowifi.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, id)
	for i := range f.calls {
		if f.calls[i].ID != id {
			continue
		}
		f.calls[i].State = "active"
		f.calls[i].MediaReady = true
		if f.calls[i].Codec == "" {
			f.calls[i].Codec = "PCMU"
		}
		return f.calls[i], nil
	}
	return vowifi.Call{}, errors.New("no ringing call")
}

func (f *fakeBackend) Hangup(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hangupLocked(id)
}

func (f *fakeBackend) hangupLocked(id string) error {
	f.hangups = append(f.hangups, id)
	now := time.Now()
	for i := range f.calls {
		if f.calls[i].ID != id {
			continue
		}
		f.calls[i].State = "ended"
		f.calls[i].EndedAt = &now
		f.calls[i].MediaReady = false
	}
	return nil
}

func (f *fakeBackend) OpenMedia(_ context.Context, id, owner string) (vowifi.CallMedia, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var call *vowifi.Call
	for i := range f.calls {
		if f.calls[i].ID == id {
			call = &f.calls[i]
			break
		}
	}
	if call == nil || call.EndedAt != nil || !call.MediaReady {
		return nil, nil, errors.New("media not ready")
	}
	codec := call.Codec
	if f.mediaCodec != "" {
		codec = f.mediaCodec
	}
	if codec == "" {
		codec = "PCMU"
	}
	media := &fakeMedia{codec: codec, down: make(chan []int16, 4)}
	f.media = media
	f.opens++
	f.owners = append(f.owners, owner)
	var once sync.Once
	release := func() {
		once.Do(func() {
			f.mu.Lock()
			f.releases++
			f.mu.Unlock()
			media.mu.Lock()
			media.closed = true
			media.mu.Unlock()
		})
	}
	return media, release, nil
}

func (f *fakeBackend) setExtra(id string) {
	f.add(vowifi.Call{
		ID: id, Number: "+15550002", Direction: "outgoing", State: "active",
		MediaReady: true, Codec: "PCMU", StartedAt: time.Now(),
	})
}

func (f *fakeBackend) add(call vowifi.Call) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

func (f *fakeBackend) setCalls(calls []vowifi.Call) {
	f.mu.Lock()
	f.calls = append([]vowifi.Call(nil), calls...)
	f.mu.Unlock()
}

func (f *fakeBackend) update(id string, fn func(*vowifi.Call)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.calls {
		if f.calls[i].ID == id {
			fn(&f.calls[i])
		}
	}
}

func (f *fakeBackend) snapshot() (dials, opens, releases int, answers, hangups, owners []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials, f.opens, f.releases, append([]string(nil), f.answers...), append([]string(nil), f.hangups...), append([]string(nil), f.owners...)
}

type fakeMedia struct {
	mu     sync.Mutex
	codec  string
	down   chan []int16
	up     [][]int16
	closed bool
}

func (m *fakeMedia) Codec() string { return m.codec }

func (m *fakeMedia) ReadPCM(ctx context.Context) ([]int16, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case samples, ok := <-m.down:
		if !ok {
			return nil, context.Canceled
		}
		return samples, nil
	}
}

func (m *fakeMedia) WritePCM(samples []int16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("closed")
	}
	m.up = append(m.up, append([]int16(nil), samples...))
	return nil
}

func (m *fakeMedia) uplink() [][]int16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]int16, len(m.up))
	for i := range m.up {
		out[i] = append([]int16(nil), m.up[i]...)
	}
	return out
}

type phone struct {
	t        *testing.T
	conn     *net.UDPConn
	dest     *net.UDPAddr
	user     string
	pass     string
	domain   string
	cseq     int
	nc       int
	realmVal string
	nonceVal string
	mu       sync.Mutex
	rx       []string
	seen     int
	wake     chan struct{}
	last     string
}

func newPhone(t *testing.T, dest string) *phone {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", dest)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	p := &phone{
		t: t, conn: conn, dest: addr, user: "alice", pass: testPassword,
		domain: "127.0.0.1", wake: make(chan struct{}, 1),
	}
	go p.readLoop()
	t.Cleanup(func() { _ = conn.Close() })
	return p
}

func (p *phone) readLoop() {
	buf := make([]byte, 65535)
	for {
		_ = p.conn.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
		n, _, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		p.mu.Lock()
		p.rx = append(p.rx, string(append([]byte(nil), buf[:n]...)))
		p.mu.Unlock()
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *phone) wait(d time.Duration, pred func(string) bool) (string, bool) {
	p.t.Helper()
	deadline := time.Now().Add(d)
	for {
		p.mu.Lock()
		for p.seen < len(p.rx) {
			msg := p.rx[p.seen]
			p.seen++
			if pred(msg) {
				p.mu.Unlock()
				return msg, true
			}
		}
		p.mu.Unlock()
		if time.Now().After(deadline) {
			return "", false
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-p.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (p *phone) dump() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.rx, "\n---\n")
}

func (p *phone) must(msg string, ok bool) string {
	p.t.Helper()
	if !ok {
		p.t.Fatalf("missing SIP message (%s)\n%s", msg, p.dump())
	}
	return msg
}

func (p *phone) local() string {
	return p.conn.LocalAddr().String()
}

func (p *phone) transmit(raw string) {
	p.t.Helper()
	p.mu.Lock()
	p.last = raw
	p.mu.Unlock()
	if _, err := p.conn.WriteToUDP([]byte(raw), p.dest); err != nil {
		p.t.Fatal(err)
	}
}

func (p *phone) resend() {
	p.mu.Lock()
	raw := p.last
	p.mu.Unlock()
	if _, err := p.conn.WriteToUDP([]byte(raw), p.dest); err != nil {
		p.t.Fatal(err)
	}
}

func (p *phone) request(method, uri, toUser, password string, headers []string, body string, auth bool, authHeader string) string {
	p.t.Helper()
	p.mu.Lock()
	p.cseq++
	cseq := p.cseq
	nc := ""
	if auth {
		p.nc++
		nc = fmt.Sprintf("%08x", p.nc)
	}
	p.mu.Unlock()
	callID := randomHex(6) + "@127.0.0.1"
	branch := "z9hG4bK" + randomHex(6)
	fromTag := randomHex(4)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", method, uri)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;rport;branch=%s\r\n", p.local(), branch)
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: <sip:%s@%s>;tag=%s\r\n", p.user, p.domain, fromTag)
	fmt.Fprintf(&b, "To: <sip:%s@%s>\r\n", toUser, p.domain)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", callID)
	fmt.Fprintf(&b, "CSeq: %d %s\r\n", cseq, method)
	if authHeader != "" {
		fmt.Fprintf(&b, "Authorization: %s\r\n", authHeader)
	} else if auth {
		fmt.Fprintf(&b, "Authorization: %s\r\n", p.authorization(method, uri, password, nc))
	}
	for _, header := range headers {
		fmt.Fprintf(&b, "%s\r\n", header)
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(body), body)
	raw := b.String()
	p.transmit(raw)
	return raw
}

func (p *phone) authorization(method, uri, password, nc string) string {
	p.t.Helper()
	if p.muNonce() == "" {
		p.t.Fatal("nonce is empty")
	}
	cnonce := randomHex(4)
	resp := digestResponse(p.user, p.realm(), password, p.muNonce(), nc, cnonce, "auth", method, uri)
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5, cnonce="%s", nc=%s, qop=auth`,
		p.user, p.realm(), p.muNonce(), uri, resp, cnonce, nc)
}

func (p *phone) learnChallenge() (string, string) {
	p.t.Helper()
	p.request("REGISTER", "sip:"+p.domain, p.user, "", []string{
		"Contact: <sip:" + p.user + "@" + p.local() + ">",
	}, "", false, "")
	msg := p.must(p.wait(2*time.Second, func(m string) bool {
		return statusCode(m) == 401 && cseqMethod(m) == "REGISTER"
	}))
	realm, nonce := challengeOf(msg)
	if realm == "" || nonce == "" || strings.Contains(msg, testPassword) {
		p.t.Fatalf("challenge = realm %q nonce %q\n%s", realm, nonce, msg)
	}
	p.mu.Lock()
	p.realmVal = realm
	p.nonceVal = nonce
	p.mu.Unlock()
	return realm, nonce
}

func (p *phone) register() {
	p.t.Helper()
	if p.muNonce() == "" {
		p.learnChallenge()
	}
	uri := "sip:" + p.domain
	p.request("REGISTER", uri, p.user, p.pass, []string{
		"Contact: <sip:" + p.user + "@" + p.local() + ">;expires=600",
		"Expires: 600",
	}, "", true, "")
	p.must(p.wait(2*time.Second, func(m string) bool {
		return statusCode(m) == 200 && cseqMethod(m) == "REGISTER"
	}))
}

func (p *phone) muNonce() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.nonceVal
}

func (p *phone) realm() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.realmVal == "" {
		return digestRealm
	}
	return p.realmVal
}

type harness struct {
	t    *testing.T
	srv  *Server
	fb   *fakeBackend
	ua   *phone
	rtp  *net.UDPConn
	done chan error
}

func (h *harness) must(msg string, ok bool) string {
	return h.ua.must(msg, ok)
}

func startHarness(t *testing.T, fb *fakeBackend, dial time.Duration) *harness {
	t.Helper()
	if fb == nil {
		fb = newFake()
	}
	srv, err := New(Options{
		ListenAddr:    "127.0.0.1:0",
		AdvertisedIP:  "127.0.0.1",
		Username:      "alice",
		Password:      testPassword,
		RTPListenAddr: "127.0.0.1:0",
		Backend:       fb,
		DialTimeout:   dial,
		PollInterval:  15 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- srv.Run(ctx)
		close(finished)
	}()
	ua := newPhone(t, srv.Addr().String())
	rtp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, srv: srv, fb: fb, ua: ua, rtp: rtp, done: done}
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		_ = rtp.Close()
		select {
		case <-finished:
		case <-time.After(4 * time.Second):
			t.Errorf("Run did not return")
		}
	})
	return h
}

func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func statusCode(msg string) int {
	line := msg
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		line = msg[:i]
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "SIP/") {
		return 0
	}
	n, _ := strconv.Atoi(fields[1])
	return n
}

func cseqMethod(msg string) string {
	fields := strings.Fields(headerValue(msg, "CSeq"))
	if len(fields) != 2 {
		return ""
	}
	return strings.ToUpper(fields[1])
}

func requestMethod(msg string) string {
	line := msg
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		line = msg[:i]
	}
	fields := strings.Fields(line)
	if len(fields) < 3 || strings.HasPrefix(fields[0], "SIP/") {
		return ""
	}
	return strings.ToUpper(fields[0])
}

func headerValue(msg, name string) string {
	want := strings.ToLower(name) + ":"
	for _, line := range strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trim), want) {
			return strings.TrimSpace(trim[len(want):])
		}
	}
	return ""
}

func challengeOf(msg string) (realm, nonce string) {
	value := headerValue(msg, "WWW-Authenticate")
	_, rest, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok {
		return "", ""
	}
	items, err := parseDirectives(rest)
	if err != nil {
		return "", ""
	}
	return items["realm"], items["nonce"]
}

func messageBody(msg string) string {
	if i := strings.Index(msg, "\r\n\r\n"); i >= 0 {
		return msg[i+4:]
	}
	if i := strings.Index(msg, "\n\n"); i >= 0 {
		return msg[i+2:]
	}
	return ""
}

func audioPort(msg string) int {
	for _, line := range strings.Split(strings.ReplaceAll(messageBody(msg), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "m=audio ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				n, _ := strconv.Atoi(fields[1])
				return n
			}
		}
	}
	return 0
}

func sdpOffer(port int, ip string, formats string, rtpmap string) string {
	if ip == "" {
		ip = "127.0.0.1"
	}
	return fmt.Sprintf("v=0\r\no=- 0 0 IN IP4 %s\r\ns=-\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP %s\r\n%s", ip, ip, port, formats, rtpmap)
}

func (p *phone) invite(number, sdp string) string {
	p.t.Helper()
	uri := "sip:" + number + "@" + p.domain
	return p.request("INVITE", uri, number, p.pass, []string{
		"Contact: <sip:" + p.user + "@" + p.local() + ">",
		"Content-Type: application/sdp",
	}, sdp, true, "")
}

func (p *phone) ack(invite, ok string) {
	p.t.Helper()
	inv, err := parseMessage([]byte(invite))
	if err != nil {
		p.t.Fatal(err)
	}
	resp, err := parseMessage([]byte(ok))
	if err != nil {
		p.t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ACK %s SIP/2.0\r\n", inv.requestURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=z9hG4bK%s;rport\r\n", p.local(), randomHex(4))
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s\r\n", inv.from)
	fmt.Fprintf(&b, "To: %s\r\n", resp.to)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", inv.callID)
	fmt.Fprintf(&b, "CSeq: %d ACK\r\n", inv.cseqNum)
	b.WriteString("Content-Length: 0\r\n\r\n")
	p.transmit(b.String())
}

func (p *phone) bye(invite, ok string) {
	p.t.Helper()
	inv, err := parseMessage([]byte(invite))
	if err != nil {
		p.t.Fatal(err)
	}
	resp, err := parseMessage([]byte(ok))
	if err != nil {
		p.t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BYE %s SIP/2.0\r\n", inv.requestURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=z9hG4bK%s;rport\r\n", p.local(), randomHex(4))
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s\r\n", inv.from)
	fmt.Fprintf(&b, "To: %s\r\n", resp.to)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", inv.callID)
	fmt.Fprintf(&b, "CSeq: %d BYE\r\n", inv.cseqNum+1)
	b.WriteString("Content-Length: 0\r\n\r\n")
	p.transmit(b.String())
}

func (p *phone) cancelInvite(invite string) {
	p.t.Helper()
	inv, err := parseMessage([]byte(invite))
	if err != nil {
		p.t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CANCEL %s SIP/2.0\r\n", inv.requestURI)
	if len(inv.vias) > 0 {
		fmt.Fprintf(&b, "Via: %s\r\n", inv.vias[0])
	}
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s\r\n", inv.from)
	fmt.Fprintf(&b, "To: %s\r\n", inv.to)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", inv.callID)
	fmt.Fprintf(&b, "CSeq: %d CANCEL\r\n", inv.cseqNum)
	b.WriteString("Content-Length: 0\r\n\r\n")
	p.transmit(b.String())
}

func (p *phone) byeFromInbound(invite, remoteTag string) {
	p.t.Helper()
	inv, err := parseMessage([]byte(invite))
	if err != nil {
		p.t.Fatal(err)
	}
	// The phone is the UAS: From carries the tag it put on the 200, To carries
	// the tag from the server's INVITE.
	var b strings.Builder
	fmt.Fprintf(&b, "BYE %s SIP/2.0\r\n", inv.requestURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=z9hG4bK%s;rport\r\n", p.local(), randomHex(4))
	b.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&b, "From: %s\r\n", ensureTag(inv.to, remoteTag))
	fmt.Fprintf(&b, "To: %s\r\n", inv.from)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", inv.callID)
	fmt.Fprintf(&b, "CSeq: %d BYE\r\n", inv.cseqNum+1)
	b.WriteString("Content-Length: 0\r\n\r\n")
	p.transmit(b.String())
}

func invite200(invite, sdp, toTag string) string {
	msg, err := parseMessage([]byte(invite))
	if err != nil {
		return ""
	}
	return string(buildResponse(msg, 200, "OK", toTag, []string{
		"Contact: <sip:alice@127.0.0.1>",
		"Content-Type: application/sdp",
	}, sdp))
}

func pattern(seed int16) []int16 {
	samples := make([]int16, rtpFrame)
	for i := range samples {
		samples[i] = seed + int16(i*17)
	}
	return samples
}

func TestDefaultRTPListen(t *testing.T) {
	if defaultRTPListen != "0.0.0.0:40000" {
		t.Fatalf("default RTP listen = %s", defaultRTPListen)
	}
}

func TestNewRejectsIncompleteOptions(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("missing backend was accepted")
	}
	fb := newFake()
	if _, err := New(Options{Backend: fb, Username: "alice"}); err == nil {
		t.Fatal("missing password was accepted")
	}
}

func TestRegisterDigestReplayAndBinding(t *testing.T) {
	h := startHarness(t, newFake(), time.Second)
	realm, nonce := h.ua.learnChallenge()
	if realm != digestRealm || nonce == "" {
		t.Fatalf("realm %s nonce %s", realm, nonce)
	}
	uri := "sip:" + h.ua.domain
	h.ua.request("REGISTER", uri, h.ua.user, "wrong-password", []string{
		"Contact: <sip:alice@" + h.ua.local() + ">",
	}, "", true, "")
	if msg, ok := h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 403
	}); !ok {
		t.Fatalf("wrong password was not forbidden\n%s", h.ua.dump())
	} else if statusCode(msg) == 200 || h.srv.Status().Registered {
		t.Fatal("wrong password registered")
	}

	h.ua.request("REGISTER", uri, h.ua.user, "", []string{
		"Contact: <sip:alice@" + h.ua.local() + ">",
	}, "", false, "Bearer totally-fake")
	if _, ok := h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 403
	}); !ok {
		t.Fatalf("bearer authorization was accepted\n%s", h.ua.dump())
	}

	forged := fmt.Sprintf(`Digest username="alice", realm="%s", nonce="%s", uri="%s", response="00112233445566778899aabbccddeeff", algorithm=MD5, cnonce="abcd", nc=0000000a, qop=auth`, realm, nonce, uri)
	h.ua.request("REGISTER", uri, h.ua.user, "", []string{
		"Contact: <sip:alice@" + h.ua.local() + ">",
	}, "", false, forged)
	if _, ok := h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 403
	}); !ok || h.srv.Status().Registered {
		t.Fatalf("forged digest registered\n%s", h.ua.dump())
	}

	h.ua.request("REGISTER", uri, "mallory", h.ua.pass, []string{
		"Contact: <sip:mallory@" + h.ua.local() + ">",
	}, "", true, "")
	if _, ok := h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 403
	}); !ok || h.srv.Status().Registered {
		t.Fatalf("mismatched registration address was accepted\n%s", h.ua.dump())
	}

	h.ua.register()
	if !h.srv.Status().Registered || h.srv.Status().LocalRTPAddr == "" {
		t.Fatalf("status after register: %+v", h.srv.Status())
	}
	encoded, _ := json.Marshal(h.srv.Status())
	if strings.Contains(string(encoded), testPassword) || strings.Contains(fmt.Sprintf("%+v", h.srv.Status()), testPassword) {
		t.Fatal("status leaked the password")
	}

	h.ua.resend()
	if _, ok := h.ua.wait(time.Second, func(m string) bool {
		return statusCode(m) == 200 && cseqMethod(m) == "REGISTER"
	}); !ok {
		t.Fatalf("retransmission was not answered from cache\n%s", h.ua.dump())
	}
	h.ua.mu.Lock()
	previous := h.ua.last
	h.ua.mu.Unlock()
	auth := headerValue(previous, "Authorization")
	h.ua.request("REGISTER", uri, h.ua.user, "", []string{
		"Contact: <sip:alice@" + h.ua.local() + ">;expires=600",
	}, "", false, auth)
	if _, ok := h.ua.wait(time.Second, func(m string) bool {
		code := statusCode(m)
		return cseqMethod(m) == "REGISTER" && code == 403
	}); !ok {
		t.Fatalf("reused nc was accepted\n%s", h.ua.dump())
	}
	if !h.srv.Status().Registered {
		t.Fatal("replay cleared a good registration")
	}
}

func TestNonceExpiry(t *testing.T) {
	old := cfgNonceLife.Swap(int64(120 * time.Millisecond))
	t.Cleanup(func() { cfgNonceLife.Store(old) })
	h := startHarness(t, newFake(), time.Second)
	_, nonce := h.ua.learnChallenge()
	time.Sleep(200 * time.Millisecond)
	uri := "sip:" + h.ua.domain
	h.ua.request("REGISTER", uri, h.ua.user, h.ua.pass, []string{
		"Contact: <sip:alice@" + h.ua.local() + ">",
	}, "", true, "")
	msg := h.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 401
	}))
	if !strings.Contains(strings.ToLower(msg), "stale=true") {
		t.Fatalf("expired nonce was not stale: %s", msg)
	}
	if _, next := challengeOf(msg); next == "" || next == nonce {
		t.Fatalf("stale challenge did not rotate nonce: %s", msg)
	}
	if h.srv.Status().Registered {
		t.Fatal("expired nonce registered")
	}
}

func TestInviteAuthRetransmitCancelAndBusy(t *testing.T) {
	fb := newFake()
	fb.blockDial = true
	fb.lateOK = true
	h := startHarness(t, fb, 2*time.Second)
	sdp := sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n")
	uri := "sip:+15551212@127.0.0.1"
	// Valid digest before any registration must not dial.
	h.ua.learnChallenge()
	h.ua.request("INVITE", uri, "+15551212", h.ua.pass, []string{"Content-Type: application/sdp"}, sdp, true, "")
	h.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 403
	}))
	if dials, _, _, _, _, _ := fb.snapshot(); dials != 0 {
		t.Fatalf("unregistered invite dialed %d", dials)
	}

	h.ua.register()
	// From alone, after registration, is not enough.
	h.ua.request("INVITE", uri, "+15551212", "", []string{"Content-Type: application/sdp"}, sdp, false, "")
	h.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 401
	}))
	if dials, _, _, _, _, _ := fb.snapshot(); dials != 0 {
		t.Fatal("unauthenticated invite dialed")
	}

	other := newPhone(t, h.srv.Addr().String())
	other.mu.Lock()
	other.nonceVal = h.ua.muNonce()
	other.realmVal = h.ua.realm()
	other.nc = 40
	other.mu.Unlock()
	other.request("INVITE", uri, "+15551212", testPassword, []string{"Content-Type: application/sdp"}, sdp, true, "")
	h2 := other
	if _, ok := h2.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 403
	}); !ok {
		t.Fatalf("invite from an unbound source was accepted\n%s", other.dump())
	}
	if dials, _, _, _, _, _ := fb.snapshot(); dials != 0 {
		t.Fatal("unbound source dialed")
	}

	bad := []struct {
		name string
		sdp  string
	}{
		{"third-party", sdpOffer(4000, "203.0.113.9", "0", "a=rtpmap:0 PCMU/8000\r\n")},
		{"port-zero", sdpOffer(0, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n")},
		{"no-pcmu", sdpOffer(4000, "127.0.0.1", "8", "a=rtpmap:8 PCMA/8000\r\n")},
	}
	for _, tc := range bad {
		h.ua.invite("+15551212", tc.sdp)
		msg := h.must(h.ua.wait(time.Second, func(m string) bool {
			return cseqMethod(m) == "INVITE" && statusCode(m) == 488
		}))
		if statusCode(msg) == 200 {
			t.Fatalf("%s offered a fake success", tc.name)
		}
	}
	if dials, _, _, _, hangups, _ := fb.snapshot(); dials != 0 || len(hangups) != 0 {
		t.Fatalf("rejected SDP dialed or hung up: dials %d hangups %v", dials, hangups)
	}

	invite := h.ua.invite("+15551212", sdp)
	select {
	case <-fb.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not start")
	}
	h.ua.resend()
	time.Sleep(80 * time.Millisecond)
	if dials, _, _, _, _, _ := fb.snapshot(); dials != 1 {
		t.Fatalf("retransmission dialed %d times", dials)
	}

	// A second call must be busy and must not hang the reserved dial.
	h.ua.invite("+15550000", sdp)
	h.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 486
	}))
	if dials, _, _, _, hangups, _ := fb.snapshot(); dials != 1 || len(hangups) != 0 {
		t.Fatalf("busy path dials=%d hangups=%v", dials, hangups)
	}

	other.cancelInvite(invite)
	if _, ok := other.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "CANCEL" && statusCode(m) == 481
	}); !ok {
		t.Fatalf("forged CANCEL was not rejected\n%s", other.dump())
	}
	time.Sleep(50 * time.Millisecond)
	if dials, _, _, _, hangups, _ := fb.snapshot(); dials != 1 || len(hangups) != 0 {
		t.Fatalf("forged CANCEL affected the call dials=%d hangups=%v", dials, hangups)
	}

	h.ua.cancelInvite(invite)
	h.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 487
	}))
	if _, ok := h.ua.wait(200*time.Millisecond, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 200
	}); ok {
		t.Fatalf("cancelled invite was answered\n%s", h.ua.dump())
	}
	waitUntil(t, 2*time.Second, "late dial hangup", func() bool {
		_, _, _, _, hangups, _ := fb.snapshot()
		return len(hangups) == 1 && hangups[0] == "out-1"
	})
}

func TestNo200UntilActiveMedia(t *testing.T) {
	fb := newFake()
	fb.dialLie = true
	h := startHarness(t, fb, 1200*time.Millisecond)
	h.ua.register()
	h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
	waitUntil(t, time.Second, "dial", func() bool {
		dials, _, _, _, _, _ := fb.snapshot()
		return dials == 1
	})
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := h.ua.wait(20*time.Millisecond, func(m string) bool {
			return cseqMethod(m) == "INVITE" && statusCode(m) == 200
		}); ok {
			t.Fatalf("200 sent before Calls reported active media\n%s", h.ua.dump())
		}
	}
	fb.update("out-1", func(call *vowifi.Call) {
		call.State = "active"
		call.MediaReady = false
		call.Codec = "PCMU"
	})
	fb.update("out-1", func(call *vowifi.Call) {})
	fb.setExtra("web-live")
	msg := h.must(h.ua.wait(2*time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) >= 400
	}))
	if statusCode(msg) == 200 {
		t.Fatal("media-not-ready call got 200")
	}
	waitUntil(t, time.Second, "hangup of our dial", func() bool {
		_, _, _, _, hangups, _ := fb.snapshot()
		return len(hangups) == 1 && hangups[0] == "out-1"
	})
	fb.mu.Lock()
	defer fb.mu.Unlock()
	for _, call := range fb.calls {
		if call.ID == "web-live" && call.EndedAt != nil {
			t.Fatal("cleanup hung up a different live call")
		}
	}
}

func TestBackendBusyDoesNotHangExisting(t *testing.T) {
	fb := newFake()
	fb.setCalls([]vowifi.Call{{
		ID: "web-1", Number: "+15550001", Direction: "outgoing", State: "active",
		MediaReady: true, Codec: "PCMU", StartedAt: time.Now(),
	}})
	h := startHarness(t, fb, time.Second)
	h.ua.register()
	h.ua.invite("+15551212", sdpOffer(9, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
	msg := h.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 486
	}))
	if statusCode(msg) == 200 {
		t.Fatal("busy line was offered a call")
	}
	dials, _, _, _, hangups, _ := fb.snapshot()
	if dials != 0 || len(hangups) != 0 {
		t.Fatalf("busy dials=%d hangups=%v", dials, hangups)
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if len(fb.calls) != 1 || fb.calls[0].ID != "web-1" || fb.calls[0].EndedAt != nil {
		t.Fatalf("existing call changed: %+v", fb.calls)
	}
}

func TestCodecReject(t *testing.T) {
	for _, codec := range []string{"AMR", "AMR-WB", "G729"} {
		t.Run(codec, func(t *testing.T) {
			fb := newFake()
			h := startHarness(t, fb, 2*time.Second)
			h.ua.register()
			h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
			waitUntil(t, time.Second, "dial", func() bool {
				dials, _, _, _, _, _ := fb.snapshot()
				return dials == 1
			})
			fb.update("out-1", func(call *vowifi.Call) {
				call.State = "active"
				call.MediaReady = true
				call.Codec = codec
			})
			msg := h.must(h.ua.wait(2*time.Second, func(m string) bool {
				return cseqMethod(m) == "INVITE" && statusCode(m) == 488
			}))
			if statusCode(msg) == 200 || strings.Contains(messageBody(msg), "PCMU") && statusCode(msg) == 200 {
				t.Fatal("unsupported codec was accepted")
			}
			waitUntil(t, time.Second, "codec cleanup", func() bool {
				_, opens, _, _, hangups, _ := fb.snapshot()
				return opens == 0 && len(hangups) == 1 && hangups[0] == "out-1"
			})
		})
	}
}

func TestPCMUBridgeAndDialogBinding(t *testing.T) {
	fb := newFake()
	h := startHarness(t, fb, 3*time.Second)
	h.ua.register()
	invite := h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0 8", "a=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\n"))
	waitUntil(t, time.Second, "dial", func() bool {
		dials, _, _, _, _, _ := fb.snapshot()
		return dials == 1
	})
	fb.update("out-1", func(call *vowifi.Call) {
		call.State = "active"
		call.MediaReady = true
		call.Codec = "PCMU"
	})
	okMsg := h.must(h.ua.wait(2*time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 200
	}))
	if !strings.Contains(strings.ToUpper(messageBody(okMsg)), "PCMU") || audioPort(okMsg) == 0 {
		t.Fatalf("200 SDP is not PCMU: %s", messageBody(okMsg))
	}
	h.ua.ack(invite, okMsg)
	h.ua.ack(invite, okMsg)
	waitUntil(t, 2*time.Second, "one media owner", func() bool {
		_, opens, _, _, _, owners := fb.snapshot()
		return opens == 1 && len(owners) == 1 && strings.HasPrefix(owners[0], "cellbridge-sip/")
	})

	serverRTP, err := net.ResolveUDPAddr("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(audioPort(okMsg))))
	if err != nil {
		t.Fatal(err)
	}
	down := pattern(1000)
	waitUntil(t, time.Second, "media", func() bool {
		fb.mu.Lock()
		ok := fb.media != nil
		fb.mu.Unlock()
		return ok
	})
	fb.mu.Lock()
	media := fb.media
	fb.mu.Unlock()
	media.down <- append([]int16(nil), down...)
	rtpPkt := readRTP(t, h.rtp, time.Second)
	pt, seq1, payload, ok := parseRTP(rtpPkt)
	if !ok || pt != 0 || rtpPkt[0]>>6 != 2 {
		t.Fatalf("downlink RTP invalid pt=%d ok=%v raw=%x", pt, ok, rtpPkt)
	}
	ssrc := uint32(rtpPkt[8])<<24 | uint32(rtpPkt[9])<<16 | uint32(rtpPkt[10])<<8 | uint32(rtpPkt[11])
	if ssrc == 0 || ssrc == 0x12345678 {
		t.Fatalf("ssrc = %#x", ssrc)
	}
	if got := decodePCMU(payload); !sameSamples(got, decodePCMU(encodePCMU(down))) {
		t.Fatalf("downlink PCM = %v", got[:8])
	}
	media.down <- pattern(2000)
	second := readRTP(t, h.rtp, time.Second)
	_, seq2, _, ok := parseRTP(second)
	if !ok || seq2 != seq1+1 {
		t.Fatalf("seq %d then %d", seq1, seq2)
	}
	ts1 := uint32(rtpPkt[4])<<24 | uint32(rtpPkt[5])<<16 | uint32(rtpPkt[6])<<8 | uint32(rtpPkt[7])
	ts2 := uint32(second[4])<<24 | uint32(second[5])<<16 | uint32(second[6])<<8 | uint32(second[7])
	if ts2 != ts1+rtpFrame {
		t.Fatalf("timestamp advanced %d", ts2-ts1)
	}

	up := pattern(-400)
	sendRTP(t, h.rtp, serverRTP, 50, 0, encodePCMU(up))
	sendRTP(t, h.rtp, serverRTP, 50, 0, encodePCMU(up))
	sendRTP(t, h.rtp, serverRTP, 49, 0, encodePCMU(pattern(1)))
	sendRTP(t, h.rtp, serverRTP, 51, 8, encodePCMU(pattern(2)))
	other, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	sendRTP(t, other, serverRTP, 60, 0, encodePCMU(pattern(3)))
	waitUntil(t, time.Second, "first uplink frame", func() bool {
		return len(media.uplink()) >= 1
	})
	// This packet already has its own RTP header, CSRC list, and extension.
	// sendRTP would wrap it again and hide the header from the parser.
	extended := withCSRCAndExtension(encodePCMU(pattern(4)))
	if _, err := h.rtp.WriteToUDP(extended, serverRTP); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, time.Second, "two accepted uplink frames", func() bool {
		return len(media.uplink()) >= 2
	})
	frames := media.uplink()
	if len(frames) != 2 {
		t.Fatalf("accepted %d uplink frames, want 2", len(frames))
	}
	if !sameSamples(frames[0], decodePCMU(encodePCMU(up))) || !sameSamples(frames[1], decodePCMU(encodePCMU(pattern(4)))) {
		t.Fatal("uplink samples were not the accepted packets")
	}

	// A BYE from another UDP source, even with the right tags, must not hang up.
	bye := byeBytes(t, h.ua, invite, okMsg)
	if _, err := other.WriteToUDP([]byte(bye), h.ua.dest); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, _, _, _, hangups, _ := fb.snapshot(); len(hangups) != 0 {
		t.Fatalf("forged BYE hung up %v", hangups)
	}
	h.ua.bye(invite, okMsg)
	waitUntil(t, 2*time.Second, "bye cleanup", func() bool {
		_, _, releases, _, hangups, _ := fb.snapshot()
		return len(hangups) == 1 && hangups[0] == "out-1" && releases == 1
	})
	if h.srv.Status().Active != 0 {
		t.Fatalf("active dialogs after BYE: %d", h.srv.Status().Active)
	}
}

func TestPCMABackendBridgesToPCMU(t *testing.T) {
	fb := newFake()
	h := startHarness(t, fb, 3*time.Second)
	h.ua.register()
	invite := h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
	waitUntil(t, time.Second, "dial", func() bool {
		dials, _, _, _, _, _ := fb.snapshot()
		return dials == 1
	})
	fb.update("out-1", func(call *vowifi.Call) {
		call.State = "active"
		call.MediaReady = true
		call.Codec = "PCMA"
	})
	okMsg := h.must(h.ua.wait(2*time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 200
	}))
	h.ua.ack(invite, okMsg)
	waitUntil(t, 2*time.Second, "pcma media", func() bool {
		_, opens, _, _, _, _ := fb.snapshot()
		fb.mu.Lock()
		codec := ""
		if fb.media != nil {
			codec = fb.media.codec
		}
		fb.mu.Unlock()
		return opens == 1 && codec == "PCMA"
	})
	fb.mu.Lock()
	media := fb.media
	fb.mu.Unlock()
	samples := pattern(300)
	media.down <- samples
	pkt := readRTP(t, h.rtp, time.Second)
	pt, _, payload, ok := parseRTP(pkt)
	if !ok || pt != 0 || !sameSamples(decodePCMU(payload), decodePCMU(encodePCMU(samples))) {
		t.Fatalf("PCMA backend was not bridged as PCMU pt=%d", pt)
	}
}

func TestInboundAnswerOnceLate200AndExternal(t *testing.T) {
	fb := newFake()
	h := startHarness(t, fb, 3*time.Second)
	h.ua.register()
	fb.setCalls([]vowifi.Call{{
		ID: "in-1", Number: "+15550999", Direction: "incoming", State: "ringing", StartedAt: time.Now(),
	}})
	invite := h.must(h.ua.wait(2*time.Second, func(m string) bool { return requestMethod(m) == "INVITE" }))
	if !strings.Contains(invite, "+15550999") || !strings.Contains(strings.ToUpper(messageBody(invite)), "PCMU") {
		t.Fatalf("inbound INVITE missing caller or PCMU: %s", invite)
	}
	sdp := sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n")
	ok := invite200(invite, sdp, "phone-tag")
	h.ua.transmit(ok)
	h.ua.transmit(ok)
	waitUntil(t, 2*time.Second, "single answer", func() bool {
		_, _, _, answers, _, _ := fb.snapshot()
		return len(answers) >= 1
	})
	time.Sleep(120 * time.Millisecond)
	if _, _, _, answers, _, owners := fb.snapshot(); len(answers) != 1 || answers[0] != "in-1" {
		t.Fatalf("answers = %v", answers)
	} else if len(owners) > 1 {
		t.Fatalf("owners = %v", owners)
	}
	ack := h.must(h.ua.wait(time.Second, func(m string) bool { return requestMethod(m) == "ACK" }))
	if !strings.Contains(ack, "phone-tag") {
		t.Fatalf("ACK missing dialog tag: %s", ack)
	}
	h.ua.byeFromInbound(invite, "phone-tag")
	waitUntil(t, 2*time.Second, "inbound bye", func() bool {
		_, _, releases, _, hangups, _ := fb.snapshot()
		return len(hangups) == 1 && hangups[0] == "in-1" && releases == 1
	})

	// External answer: do not hang up, and a late 200 must not revive the dialog.
	fb.setCalls([]vowifi.Call{{
		ID: "in-ext", Number: "+15550888", Direction: "incoming", State: "ringing", StartedAt: time.Now(),
	}})
	invite = h.must(h.ua.wait(2*time.Second, func(m string) bool {
		return requestMethod(m) == "INVITE" && strings.Contains(m, "+15550888")
	}))
	fb.update("in-ext", func(call *vowifi.Call) {
		call.State = "active"
		call.MediaReady = true
		call.Codec = "PCMU"
	})
	h.must(h.ua.wait(2*time.Second, func(m string) bool { return requestMethod(m) == "CANCEL" }))
	h.ua.transmit(invite200(invite, sdp, "late-tag"))
	h.must(h.ua.wait(time.Second, func(m string) bool {
		return requestMethod(m) == "ACK" && strings.Contains(m, "late-tag")
	}))
	h.must(h.ua.wait(time.Second, func(m string) bool {
		return requestMethod(m) == "BYE" && strings.Contains(m, "late-tag")
	}))
	time.Sleep(100 * time.Millisecond)
	_, opens, _, answers, hangups, _ := fb.snapshot()
	if len(answers) != 1 || opens != 1 {
		t.Fatalf("late 200 revived the session answers=%v opens=%d", answers, opens)
	}
	for _, id := range hangups {
		if id == "in-ext" {
			t.Fatal("external active call was hung up")
		}
	}
	if h.srv.Status().Active != 0 {
		t.Fatalf("revived active=%d", h.srv.Status().Active)
	}
}

func TestInboundTimeoutHangsRingingOnly(t *testing.T) {
	old := cfgInviteWait.Swap(int64(350 * time.Millisecond))
	t.Cleanup(func() { cfgInviteWait.Store(old) })
	fb := newFake()
	h := startHarness(t, fb, time.Second)
	h.ua.register()
	fb.setCalls([]vowifi.Call{{
		ID: "in-ring", Number: "+15550777", Direction: "incoming", State: "ringing", StartedAt: time.Now(),
	}})
	h.must(h.ua.wait(2*time.Second, func(m string) bool { return requestMethod(m) == "INVITE" }))
	waitUntil(t, 2*time.Second, "ringing cleanup", func() bool {
		_, _, _, answers, hangups, _ := fb.snapshot()
		return len(answers) == 0 && len(hangups) == 1 && hangups[0] == "in-ring"
	})
}

func TestAckTimeoutCleansOwnedCall(t *testing.T) {
	old := cfgAckWait.Swap(int64(250 * time.Millisecond))
	t.Cleanup(func() { cfgAckWait.Store(old) })
	fb := newFake()
	h := startHarness(t, fb, 2*time.Second)
	h.ua.register()
	h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
	waitUntil(t, time.Second, "dial", func() bool {
		dials, _, _, _, _, _ := fb.snapshot()
		return dials == 1
	})
	fb.update("out-1", func(call *vowifi.Call) {
		call.State = "active"
		call.MediaReady = true
		call.Codec = "PCMU"
	})
	h.must(h.ua.wait(2*time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 200
	}))
	waitUntil(t, 2*time.Second, "ack timeout cleanup", func() bool {
		_, opens, _, _, hangups, _ := fb.snapshot()
		return opens == 0 && len(hangups) == 1 && hangups[0] == "out-1"
	})
	if _, ok := h.ua.wait(time.Second, func(m string) bool { return requestMethod(m) == "BYE" }); !ok {
		t.Fatalf("ACK timeout did not BYE\n%s", h.ua.dump())
	}
}

func TestMessageIsNotImplemented(t *testing.T) {
	h := startHarness(t, newFake(), time.Second)
	h.ua.learnChallenge()
	h.ua.request("MESSAGE", "sip:+15551212@127.0.0.1", "+15551212", testPassword, []string{
		"Content-Type: text/plain",
	}, "hello", true, "")
	msg := h.must(h.ua.wait(time.Second, func(m string) bool { return cseqMethod(m) == "MESSAGE" }))
	if statusCode(msg) != 501 || statusCode(msg) == 200 {
		t.Fatalf("MESSAGE status = %d\n%s", statusCode(msg), msg)
	}
	if dials, _, _, _, hangups, _ := h.fb.snapshot(); dials != 0 || len(hangups) != 0 {
		t.Fatalf("MESSAGE touched the line dials=%d hangups=%v", dials, hangups)
	}
}

func TestCloseConcurrent(t *testing.T) {
	fb := newFake()
	fb.blockDial = true
	h := startHarness(t, fb, 3*time.Second)
	h.ua.register()
	h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
	select {
	case <-fb.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not start before close")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.srv.Status()
			_ = h.srv.Addr()
			h.ua.request("OPTIONS", "sip:127.0.0.1", "alice", "", nil, "", false, "")
		}()
	}
	wg.Wait()
	var closeWG sync.WaitGroup
	var closeMu sync.Mutex
	var closeErr error
	for i := 0; i < 4; i++ {
		closeWG.Add(1)
		go func() {
			defer closeWG.Done()
			if err := h.srv.Close(); err != nil {
				closeMu.Lock()
				closeErr = err
				closeMu.Unlock()
			}
		}()
	}
	closeWG.Wait()
	closeMu.Lock()
	err := closeErr
	closeMu.Unlock()
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-h.done:
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return after Close")
	}
	if err := h.srv.Close(); err != nil {
		t.Fatal(err)
	}
}

func readRTP(t *testing.T, conn *net.UDPConn, d time.Duration) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 2048)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read RTP: %v", err)
	}
	return append([]byte(nil), buf[:n]...)
}

func sendRTP(t *testing.T, conn *net.UDPConn, dest *net.UDPAddr, seq uint16, pt byte, payload []byte) {
	t.Helper()
	pkt := buildRTP(seq, uint32(seq)*rtpFrame, 0x1111, payload)
	if pt != 0 {
		pkt[1] = pt
	}
	if _, err := conn.WriteToUDP(pkt, dest); err != nil {
		t.Fatal(err)
	}
}

func withCSRCAndExtension(payload []byte) []byte {
	// Version 2, extension, one CSRC, one extension word.
	header := 12 + 4 + 4 + 4
	pkt := make([]byte, header+len(payload))
	pkt[0] = 0x91
	pkt[1] = 0
	pkt[2], pkt[3] = 0, 52
	copy(pkt[header:], payload)
	// extension length in 32-bit words at bytes 18-19 of the whole packet:
	// csrc occupies 12:16, extension profile 16:18, length 18:20.
	pkt[18], pkt[19] = 0, 1
	return pkt
}

func sameSamples(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func byeBytes(t *testing.T, p *phone, invite, ok string) string {
	t.Helper()
	inv, err := parseMessage([]byte(invite))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := parseMessage([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BYE %s SIP/2.0\r\n", inv.requestURI)
	fmt.Fprintf(&b, "Via: SIP/2.0/UDP %s;branch=z9hG4bKforged;rport\r\n", p.local())
	fmt.Fprintf(&b, "From: %s\r\n", inv.from)
	fmt.Fprintf(&b, "To: %s\r\n", resp.to)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", inv.callID)
	fmt.Fprintf(&b, "CSeq: %d BYE\r\n", inv.cseqNum+1)
	b.WriteString("Content-Length: 0\r\n\r\n")
	return b.String()
}
