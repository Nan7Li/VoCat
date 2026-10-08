package sip

// UDP regressions for peer-bound nonces, strictly increasing nc, transaction
// identity, dialog request-URIs, and dial deadlines. These sit beside the
// cases in review_protocol_test.go and do not replace them.

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func digestHeader(user, realm, password, nonce, method, uri, nc, cnonce string) string {
	resp := digestResponse(user, realm, password, nonce, nc, cnonce, "auth", method, uri)
	return `Digest username="` + user + `", realm="` + realm + `", nonce="` + nonce + `", uri="` + uri + `", response="` + resp + `", algorithm=MD5, cnonce="` + cnonce + `", nc=` + nc + `, qop=auth`
}

func rewriteCallIDStripAuth(raw, callID string) string {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	head, body, ok := strings.Cut(normalized, "\n\n")
	var lines []string
	for _, line := range strings.Split(head, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "authorization:") {
			continue
		}
		if strings.HasPrefix(lower, "call-id:") {
			lines = append(lines, "Call-ID: "+callID)
			continue
		}
		lines = append(lines, line)
	}
	out := strings.Join(lines, "\r\n") + "\r\n\r\n"
	if ok {
		out += body
	}
	return out
}

func registerContact(p *phone) []string {
	return []string{"Contact: <sip:" + p.user + "@" + p.local() + ">;expires=600", "Expires: 600"}
}

func TestNonceBoundToChallengePeer(t *testing.T) {
	h := startHarness(t, nil, time.Second)
	_, nonce := h.ua.learnChallenge()
	if h.srv.Status().Registered {
		t.Fatal("challenge registered the peer")
	}
	other := newPhone(t, h.srv.Addr().String())
	uri := "sip:" + h.ua.domain
	header := digestHeader(h.ua.user, digestRealm, testPassword, nonce, "REGISTER", uri, "00000001", "stolen-cnonce")
	other.request("REGISTER", uri, h.ua.user, "", registerContact(other), "", false, header)
	msg := other.must(other.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) >= 400
	}))
	if statusCode(msg) != 403 || h.srv.Status().Registered {
		t.Fatalf("peer B used A's nonce: %d registered=%v\n%s", statusCode(msg), h.srv.Status().Registered, msg)
	}
	h.ua.register()
	if !h.srv.Status().Registered || h.srv.Status().Peer != h.ua.local() {
		t.Fatalf("A could not register after the stolen nonce: %+v", h.srv.Status())
	}
	header = digestHeader(h.ua.user, digestRealm, testPassword, nonce, "REGISTER", uri, "00000001", "stolen-cnonce-2")
	other.request("REGISTER", uri, h.ua.user, "", registerContact(other), "", false, header)
	msg = other.must(other.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) >= 400
	}))
	if statusCode(msg) != 403 || h.srv.Status().Peer != h.ua.local() {
		t.Fatalf("peer B stole the binding: %d peer=%s\n%s", statusCode(msg), h.srv.Status().Peer, msg)
	}
}

func TestNonceCountRejectsDecreaseAndZero(t *testing.T) {
	h := startHarness(t, nil, time.Second)
	h.ua.learnChallenge()
	uri := "sip:" + h.ua.domain
	cnonce := "fixed-cnonce"
	send := func(nc string) {
		t.Helper()
		header := digestHeader(h.ua.user, h.ua.realm(), testPassword, h.ua.muNonce(), "REGISTER", uri, nc, cnonce)
		h.ua.request("REGISTER", uri, h.ua.user, "", registerContact(h.ua), "", false, header)
	}
	send("00000002")
	h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 200
	}))
	if !h.srv.Status().Registered {
		t.Fatal("nc=2 did not register")
	}
	h.ua.resend()
	h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 200
	}))
	for _, nc := range []string{"00000001", "00000000"} {
		send(nc)
		msg := h.ua.must(h.ua.wait(time.Second, func(m string) bool {
			return cseqMethod(m) == "REGISTER" && statusCode(m) >= 400
		}))
		if statusCode(msg) != 403 || !h.srv.Status().Registered {
			t.Fatalf("nc %s status=%d registered=%v\n%s", nc, statusCode(msg), h.srv.Status().Registered, msg)
		}
	}
}

