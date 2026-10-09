package sip

// These UDP regressions keep the protocol-review findings in this package:
// PCMU offers must be 8 kHz mono payload 0, DialTimeout covers Backend.Dial,
// a provisional response does not cancel the incoming timer, a restarted
// client may reuse nc=1 with a new cnonce, another source cannot overwrite a
// completed transaction, and incoming ACK uses the dialog Contact.

import (
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestReviewRejectUnsupportedPCMUOfferBeforeDial(t *testing.T) {
	for _, offer := range []struct{ formats, mapping string }{
		{"96", "a=rtpmap:96 PCMU/8000\r\n"},
		{"0", "a=rtpmap:0 PCMU/16000\r\n"},
		{"0", "a=rtpmap:0 PCMU/8000/2\r\n"},
	} {
		t.Run(strings.TrimSpace(offer.mapping), func(t *testing.T) {
			h := startHarness(t, nil, time.Second)
			h.ua.register()
			h.ua.invite("12345", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", offer.formats, offer.mapping))
			h.ua.must(h.ua.wait(time.Second, func(msg string) bool {
				return cseqMethod(msg) == "INVITE" && statusCode(msg) == 488
			}))
			if dials, _, _, _, _, _ := h.fb.snapshot(); dials != 0 {
				t.Fatal("unsupported offer initiated a cellular call")
			}
		})
	}
}

func TestReviewDialTimeoutIncludesBackendDial(t *testing.T) {
	fb := newFake()
	fb.blockDial = true
	h := startHarness(t, fb, 120*time.Millisecond)
	h.ua.register()
	h.ua.invite("12345", sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", ""))
	h.ua.must(h.ua.wait(time.Second, func(msg string) bool {
		return cseqMethod(msg) == "INVITE" && statusCode(msg) == 504
	}))
	waitUntil(t, time.Second, "timed out session released", func() bool { return h.srv.Status().Active == 0 })
}

func TestReviewProvisionalResponseDoesNotDisableIncomingTimeout(t *testing.T) {
	old := cfgInviteWait.Load()
	cfgInviteWait.Store(int64(150 * time.Millisecond))
	t.Cleanup(func() { cfgInviteWait.Store(old) })
	h := startHarness(t, nil, time.Second)
	h.ua.register()
	h.fb.add(vowifi.Call{ID: "provisional-only", Number: "12345", Direction: "incoming", State: "ringing", StartedAt: time.Now()})
	invite := h.ua.must(h.ua.wait(time.Second, func(msg string) bool { return requestMethod(msg) == "INVITE" }))
	parsed, err := parseMessage([]byte(invite))
	if err != nil {
		t.Fatal(err)
	}
	h.ua.transmit(string(buildResponse(parsed, 180, "Ringing", "ringing-tag", nil, "")))
	h.ua.must(h.ua.wait(time.Second, func(msg string) bool { return requestMethod(msg) == "CANCEL" }))
	waitUntil(t, time.Second, "ringing backend released", func() bool {
		_, _, _, _, hung, _ := h.fb.snapshot()
		return len(hung) == 1 && hung[0] == "provisional-only"
	})
}

func TestReviewDigestAllowsClientRestartWithNewCnonce(t *testing.T) {
	h := startHarness(t, nil, time.Second)
	h.ua.register()
	// A restarted client starts its nonce count at one with a fresh cnonce.
	// authorization generates a fresh cnonce for this request.
	h.ua.nc = 0
	h.ua.register()
	if !h.srv.Status().Registered {
		t.Fatal("valid registration after client restart was lost")
	}
}

func TestReviewOtherSourceCannotOverwriteSuccessfulTransaction(t *testing.T) {
	h := startHarness(t, nil, time.Second)
	h.ua.register()
	h.ua.mu.Lock()
	original := h.ua.last
	h.ua.mu.Unlock()
	var lines []string
	for _, line := range strings.Split(original, "\r\n") {
		if !strings.HasPrefix(strings.ToLower(line), "authorization:") {
			lines = append(lines, line)
		}
	}
	other := newPhone(t, h.srv.Addr().String())
	other.transmit(strings.Join(lines, "\r\n"))
	other.must(other.wait(time.Second, func(msg string) bool { return statusCode(msg) == 401 }))
	h.ua.transmit(original)
	h.ua.must(h.ua.wait(time.Second, func(msg string) bool {
		return cseqMethod(msg) == "REGISTER" && statusCode(msg) == 200
	}))
}

func TestReviewIncomingACKUsesDialogContact(t *testing.T) {
	h := startHarness(t, nil, time.Second)
	h.ua.register()
	h.fb.add(vowifi.Call{ID: "contact-call", Number: "12345", Direction: "incoming", State: "ringing", StartedAt: time.Now()})
	invite := h.ua.must(h.ua.wait(time.Second, func(msg string) bool { return requestMethod(msg) == "INVITE" }))
	if !strings.HasPrefix(invite, "INVITE sip:alice@"+h.ua.local()+" ") {
		t.Fatalf("incoming INVITE ignored registered Contact: %s", strings.SplitN(invite, "\r\n", 2)[0])
	}
	target := "sip:phone-dialog@" + h.ua.local()
	sdp := sdpOffer(h.rtp.LocalAddr().(*net.UDPAddr).Port, "127.0.0.1", "0", "")
	response := strings.Replace(invite200(invite, sdp, "dialog-tag"), "Contact: <sip:alice@127.0.0.1>", "Contact: <"+target+">", 1)
	h.ua.transmit(response)
	ack := h.ua.must(h.ua.wait(time.Second, func(msg string) bool { return requestMethod(msg) == "ACK" }))
	if !strings.HasPrefix(ack, "ACK "+target+" ") {
		t.Fatalf("ACK ignored dialog Contact: %s", strings.SplitN(ack, "\r\n", 2)[0])
	}
}
