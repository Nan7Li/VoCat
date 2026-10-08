package sip

import (
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

// Cleanup may observe the confirmed dialog while the response handler has
// not yet sent ACK. Even with that scheduling, the peer must see ACK first.
func TestCleanupWaitsForFirst2xxACK(t *testing.T) {
	h := startHarness(t, newFake(), time.Second)
	h.ua.register()
	h.srv.mu.Lock()
	sess := h.srv.newInbound(vowifi.Call{ID: "incoming", Number: "+15551212"}, h.ua.conn.LocalAddr().(*net.UDPAddr), "sip:alice@127.0.0.1")
	h.srv.mu.Unlock()
	defer sess.cancel()
	sess.remoteTag = "answered"
	sess.dialogConfirmed = true
	h.srv.sendBYE(sess)
	if raw, ok := h.ua.wait(50*time.Millisecond, func(m string) bool { return requestMethod(m) == "BYE" }); ok {
		t.Fatalf("cleanup sent BYE before first ACK: %s", raw)
	}
	h.srv.sendAck(sess, false)
	ack := h.must(h.ua.wait(time.Second, func(m string) bool { return requestMethod(m) == "ACK" }))
	bye := h.must(h.ua.wait(time.Second, func(m string) bool { return requestMethod(m) == "BYE" }))
	for _, raw := range []string{ack, bye} {
		if !strings.Contains(raw, "Call-ID: "+sess.sipCallID) {
			t.Fatalf("wrong dialog: %s", raw)
		}
	}
}