func TestNewCallIDRequiresFreshAuth(t *testing.T) {
	h := startHarness(t, nil, time.Second)
	h.ua.register()
	h.ua.mu.Lock()
	original := h.ua.last
	h.ua.mu.Unlock()
	old, err := parseMessage([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	newID := "fresh-call-id@127.0.0.1"
	rewritten := rewriteCallIDStripAuth(original, newID)
	next, err := parseMessage([]byte(rewritten))
	if err != nil {
		t.Fatal(err)
	}
	if next.callID != newID || next.branch != old.branch || next.cseqNum != old.cseqNum || next.method != "REGISTER" || next.requestURI != old.requestURI {
		t.Fatalf("rewritten key fields changed: branch %s/%s cseq %d/%d uri %s/%s method %s", next.branch, old.branch, next.cseqNum, old.cseqNum, next.requestURI, old.requestURI, next.method)
	}
	if _, ok := next.first("authorization"); ok {
		t.Fatal("new Call-ID kept Authorization")
	}
	h.ua.transmit(rewritten)
	msg := h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) >= 200
	}))
	if statusCode(msg) != 401 || headerValue(msg, "Call-ID") != newID {
		t.Fatalf("new Call-ID reused the cached REGISTER: %s", strings.SplitN(msg, "\r\n", 2)[0]+" Call-ID="+headerValue(msg, "Call-ID"))
	}
	h.ua.transmit(original)
	cached := h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 200
	}))
	if headerValue(cached, "Call-ID") != old.callID {
		t.Fatalf("original retransmission Call-ID = %s, want %s", headerValue(cached, "Call-ID"), old.callID)
	}
}

func TestPeerTransactionsRetransmitIndependently(t *testing.T) {
	h := startHarness(t, nil, time.Second)
	uri := "sip:" + h.ua.domain
	rawA := h.ua.request("REGISTER", uri, h.ua.user, "", registerContact(h.ua), "", false, "")
	a401 := h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 401
	}))
	idA := headerValue(a401, "Call-ID")
	other := newPhone(t, h.srv.Addr().String())
	rawB := other.request("REGISTER", uri, other.user, "", registerContact(other), "", false, "")
	b401 := other.must(other.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 401
	}))
	idB := headerValue(b401, "Call-ID")
	if idA == "" || idB == "" || idA == idB {
		t.Fatalf("call-ids A=%s B=%s", idA, idB)
	}
	h.ua.transmit(rawA)
	againA := h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 401
	}))
	if headerValue(againA, "Call-ID") != idA {
		t.Fatalf("A retransmission Call-ID = %s", headerValue(againA, "Call-ID"))
	}
	other.transmit(rawB)
	againB := other.must(other.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 401
	}))
	if headerValue(againB, "Call-ID") != idB {
		t.Fatalf("B retransmission Call-ID = %s", headerValue(againB, "Call-ID"))
	}
	realm, nonce := challengeOf(a401)
	h.ua.mu.Lock()
	h.ua.realmVal = realm
	h.ua.nonceVal = nonce
	h.ua.mu.Unlock()
	h.ua.register()
	h.ua.mu.Lock()
	rawOK := h.ua.last
	h.ua.mu.Unlock()
	okMsg, err := parseMessage([]byte(rawOK))
	if err != nil {
		t.Fatal(err)
	}
	other.transmit(rawB)
	stillB := other.must(other.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) >= 200
	}))
	if statusCode(stillB) != 401 || headerValue(stillB, "Call-ID") != idB {
		t.Fatalf("B cache became A's success: %d %s", statusCode(stillB), headerValue(stillB, "Call-ID"))
	}
	h.ua.transmit(rawOK)
	cached := h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 200
	}))
	if headerValue(cached, "Call-ID") != okMsg.callID {
		t.Fatalf("A 200 retransmission Call-ID = %s, want %s", headerValue(cached, "Call-ID"), okMsg.callID)
	}
	h.ua.transmit(rawA)
	old := h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "REGISTER" && statusCode(m) == 401
	}))
	if headerValue(old, "Call-ID") != idA {
		t.Fatalf("A 401 was overwritten: %s", headerValue(old, "Call-ID"))
	}
}

