package ims

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

func TestOutgoingCallKeepsEarlyAnswerWhenFinal200OmitsSDP(t *testing.T) {
	for _, tc := range []struct {
		name        string
		earlyAnswer bool
		finalTag    string
		wantActive  bool
	}{
		{"with-early-answer", true, "b", true},
		{"without-early-answer", false, "b", false},
		{"different-dialog", true, "c", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer client.Close()
			defer peer.Close()
			go io.Copy(io.Discard, peer)
			media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer media.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			call := &imsCall{
				public: vowifi.Call{ID: "test", State: "dialing"}, callID: "test",
				target: "sip:callee@example.test", from: "<sip:caller@example.test>;tag=a", to: "<sip:callee@example.test>;tag=b",
				cseq: 1, media: media, responses: make(chan *sipResponse, 3),
			}
			session := &Session{provider: &Provider{config: Config{}}, conn: client, transport: "tcp",
				calls: map[string]*imsCall{"test": call}, transactions: make(map[sipTransactionKey]chan *sipResponse), refreshContext: ctx}
			done := make(chan struct{})
			go func() {
				defer close(done)
				session.watchOutgoingCall(call, sipTransactionKey{callID: "test", cseq: 1, method: "INVITE"})
			}()
			if tc.earlyAnswer {
				call.responses <- &sipResponse{StatusCode: 183, Headers: map[string][]string{"to": {"<sip:callee@example.test>;tag=b"}}, Body: media.offerSDP(net.IPv4(127, 0, 0, 1))}
			}
			call.responses <- &sipResponse{StatusCode: 200, Headers: map[string][]string{"to": {"<sip:callee@example.test>;tag=" + tc.finalTag}}}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("call did not finish INVITE processing")
			}
			got := session.Calls()[0]
			if tc.wantActive && (got.State != "active" || !got.MediaReady || got.Codec != "PCMA") {
				t.Fatalf("early answer lost: %#v", got)
			}
			if !tc.wantActive && got.State != "failed" {
				t.Fatalf("missing all SDP was accepted: %#v", got)
			}
		})
	}
}

type protectedResponseTestConn struct {
	fakeConn
	remote net.Addr
}

func (c *protectedResponseTestConn) RemoteAddr() net.Addr { return c.remote }

func TestProtectedUDPResponseUsesClientPortAndPCSCFServer(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session := &Session{transport: "tcp", securityActive: true, protectedUDPClient: client,
		conn:              &protectedResponseTestConn{remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: server.LocalAddr().(*net.UDPAddr).Port}},
		securityAgreement: securityAgreement{selected: securityMechanism{portServer: server.LocalAddr().(*net.UDPAddr).Port}},
	}
	want := []byte("SIP/2.0 200 OK\r\nContent-Length: 0\r\n\r\n")
	if err := session.sendProtectedUDPResponse(want); err != nil {
		t.Fatal(err)
	}
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 512)
	n, from, err := server.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if from.Port != client.LocalAddr().(*net.UDPAddr).Port || string(buffer[:n]) != string(want) {
		t.Fatal("response did not use negotiated client socket")
	}
}

func TestRTPRejectsUnimplementedCodecAndChoosesG711(t *testing.T) {
	media, err := newRTPMedia(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	body := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 22000 RTP/AVP 104\r\na=rtpmap:104 AMR-WB/16000\r\n"
	if err := media.configureRemote([]byte(body)); err == nil || media.ready() {
		t.Fatal("AMR was accepted without an encoder/decoder")
	}
	body = strings.Replace(body, "RTP/AVP 104", "RTP/AVP 104 8", 1) + "a=rtpmap:8 PCMA/8000\r\n"
	if err := media.configureRemote([]byte(body)); err != nil || media.Codec() != "PCMA" {
		t.Fatalf("G711 alternative not chosen: %v", err)
	}
	if strings.Contains(string(media.offerSDP(net.IPv4(127, 0, 0, 1))), "AMR") {
		t.Fatal("offer still advertises unimplemented AMR")
	}
}

func TestRegistrationRetainsAuthAndUsesOwnContactExpiry(t *testing.T) {
	session := &Session{provider: &Provider{config: Config{RegistrationExpiry: time.Hour}},
		conn: &fakeConn{}, transport: "udp", instanceID: "test-instance",
		identity: identitySet{user: "test"},
		auth:     &authenticationState{challenge: digestChallenge{QOP: "auth"}, response: []byte{1, 2, 3}, nc: 2},
	}
	response := &sipResponse{StatusCode: 200, Headers: map[string][]string{
		"contact": {`<sip:other@example.test>;expires=3600`,
			`<sip:test@example.test>;+sip.instance="test-instance";+g.3gpp.smsip;expires=60`},
	}}
	before := time.Now()
	if err := session.applyRegistrationEvidence(response); err != nil {
		t.Fatal(err)
	}
	if session.auth == nil || session.auth.nc != 2 || len(session.auth.response) != 3 {
		t.Fatal("qop=auth credentials were discarded before refresh")
	}
	if expiry := session.expiresAt.Sub(before); expiry < 59*time.Second || expiry > 61*time.Second {
		t.Fatalf("expiry came from another device: %s", expiry)
	}
	if !session.smsContactConfirmed {
		t.Fatal("matching SMS Contact was not confirmed")
	}
	session.auth.challenge.QOP = ""
	material := session.auth.response
	if err := session.applyRegistrationEvidence(response); err != nil {
		t.Fatal(err)
	}
	if session.auth != nil || material[0] != 0 {
		t.Fatal("non-replay-protected credentials were retained")
	}
}
