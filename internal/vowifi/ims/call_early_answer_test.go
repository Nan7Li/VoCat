package ims

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestOutgoingFinalResponsePreservesEarlyAnswer(t *testing.T) {
	pcma := []byte("v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 9000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n")
	pcmu := []byte("v=0\r\nc=IN IP4 192.0.2.21\r\nm=audio 9002 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
	for _, tc := range []struct {
		name         string
		early, final []byte
		state, codec string
		port         int
	}{
		{"early answer and bodyless final", pcma, nil, "active", "PCMA", 9000},
		{"final answer only", nil, pcmu, "active", "PCMU", 9002},
		{"final answer updates media", pcma, pcmu, "active", "PCMU", 9002},
		{"missing answer everywhere", nil, nil, "failed", "", 0},
		{"invalid early answer", []byte("invalid"), nil, "failed", "", 0},
		{"invalid final answer is rejected", pcma, []byte("invalid"), "failed", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer media.Close()
			call := &imsCall{
				public: vowifi.Call{ID: "early-answer", State: "dialing"},
				callID: "early-answer", target: "sip:callee@example.test", cseq: 1,
				media: media, responses: make(chan *sipResponse, 3),
			}
			session := &Session{
				provider:       &Provider{config: Config{TransactionTimeout: time.Second}},
				calls:          map[string]*imsCall{call.callID: call},
				transactions:   make(map[sipTransactionKey]chan *sipResponse),
				refreshContext: context.Background(), transport: "udp", cseq: 2,
			}
			acks := make(chan struct{}, 1)
			byes := make(chan struct{}, 1)
			session.conn = &fakeConn{onWrite: func(raw []byte) {
				packet, err := parseSIPPacket(raw)
				if err != nil || packet.Request == nil {
					return
				}
				r := packet.Request
				if r.Method == "ACK" {
					acks <- struct{}{}
				}
				if r.Method == "BYE" {
					session.dispatchPacket(sipPacket{Response: &sipResponse{
						StatusCode: 200, Headers: map[string][]string{
							"call-id": {r.value("Call-ID")}, "cseq": {r.value("CSeq")},
						},
					}}, nil)
					byes <- struct{}{}
				}
			}}
			// Match the observed carrier sequence: SDP in the first 183,
			// another bodyless 183, then a bodyless 200 OK.
			if tc.early != nil {
				call.responses <- &sipResponse{StatusCode: 183, Body: tc.early}
				call.responses <- &sipResponse{StatusCode: 183}
			}
			call.responses <- &sipResponse{StatusCode: 200, Body: tc.final}
			done := make(chan struct{})
			go func() {
				session.watchOutgoingCall(call, sipTransactionKey{callID: call.callID, cseq: 1, method: "INVITE"})
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("outgoing response processing did not finish")
			}
			got := session.Calls()[0]
			if got.State != tc.state {
				t.Fatalf("call state = %q (%s), want %q", got.State, got.Reason, tc.state)
			}
			select {
			case <-acks:
			default:
				t.Fatal("final response was not acknowledged")
			}
			if tc.state == "active" {
				if !got.MediaReady || got.Codec != tc.codec || got.EndedAt != nil {
					t.Fatalf("active media = %#v", got)
				}
				if media.remote.Port != tc.port {
					t.Fatalf("RTP port = %d, want %d", media.remote.Port, tc.port)
				}
				select {
				case <-byes:
					t.Fatal("accepted call was hung up")
				default:
				}
			} else {
				if got.EndedAt == nil || !strings.Contains(got.Reason, "SDP") {
					t.Fatalf("missing failure detail: %#v", got)
				}
				select {
				case <-byes:
				case <-time.After(time.Second):
					t.Fatal("invalid media call was not hung up")
				}
			}
		})
	}
}