func TestOutboundContactBYEUsesDialogURI(t *testing.T) {
	fb := newFake()
	h := startHarness(t, fb, 3*time.Second)
	h.ua.register()
	target := "sip:desk@203.0.113.9"
	sdp := sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n")
	invite := h.ua.request("INVITE", "sip:+15551212@127.0.0.1", "+15551212", h.ua.pass, []string{
		"Contact: <" + target + ">",
		"Content-Type: application/sdp",
	}, sdp, true, "")
	waitUntil(t, time.Second, "dial", func() bool {
		dials, _, _, _, _, _ := fb.snapshot()
		return dials == 1
	})
	fb.update("out-1", func(call *vowifi.Call) {
		call.State = "active"
		call.MediaReady = true
		call.Codec = "PCMU"
	})
	ok := h.ua.must(h.ua.wait(2*time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 200
	}))
	h.ua.ack(invite, ok)
	waitUntil(t, 2*time.Second, "media open", func() bool {
		_, opens, _, _, _, _ := fb.snapshot()
		return opens == 1
	})
	fb.update("out-1", func(call *vowifi.Call) {
		now := time.Now()
		call.State = "ended"
		call.EndedAt = &now
		call.MediaReady = false
	})
	bye := h.ua.must(h.ua.wait(2*time.Second, func(m string) bool {
		return requestMethod(m) == "BYE"
	}))
	if !strings.HasPrefix(bye, "BYE "+target+" SIP/2.0") {
		t.Fatalf("BYE request-URI = %s", strings.SplitN(bye, "\r\n", 2)[0])
	}
}

func TestDialDeadlineExceededHangsReturnedCall(t *testing.T) {
	fb := newFake()
	fb.deadlineWithID = true
	h := startHarness(t, fb, time.Second)
	h.ua.register()
	h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
	h.ua.must(h.ua.wait(time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 504
	}))
	waitUntil(t, time.Second, "deadline hangup", func() bool {
		_, _, _, _, hangups, _ := fb.snapshot()
		return len(hangups) == 1 && hangups[0] == "out-1"
	})
	waitUntil(t, time.Second, "deadline session released", func() bool {
		return h.srv.Status().Active == 0
	})
	time.Sleep(80 * time.Millisecond)
	_, _, _, _, hangups, _ := fb.snapshot()
	if len(hangups) != 1 || hangups[0] != "out-1" {
		t.Fatalf("hangups = %v", hangups)
	}
	assertWebLive(t, fb)
}

func TestIgnoredContextLateSuccess504BeforeRelease(t *testing.T) {
	fb := newFake()
	fb.blockDial = true
	fb.ignoreCtx = true
	h := startHarness(t, fb, 150*time.Millisecond)
	var once sync.Once
	release := func() { once.Do(func() { close(fb.release) }) }
	t.Cleanup(release)

	h.ua.register()
	h.ua.invite("+15551212", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "a=rtpmap:0 PCMU/8000\r\n"))
	select {
	case <-fb.entered:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	started := time.Now()
	h.ua.must(h.ua.wait(2*time.Second, func(m string) bool {
		return cseqMethod(m) == "INVITE" && statusCode(m) == 504
	}))
	if time.Since(started) > time.Second {
		t.Fatalf("504 arrived after %s, past the dial budget", time.Since(started))
	}
	dump := h.ua.dump()
	if strings.Contains(dump, "SIP/2.0 180") || strings.Contains(dump, "SIP/2.0 408") {
		t.Fatalf("late dial sent 180 or 408\n%s", dump)
	}
	waitUntil(t, time.Second, "expired session released", func() bool {
		return h.srv.Status().Active == 0
	})
	if _, _, _, _, hangups, _ := fb.snapshot(); len(hangups) != 0 {
		t.Fatalf("hangup before Dial returned: %v", hangups)
	}
	release()
	waitUntil(t, time.Second, "late hangup", func() bool {
		_, _, _, _, hangups, _ := fb.snapshot()
		return len(hangups) == 1 && hangups[0] == "out-1"
	})
	time.Sleep(80 * time.Millisecond)
	_, _, _, _, hangups, _ := fb.snapshot()
	if len(hangups) != 1 || hangups[0] != "out-1" {
		t.Fatalf("hangups after release = %v", hangups)
	}
	dump = h.ua.dump()
	if strings.Contains(dump, "SIP/2.0 180") || strings.Contains(dump, "SIP/2.0 408") {
		t.Fatalf("late result sent 180 or 408\n%s", dump)
	}
	assertWebLive(t, fb)
}

func assertWebLive(t *testing.T, fb *fakeBackend) {
	t.Helper()
	fb.mu.Lock()
	defer fb.mu.Unlock()
	var found bool
	for _, call := range fb.calls {
		if call.ID != "web-live" {
			continue
		}
		found = true
		if call.EndedAt != nil || call.State != "active" {
			t.Fatalf("web-live was cleaned up: state=%s ended=%v", call.State, call.EndedAt != nil)
		}
	}
	if !found {
		t.Fatal("web-live call missing")
	}
}
