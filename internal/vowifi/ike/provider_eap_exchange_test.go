package ike

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

// Exercise encrypted exchanges through Provider.Start, rather than calling the
// notification helper directly. All identities and AKA material are synthetic.
type eapSequenceTransport struct {
	*firstAuthCaptureTransport
	replies     [][]payload
	requests    [][]payload
	initRequest []byte
}

func (transport *eapSequenceTransport) RoundTrip(ctx context.Context, packet []byte) ([]byte, error) {
	responses, err := transport.RoundTripExchange(ctx, [][]byte{packet})
	if err != nil {
		return nil, err
	}
	return responses[0], nil
}

func (transport *eapSequenceTransport) RoundTripExchange(_ context.Context, packets [][]byte) ([][]byte, error) {
	transport.calls++
	if transport.calls == 1 {
		transport.initRequest = append([]byte(nil), packets[0]...)
		packet, err := transport.answerIKEInit(packets[0])
		return [][]byte{packet}, err
	}
	header, request, err := decryptPayloads(packets[0], transport.suite, transport.keys.SKei, transport.keys.SKai)
	if err != nil {
		return nil, err
	}
	transport.requests = append(transport.requests, request)
	index := transport.calls - 2
	if index >= len(transport.replies) {
		return nil, errors.New("test: no further response")
	}
	header.Flags = flagResponse
	packet, err := encryptPayloads(header, transport.replies[index], transport.suite,
		transport.keys.SKer, transport.keys.SKar, constantReader{value: 0x51})
	return [][]byte{packet}, err
}

func TestProviderFinalAUTHUsesTransmittedINITAndIdentity(t *testing.T) {
	result := vowifi.AKAResult{RES: bytes.Repeat([]byte{0x91}, 8), CK: bytes.Repeat([]byte{0x92}, 16), IK: bytes.Repeat([]byte{0x93}, 16)}
	nai, _ := permanentAKAIdentity(testSIMIdentity())
	keys, _ := deriveAKAKeys(nai, result.IK, result.CK)
	rand, _ := marshalAKAAttribute(akaAttrRAND, append([]byte{0, 0}, bytes.Repeat([]byte{0xa1}, 16)...))
	autn, _ := marshalAKAAttribute(akaAttrAUTN, append([]byte{0, 0}, bytes.Repeat([]byte{0xa2}, 16)...))
	mac, _ := marshalAKAAttribute(akaAttrMAC, make([]byte, 18))
	data := append([]byte{akaSubtypeChallenge, 0, 0}, rand...)
	data = append(data, autn...)
	data = append(data, mac...)
	challenge, _ := marshalEAPPacket(eapPacket{Code: eapRequest, Identifier: 2, Type: eapTypeAKA, Data: data})
	copy(challenge[len(challenge)-16:], akaMAC(keys.KAut, challenge))
	success, _ := marshalEAPPacket(eapPacket{Code: eapSuccess, Identifier: 2})
	transport, logs, err := startEAPSequence(t, [][]payload{
		{{Type: payloadIDr, Body: append([]byte{2, 0, 0, 0}, []byte("epdg.example.org")...)}, {Type: payloadEAP, Body: challenge}},
		{{Type: payloadEAP, Body: success}},
		{makeNotify(notifyAuthenticationFailed, nil)},
	}, &testAKAProvider{result: result})
	if !errors.Is(err, vowifi.ErrEAPAuthenticationRejected) || !strings.Contains(err.Error(), "final IKE_AUTH after EAP-Success") {
		t.Fatalf("final rejection incorrectly classified: %v", err)
	}
	if len(transport.requests) != 3 {
		t.Fatalf("authentication requests = %d", len(transport.requests))
	}
	idi, err := onePayload(transport.requests[0], payloadIDi)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := onePayload(transport.requests[2], payloadAuth)
	if err != nil {
		t.Fatal(err)
	}
	// Compute RFC 7296 signed octets independently from the packets received by
	// the peer; do not reuse makeEAPInitiatorAUTH or initiatorSignedOctets.
	mac256 := func(key, message []byte) []byte {
		h := hmac.New(sha256.New, key)
		h.Write(message)
		return h.Sum(nil)
	}
	signed := append(append([]byte(nil), transport.initRequest...), transport.nonceR...)
	signed = append(signed, mac256(transport.keys.SKpi, idi.Body)...)
	expected := append([]byte{authMethodSharedKeyMIC, 0, 0, 0}, mac256(mac256(keys.MSK, []byte("Key Pad for IKEv2")), signed)...)
	if len(transport.requests[2]) != 1 || !bytes.Equal(auth.Body, expected) {
		t.Fatal("final encrypted AUTH did not authenticate the transmitted INIT and IDi")
	}
	if !strings.Contains(logs, "payload_types=[41]") || !strings.Contains(logs, "round=3") {
		t.Fatal("final rejection structure missing from diagnostics")
	}
}

func startEAPSequence(t *testing.T, replies [][]payload, sim *testAKAProvider) (*eapSequenceTransport, string, error) {
	t.Helper()
	var logs bytes.Buffer
	transport := &eapSequenceTransport{firstAuthCaptureTransport: &firstAuthCaptureTransport{t: t}, replies: replies}
	provider, err := NewProvider(Config{Random: constantReader{value: 0x42}, Timeout: time.Second,
		Installer: unusedInstaller{}, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	provider.transportFactory = func(context.Context, transportConfig, vowifi.ProxyRoute, string) (datagramTransport, error) {
		return transport, nil
	}
	identity := testSIMIdentity()
	identity.IMEI = "123456789012345"
	_, err = provider.Start(context.Background(), vowifi.TunnelRequest{
		EPDG: "epdg.example.org", Identity: identity, AKA: sim, Proxy: vowifi.ProxyRoute{Mode: vowifi.ProxyModeDirect},
	})
	for _, secret := range []string{identity.IMSI, identity.IMEI, "nai.epc"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("protocol diagnostics disclosed an identity")
		}
	}
	return transport, logs.String(), err
}

func TestProviderInitialAuthenticationNotifyIsTyped(t *testing.T) {
	_, _, err := startEAPSequence(t, [][]payload{{makeNotify(notifyAuthenticationFailed, nil)}}, &testAKAProvider{})
	if !errors.Is(err, vowifi.ErrEAPAuthenticationRejected) || !strings.Contains(err.Error(), "initial IKE_AUTH") {
		t.Fatalf("initial authentication rejection = %v", err)
	}
}

func TestProviderEAPNotifyOnlyRejectionReportsIdentityStage(t *testing.T) {
	identityRequest, _ := marshalEAPPacket(eapPacket{Code: eapRequest, Identifier: 1, Type: eapTypeIdentity})
	sim := &testAKAProvider{}
	transport, logs, err := startEAPSequence(t, [][]payload{
		{{Type: payloadIDr, Body: append([]byte{2, 0, 0, 0}, []byte("epdg.example.org")...)}, {Type: payloadEAP, Body: identityRequest}},
		{makeNotify(notifyAuthenticationFailed, nil)},
	}, sim)
	if !errors.Is(err, vowifi.ErrEAPAuthenticationRejected) || !strings.Contains(err.Error(), "after EAP-Response/Identity") {
		t.Fatalf("round two notify-only rejection = %v", err)
	}
	if len(transport.requests) != 2 || sim.calls != 0 {
		t.Fatalf("identity rejection reached SIM auth: requests=%d calls=%d", len(transport.requests), sim.calls)
	}
	if !strings.Contains(logs, "round=2") || !strings.Contains(logs, "notify_types=[24]") {
		t.Fatalf("structural diagnostics did not retain the rejection stage: %s", logs)
	}
}

func TestProviderEAPOnlyAnswersDeviceIdentityAfterVerifiedChallenge(t *testing.T) {
	result := vowifi.AKAResult{RES: bytes.Repeat([]byte{0x91}, 8), CK: bytes.Repeat([]byte{0x92}, 16), IK: bytes.Repeat([]byte{0x93}, 16)}
	sim := &testAKAProvider{result: result}
	nai, _ := permanentAKAIdentity(testSIMIdentity())
	keys, _ := deriveAKAKeys(nai, result.IK, result.CK)
	rand, _ := marshalAKAAttribute(akaAttrRAND, append([]byte{0, 0}, bytes.Repeat([]byte{0xa1}, 16)...))
	autn, _ := marshalAKAAttribute(akaAttrAUTN, append([]byte{0, 0}, bytes.Repeat([]byte{0xa2}, 16)...))
	mac, _ := marshalAKAAttribute(akaAttrMAC, make([]byte, 18))
	data := append([]byte{akaSubtypeChallenge, 0, 0}, rand...)
	data = append(data, autn...)
	data = append(data, mac...)
	challenge, _ := marshalEAPPacket(eapPacket{Code: eapRequest, Identifier: 2, Type: eapTypeAKA, Data: data})
	copy(challenge[len(challenge)-16:], akaMAC(keys.KAut, challenge))
	identityRequest, _ := marshalEAPPacket(eapPacket{Code: eapRequest, Identifier: 1, Type: eapTypeIdentity})
	transport, _, err := startEAPSequence(t, [][]payload{
		{{Type: payloadIDr, Body: append([]byte{2, 0, 0, 0}, []byte("epdg.example.org")...)}, {Type: payloadEAP, Body: identityRequest}, makeNotify(notifyDeviceIdentity, nil)},
		{{Type: payloadEAP, Body: challenge}},
		{makeNotify(notifyAuthenticationFailed, nil)},
	}, sim)
	if !errors.Is(err, vowifi.ErrEAPAuthenticationRejected) || sim.calls != 1 || len(transport.requests) != 3 {
		t.Fatalf("challenge exchange = %v, calls=%d, requests=%d", err, sim.calls, len(transport.requests))
	}
	if hasNotifyType(transport.requests[1], notifyDeviceIdentity) {
		t.Fatal("device identity sent before server authentication")
	}
	if !hasNotifyType(transport.requests[2], notifyDeviceIdentity) {
		t.Fatal("EAP-only flow omitted requested device identity after verified AKA server MAC")
	}
}

func TestEAPDeviceIdentityRequiresAuthenticatedServerAndValidIdentity(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		_, sent, err := buildEAPResponsePayloads([]byte{2, 1, 0, 4}, true, authenticated, "invalid")
		if sent || (authenticated && err == nil) || (!authenticated && err != nil) {
			t.Fatalf("authenticated=%v: sent=%v err=%v", authenticated, sent, err)
		}
	}
}
