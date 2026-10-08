// Package cellular is the Halo circuit-switched call controller used by the
// CellBridge line. AT commands go through the injected executor only after
// Authorize allows this physical device. Reader lines are refused here so a
// missing IMS registration cannot fall through to ATD.
package cellular

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"vocat/internal/audiowav"
	"vocat/internal/modem"
	"vocat/internal/vowifi"
)

var (
	// ErrBusy is returned when the line already has a live call.
	ErrBusy = errors.New("line is busy")
	// ErrMediaBusy is returned when a second consumer asks for the same PCM stream.
	ErrMediaBusy = errors.New("call media is already in use")
	// ErrNotCellular is returned instead of sending ATD to a reader or other backend.
	ErrNotCellular  = errors.New("cellular AT is not available for this line")
	errUplinkClosed = errors.New("cellular uplink is closed")
)

// Executor is the narrow AT port. Halo passes device.Manager.ExecuteAT.
type Executor interface {
	ExecuteAT(context.Context, string, string) (modem.Response, error)
}

// Authorization is the device and SIM decision captured for one call.
type Authorization struct {
	Kind          string
	DeviceID      string
	PhysicalID    string
	ICCID         string
	USBGeneration string
	Reason        string
}

// Gate decides whether this line may use cellular AT right now.
type Gate interface {
	Authorize(context.Context) (Authorization, error)
}

// Audio is the local voice path. Prepare only reserves the QDC route.
// Start runs once the cellular call is actually active and is what opens
// capture/playback. MediaReady stays false until Start returns nil.
type Audio interface {
	Prepare(context.Context, string) error
	Start(context.Context, string) error
	ReadPCM([]int16) (int, error)
	WritePCM([]int16) error
	Stop(context.Context) error
}

// Saver persists one snapshot. The same call id is saved again as it changes.
type Saver interface {
	Save(context.Context, vowifi.Call)
}

// Controller tracks one cellular call for one Halo device.
type Controller struct {
	DeviceID      string
	Gate          Gate
	AT            Executor
	Audio         Audio
	Saver         Saver
	RecordingsDir string
	OnIncoming    func(number string)

	// mu guards call state. sig serializes modem commands. audioMu serializes
	// Prepare/Start/Stop. pumpGate keeps a late Read from overlapping the next
	// call's capture. Never take mu and then one of the other locks.
	mu       sync.Mutex
	sig      sync.Mutex
	audioMu  sync.Mutex
	pumpGate sync.Mutex

	live       *session
	recent     []vowifi.Call
	nextEpoch  uint64
	pumpOnce   sync.Once
	mediaSub   *subscriber
	mediaDone  chan struct{}
	lease      *uplinkLease
	audioOwner *session
}

type session struct {
	call          vowifi.Call
	physicalID    string
	iccid         string
	usbGeneration string
	kind          string
	epoch         uint64
	// onModem is set after ATD is accepted or an incoming CLCC call is adopted.
	// Hangup sends ATH only then, and only to this original identity.
	onModem bool
	// holdCLCC ignores CLCC while dial preparation has not submitted ATD, and
	// while an incoming answer is between Prepare and ATA. An empty list in
	// that window is not the end of the call.
	holdCLCC      bool
	prepared      bool
	started       bool
	starting      bool
	audioStopping bool
	cancelled     bool
	index         int
	indexBound    bool
	recorder      *audiowav.Recorder
}

type subscriber struct {
	owner string
	ch    chan []int16
	epoch uint64
}

// uplinkLease is the write permit captured when a PCM port is opened.
// Release and finish clear it so a port cannot target a later call.
type uplinkLease struct {
	mu       sync.Mutex
	open     bool
	audio    Audio
	recorder *audiowav.Recorder
	onError  func(error)
}

func (lease *uplinkLease) write(samples []int16) (err error) {
	if lease == nil {
		return errUplinkClosed
	}
	lease.mu.Lock()
	defer func() {
		lease.mu.Unlock()
		if err != nil && !errors.Is(err, errUplinkClosed) && lease.onError != nil {
			lease.onError(err)
		}
	}()
	if !lease.open {
		return errUplinkClosed
	}
	if lease.recorder != nil {
		lease.recorder.WriteUplink(samples)
	}
	if lease.audio == nil {
		return errors.New("cellular audio is not configured")
	}
	return lease.audio.WritePCM(samples)
}

func (lease *uplinkLease) close() {
	if lease == nil {
		return
	}
	lease.mu.Lock()
	lease.open = false
	lease.mu.Unlock()
}

var callNonce atomic.Uint64

// Dial starts an outgoing call. Audio is prepared first when an engine is
// configured or requireMedia is set. A failed or cancelled preparation does
// not send ATD.
func (controller *Controller) Dial(ctx context.Context, number string, requireMedia bool) (vowifi.Call, error) {
	number = strings.TrimSpace(number)
	if !ValidNumber(number) {
		return vowifi.Call{}, errors.New("phone number is invalid")
	}
	auth, err := controller.authorize(ctx)
	if err != nil {
		return vowifi.Call{}, err
	}
	if auth.Kind != "cellular" || auth.PhysicalID == "" {
		reason := strings.TrimSpace(auth.Reason)
		if reason == "" {
			reason = "this line is not a cellular module"
		}
		return vowifi.Call{}, fmt.Errorf("%w: %s", ErrNotCellular, reason)
	}

	controller.mu.Lock()
	if controller.live != nil && controller.live.call.EndedAt == nil {
		controller.mu.Unlock()
		return vowifi.Call{}, ErrBusy
	}
	callID := newCallID(controller.DeviceID)
	sess := &session{
		call: vowifi.Call{
			ID: callID, Number: number, Direction: "outgoing", State: "dialing", StartedAt: time.Now().UTC(),
		},
		physicalID:    auth.PhysicalID,
		iccid:         auth.ICCID,
		usbGeneration: auth.USBGeneration,
		kind:          auth.Kind,
		epoch:         controller.bumpEpochLocked(),
		holdCLCC:      true,
	}
	controller.live = sess
	controller.mu.Unlock()

	if controller.Audio != nil || requireMedia {
		if err := controller.prepareAudio(ctx, sess, callID); err != nil {
			controller.stopAudioFor(sess)
			controller.finish(callID, false, err.Error())
			return vowifi.Call{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		controller.stopAudioFor(sess)
		controller.finish(callID, false, err.Error())
		return vowifi.Call{}, err
	}
	if !controller.stillOwns(sess) {
		controller.stopAudioFor(sess)
		controller.finish(callID, false, "cellular call was cancelled")
		return vowifi.Call{}, errors.New("cellular call was cancelled")
	}

	controller.sig.Lock()
	auth, err = controller.authorize(ctx)
	if err != nil || !matchAuth(auth, sess) || ctx.Err() != nil || !controller.stillPreparing(sess) {
		controller.sig.Unlock()
		cause := err
		if cause == nil && ctx.Err() != nil {
			cause = ctx.Err()
		}
		if cause == nil && !matchAuth(auth, sess) {
			cause = errors.New("device identity changed")
		}
		if cause == nil {
			cause = errors.New("cellular call was cancelled")
		}
		controller.stopAudioFor(sess)
		controller.finish(callID, false, cause.Error())
		return vowifi.Call{}, cause
	}

	response, err := controller.AT.ExecuteAT(ctx, sess.physicalID, "ATD"+number+";")
	accepted := err == nil && response.OK()
	interrupted := ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	owns := controller.stillPreparing(sess)
	if !accepted || interrupted || !owns {
		// A cancelled or expired ATD can still have been accepted by the modem.
		// Hang up only when this same physical line and SIM are still present.
		if accepted || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			controller.cleanupModem(sess)
		}
		controller.sig.Unlock()
		cause := err
		if cause == nil && !accepted {
			cause = errors.New("modem did not accept ATD")
		}
		if cause == nil && ctx.Err() != nil {
			cause = ctx.Err()
		}
		if cause == nil {
			cause = errors.New("cellular call was cancelled")
		}
		controller.stopAudioFor(sess)
		controller.finish(callID, false, cause.Error())
		return vowifi.Call{}, cause
	}

	controller.mu.Lock()
	if controller.ownsLocked(sess) && sess.holdCLCC {
		sess.onModem = true
		sess.holdCLCC = false
		snapshot := sess.call
		controller.emitLocked(snapshot)
		controller.mu.Unlock()
		controller.sig.Unlock()
		return snapshot, nil
	}
	controller.mu.Unlock()
	controller.cleanupModem(sess)
	controller.sig.Unlock()
	controller.stopAudioFor(sess)
	controller.finish(callID, false, "cellular call was cancelled")
	return vowifi.Call{}, errors.New("cellular call was cancelled")
}

// Answer accepts the pinned incoming ringing call. Prepare runs before ATA.
// Capture starts later, only after CLCC reports the call active.
func (controller *Controller) Answer(ctx context.Context, callID string) (vowifi.Call, error) {
	controller.mu.Lock()
	sess := controller.live
	if sess == nil || (callID != "" && sess.call.ID != callID) || sess.call.EndedAt != nil ||
		sess.call.Direction != "incoming" || sess.call.State != "ringing" {
		controller.mu.Unlock()
		return vowifi.Call{}, errors.New("no ringing cellular call")
	}
	id := sess.call.ID
	sess.holdCLCC = true
	controller.mu.Unlock()
	clearHold := func() {
		controller.mu.Lock()
		if controller.live == sess {
			sess.holdCLCC = false
		}
		controller.mu.Unlock()
	}
	defer clearHold()

	auth, err := controller.authorize(ctx)
	if err != nil || !matchAuth(auth, sess) || !controller.stillOwns(sess) {
		if controller.stillOwns(sess) {
			controller.stopAudioFor(sess)
			controller.finish(id, false, "device identity changed")
		}
		if err != nil {
			return vowifi.Call{}, err
		}
		return vowifi.Call{}, errors.New("device identity changed")
	}
	if controller.Audio != nil {
		if err := controller.prepareAudio(ctx, sess, id); err != nil {
			controller.stopAudioFor(sess)
			return vowifi.Call{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return vowifi.Call{}, err
	}

	controller.sig.Lock()
	defer controller.sig.Unlock()
	auth, err = controller.authorize(ctx)
	owns := controller.stillRinging(sess)
	if err != nil || !matchAuth(auth, sess) || !owns || ctx.Err() != nil {
		if owns && (err != nil || !matchAuth(auth, sess)) {
			controller.stopAudioFor(sess)
			controller.finish(id, false, "device identity changed")
		}
		if err != nil {
			return vowifi.Call{}, err
		}
		if ctx.Err() != nil {
			return vowifi.Call{}, ctx.Err()
		}
		if !matchAuth(auth, sess) {
			return vowifi.Call{}, errors.New("device identity changed")
		}
		return vowifi.Call{}, errors.New("no ringing cellular call")
	}
	response, err := controller.AT.ExecuteAT(ctx, sess.physicalID, "ATA")
	accepted := err == nil && response.OK()
	interrupted := ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if !accepted || interrupted || !controller.stillOwns(sess) {
		if accepted || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			controller.cleanupModem(sess)
		}
		cause := err
		if cause == nil && !accepted {
			cause = errors.New("modem did not accept ATA")
		}
		if cause == nil && ctx.Err() != nil {
			cause = ctx.Err()
		}
		if cause == nil {
			cause = errors.New("cellular call disappeared during answer")
		}
		controller.stopAudioFor(sess)
		controller.finish(id, false, cause.Error())
		return vowifi.Call{}, cause
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.live != sess || sess.call.EndedAt != nil {
		return vowifi.Call{}, errors.New("cellular call disappeared during answer")
	}
	sess.holdCLCC = false
	return sess.call, nil
}

// Hangup clears the pinned call on its original physical device.
// A new SIM or a different modem never receives ATH.
func (controller *Controller) Hangup(ctx context.Context, callID string) error {
	controller.mu.Lock()
	sess := controller.live
	if sess == nil || (callID != "" && sess.call.ID != callID) || sess.call.EndedAt != nil {
		controller.mu.Unlock()
		return errors.New("no cellular call")
	}
	sess.cancelled = true
	id := sess.call.ID
	onModem := sess.onModem
	controller.mu.Unlock()
	if !onModem {
		controller.stopAudioFor(sess)
		controller.finish(id, false, "")
		return nil
	}

	controller.sig.Lock()
	auth, err := controller.authorize(ctx)
	if err != nil || !matchAuth(auth, sess) || !controller.liveIs(sess) {
		controller.sig.Unlock()
		if controller.liveIs(sess) {
			controller.stopAudioFor(sess)
			reason := "device identity changed"
			if err != nil {
				reason = err.Error()
			}
			controller.finish(id, false, reason)
		}
		if err != nil {
			return err
		}
		if !matchAuth(auth, sess) {
			return errors.New("device identity changed")
		}
		return nil
	}
	response, err := controller.AT.ExecuteAT(ctx, sess.physicalID, "ATH")
	controller.sig.Unlock()
	controller.stopAudioFor(sess)
	controller.finish(id, false, "")
	if err != nil {
		return err
	}
	if !response.OK() {
		return errors.New("modem did not accept ATH")
	}
	return nil
}

// Poll reads CLCC for the pinned device. A changed SIM or physical id ends
// the local call without sending ATH to the new device. An empty CLCC list
// during dial/answer preparation does not finish the call.
func (controller *Controller) Poll(ctx context.Context) error {
	controller.mu.Lock()
	sess := controller.live
	var physicalID, iccid, kind, id, generation string
	hold := false
	if sess != nil && sess.call.EndedAt == nil {
		physicalID = sess.physicalID
		iccid = sess.iccid
		generation = sess.usbGeneration
		kind = sess.kind
		id = sess.call.ID
		hold = sess.holdCLCC
	}
	controller.mu.Unlock()

	auth, err := controller.authorize(ctx)
	if sess != nil && id != "" && controller.liveIs(sess) {
		if err != nil || !matchAuth(auth, sess) {
			controller.stopAudioFor(sess)
			controller.finish(id, false, "device identity changed")
			return nil
		}
	} else {
		sess = nil
		if err != nil || auth.Kind != "cellular" || auth.PhysicalID == "" {
			return nil
		}
		physicalID = auth.PhysicalID
		iccid = auth.ICCID
		generation = auth.USBGeneration
		kind = auth.Kind
	}
	if hold && controller.liveIs(sess) {
		return nil
	}
	if physicalID == "" || controller.AT == nil {
		return nil
	}
	controller.sig.Lock()
	response, cmdErr := controller.AT.ExecuteAT(ctx, physicalID, "AT+CLCC")
	controller.sig.Unlock()
	if cmdErr != nil {
		return cmdErr
	}
	if !response.OK() {
		return errors.New("modem did not accept AT+CLCC")
	}
	controller.observe(response, physicalID, iccid, kind, generation, sess, true)
	return nil
}

// Observe applies one CLCC response to the pinned call. Voice-mode entries are
// matched by the index already bound to the session. A call-waiting line cannot
// replace the original call's number or state.
func (controller *Controller) Observe(response modem.Response, physicalID, iccid, kind string, generation ...string) {
	usbGeneration := ""
	if len(generation) > 0 {
		usbGeneration = generation[0]
	}
	controller.observe(response, physicalID, iccid, kind, usbGeneration, nil, false)
}

func (controller *Controller) observe(response modem.Response, physicalID, iccid, kind, generation string, expected *session, checkSession bool) {
	found := parseCLCC(response)
	controller.mu.Lock()
	// A delayed poll belongs to the session present when CLCC was sent.
	// Do not apply an old empty response to a call created after it.
	if checkSession && controller.live != expected {
		controller.mu.Unlock()
		return
	}
	if controller.live != nil && controller.live.call.EndedAt == nil && controller.live.holdCLCC {
		controller.mu.Unlock()
		return
	}
	if controller.live != nil && controller.live.call.EndedAt == nil {
		sess := controller.live
		if !sessionIdentityMatches(sess, physicalID, iccid, kind) || (sess.usbGeneration != "" && generation != sess.usbGeneration) {
			id := sess.call.ID
			controller.mu.Unlock()
			controller.stopAudioFor(sess)
			controller.finish(id, false, "device identity changed")
			return
		}
		matched, ok := matchCall(found, sess)
		if !ok {
			// An empty list, or a bound index that disappeared, ends this call.
			// A different CLCC row (call waiting, data) must not inherit it.
			// Before an index is bound, unrelated rows are ignored so a dial
			// that is not listed yet stays up.
			if len(found) == 0 || sess.indexBound {
				id := sess.call.ID
				controller.mu.Unlock()
				controller.stopAudioFor(sess)
				controller.finish(id, false, "")
				return
			}
			controller.mu.Unlock()
			return
		}
		if !sess.indexBound {
			sess.index = matched.index
			sess.indexBound = true
		}
		previousNumber := sess.call.Number
		if matched.number != "" {
			sess.call.Number = matched.number
		}
		previous := sess.call.State
		next := mapCLCCState(matched.state, sess.call.Direction)
		sess.call.State = next
		if next == "active" && sess.call.AnsweredAt == nil {
			now := time.Now().UTC()
			sess.call.AnsweredAt = &now
		}
		if previous != next || (matched.number != "" && matched.number != previousNumber) {
			controller.emitLocked(sess.call)
		}
		needStart := next == "active" && sess.prepared && !sess.started && !sess.starting && controller.Audio != nil
		if needStart {
			sess.starting = true
		}
		controller.mu.Unlock()
		if needStart {
			controller.activateMedia(sess)
		}
		return
	}

	item, ok := pickUntracked(found)
	if !ok {
		controller.mu.Unlock()
		return
	}
	direction := "outgoing"
	state := "dialing"
	if item.direction == 1 {
		direction = "incoming"
		state = "ringing"
	}
	mapped := mapCLCCState(item.state, direction)
	if mapped != "" {
		state = mapped
	}
	now := time.Now().UTC()
	sess := &session{
		call: vowifi.Call{
			ID:        newCallID(controller.DeviceID),
			Number:    item.number,
			Direction: direction,
			State:     state,
			StartedAt: now,
		},
		physicalID:    physicalID,
		iccid:         iccid,
		usbGeneration: generation,
		kind:          kind,
		epoch:         controller.bumpEpochLocked(),
		onModem:       true,
		index:         item.index,
		indexBound:    true,
	}
	if state == "active" {
		answered := now
		sess.call.AnsweredAt = &answered
	}
	controller.live = sess
	controller.emitLocked(sess.call)
	var notify func(string)
	var notifyNumber string
	if direction == "incoming" && controller.OnIncoming != nil {
		notify = controller.OnIncoming
		notifyNumber = item.number
	}
	needStart := state == "active" && sess.prepared && controller.Audio != nil
	if needStart {
		sess.starting = true
	}
	controller.mu.Unlock()
	if notify != nil {
		go notify(notifyNumber)
	}
	if needStart {
		controller.activateMedia(sess)
	}
}

// Calls returns the live call and recently finished calls.
func (controller *Controller) Calls() []vowifi.Call {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	result := make([]vowifi.Call, 0, len(controller.recent)+1)
	if controller.live != nil && controller.live.call.EndedAt == nil {
		result = append(result, controller.live.call)
	}
	result = append(result, controller.recent...)
	return result
}

// OpenMedia attaches one PCM consumer. The returned port keeps the session
// and lease it was opened with. Release or finish makes further writes fail.
func (controller *Controller) OpenMedia(callID, owner string) (vowifi.CallMedia, func(), error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	current := controller.live
	if current == nil || current.call.ID != callID || !current.call.MediaReady || current.call.Codec != "PCMU" {
		return nil, nil, errors.New("cellular PCM media is not available")
	}
	if controller.mediaSub != nil || controller.lease != nil {
		return nil, nil, ErrMediaBusy
	}
	sub := &subscriber{owner: owner, ch: make(chan []int16, 8), epoch: current.epoch}
	controller.mediaSub = sub
	lease := &uplinkLease{open: true, audio: controller.Audio, recorder: current.recorder}
	lease.onError = func(err error) { controller.failAudio(current, err) }
	controller.lease = lease
	port := &mediaPort{
		codec: "PCMU",
		read:  sub.ch,
		done:  controller.mediaDone,
		write: lease.write,
	}
	release := func() {
		lease.close()
		controller.mu.Lock()
		if controller.mediaSub == sub {
			controller.mediaSub = nil
		}
		if controller.lease == lease {
			controller.lease = nil
		}
		controller.mu.Unlock()
	}
	return port, release, nil
}

func (controller *Controller) activateMedia(sess *session) {
	defer func() {
		controller.mu.Lock()
		sess.starting = false
		controller.mu.Unlock()
	}()
	if controller.Audio == nil {
		return
	}
	err := controller.startAudio(sess, sess.call.ID)
	if err != nil {
		if controller.liveIs(sess) {
			controller.safeHangup(sess)
			controller.stopAudioFor(sess)
			controller.finishCall(sess.call.ID, false, "cellular audio failed to start: "+err.Error(), true)
		} else {
			controller.stopAudioFor(sess)
		}
		return
	}
	controller.mu.Lock()
	if controller.live != sess || sess.cancelled || sess.call.EndedAt != nil || sess.call.State != "active" {
		controller.mu.Unlock()
		controller.stopAudioFor(sess)
		return
	}
	sess.started = true
	sess.call.MediaReady = true
	sess.call.Codec = "PCMU"
	controller.ensureRecordingLocked(sess)
	controller.startPumpLocked(sess)
	controller.emitLocked(sess.call)
	controller.mu.Unlock()
}

func (controller *Controller) prepareAudio(ctx context.Context, sess *session, callID string) error {
	if controller.Audio == nil {
		return errors.New("cellular audio is not configured")
	}
	if !controller.stillOwns(sess) {
		return errors.New("cellular call was cancelled")
	}
	// Prepare is not under audioMu so Hangup can Stop a route that is still
	// being reserved. The device implementation must tolerate that overlap.
	if err := controller.Audio.Prepare(ctx, callID); err != nil {
		controller.stopAudioFor(sess)
		return fmt.Errorf("cellular audio is not ready: %w", err)
	}
	controller.mu.Lock()
	owned := controller.ownsLocked(sess)
	if owned {
		sess.prepared = true
		controller.audioOwner = sess
	}
	controller.mu.Unlock()
	if !owned {
		controller.stopAudioFor(sess)
		return errors.New("cellular call was cancelled")
	}
	return nil
}

func (controller *Controller) startAudio(sess *session, callID string) error {
	if controller.Audio == nil {
		return errors.New("cellular audio is not configured")
	}
	if !controller.stillOwns(sess) {
		return errors.New("cellular call was cancelled")
	}
	if err := controller.Audio.Start(context.Background(), callID); err != nil {
		controller.stopAudioFor(sess)
		return err
	}
	controller.mu.Lock()
	owned := controller.ownsLocked(sess)
	if owned {
		controller.audioOwner = sess
	}
	controller.mu.Unlock()
	if !owned {
		controller.stopAudioFor(sess)
		return errors.New("cellular call was cancelled")
	}
	return nil
}

func (controller *Controller) authorize(ctx context.Context) (Authorization, error) {
	if controller.Gate == nil {
		return Authorization{}, errors.New("cellular call policy gate is not configured")
	}
	return controller.Gate.Authorize(ctx)
}

func (controller *Controller) emitLocked(call vowifi.Call) {
	if controller.Saver == nil {
		return
	}
	controller.Saver.Save(context.Background(), call)
}

func (controller *Controller) finish(callID string, answered bool, reason string) {
	controller.finishCall(callID, answered, reason, false)
}

func (controller *Controller) finishCall(callID string, answered bool, reason string, forceFailed bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	current := controller.live
	if current == nil || current.call.ID != callID || current.call.EndedAt != nil {
		return
	}
	now := time.Now().UTC()
	current.call.EndedAt = &now
	current.call.State = "ended"
	current.call.MediaReady = false
	if reason != "" {
		current.call.Reason = reason
		if current.call.AnsweredAt == nil || forceFailed {
			current.call.State = "failed"
		}
	}
	if forceFailed {
		current.call.State = "failed"
		if reason != "" {
			current.call.Reason = reason
		}
	}
	if answered && current.call.AnsweredAt == nil {
		current.call.AnsweredAt = &now
	}
	if current.recorder != nil {
		_ = current.recorder.Close()
		current.recorder = nil
	}
	if controller.lease != nil {
		controller.lease.close()
		controller.lease = nil
	}
	// Close the readiness signal only. The subscriber channel stays open so a
	// send already in select cannot panic.
	if controller.mediaDone != nil {
		close(controller.mediaDone)
		controller.mediaDone = nil
	}
	controller.mediaSub = nil
	if controller.audioOwner == current {
		controller.audioOwner = nil
	}
	finished := current.call
	controller.recent = append([]vowifi.Call{finished}, controller.recent...)
	if len(controller.recent) > 8 {
		controller.recent = controller.recent[:8]
	}
	controller.live = nil
	controller.pumpOnce = sync.Once{}
	controller.emitLocked(finished)
}

func (controller *Controller) stopAudioFor(sess *session) {
	if controller == nil || controller.Audio == nil || sess == nil {
		return
	}
	controller.audioMu.Lock()
	defer controller.audioMu.Unlock()
	controller.mu.Lock()
	if controller.audioOwner != nil && controller.audioOwner != sess {
		controller.mu.Unlock()
		return
	}
	if controller.audioOwner == nil && controller.live != nil && controller.live != sess {
		controller.mu.Unlock()
		return
	}
	if controller.audioOwner == sess {
		controller.audioOwner = nil
	}
	sess.audioStopping = true
	audio := controller.Audio
	controller.mu.Unlock()
	if audio != nil {
		_ = audio.Stop(context.Background())
	}
}

func (controller *Controller) safeHangup(sess *session) {
	if sess == nil || !sess.onModem {
		return
	}
	controller.sig.Lock()
	controller.cleanupModem(sess)
	controller.sig.Unlock()
}

// cleanupModem sends ATH only when the original physical device and SIM are
// still the line we dialed. Caller holds sig. onModem may still be false when
// a late ATD succeeded and the local session must be cleared.
func (controller *Controller) cleanupModem(sess *session) {
	if sess == nil || controller.AT == nil {
		return
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth, err := controller.authorize(cleanup)
	if err != nil || !matchAuth(auth, sess) {
		return
	}
	controller.mu.Lock()
	if controller.live != nil && controller.live != sess {
		controller.mu.Unlock()
		return
	}
	controller.mu.Unlock()
	_, _ = controller.AT.ExecuteAT(cleanup, sess.physicalID, "ATH")
}

func (controller *Controller) ensureRecordingLocked(current *session) {
	if current.recorder != nil || controller.RecordingsDir == "" {
		return
	}
	relative := filepath.Join(sanitize(controller.DeviceID), sanitize(current.call.ID)+".wav")
	recorder, err := audiowav.New(filepath.Join(controller.RecordingsDir, relative))
	if err != nil {
		return
	}
	current.recorder = recorder
	current.call.Recording = filepath.ToSlash(relative)
}

func (controller *Controller) startPumpLocked(current *session) {
	if controller.Audio == nil || !current.started {
		return
	}
	if controller.mediaDone == nil {
		controller.mediaDone = make(chan struct{})
	}
	done := controller.mediaDone
	epoch := current.epoch
	controller.pumpOnce.Do(func() {
		go func() {
			controller.pumpGate.Lock()
			defer controller.pumpGate.Unlock()
			controller.mu.Lock()
			alive := controller.live == current && current.epoch == epoch && current.call.EndedAt == nil
			controller.mu.Unlock()
			if !alive {
				return
			}
			controller.pump(done, current, epoch)
		}()
	})
}

func (controller *Controller) pump(done <-chan struct{}, sess *session, epoch uint64) {
	if controller.Audio == nil || sess == nil {
		return
	}
	buffer := make([]int16, 160)
	for {
		count, err := controller.Audio.ReadPCM(buffer)
		var samples []int16
		if count > 0 {
			samples = append([]int16(nil), buffer[:count]...)
		}
		controller.mu.Lock()
		alive := controller.live == sess && sess.epoch == epoch && sess.call.EndedAt == nil
		var recorder *audiowav.Recorder
		var sub *subscriber
		if alive && count > 0 {
			recorder = sess.recorder
			if controller.mediaSub != nil && controller.mediaSub.epoch == epoch {
				sub = controller.mediaSub
			}
		}
		controller.mu.Unlock()
		if !alive {
			return
		}
		if count > 0 {
			if recorder != nil {
				recorder.WriteDownlink(samples)
			}
			if sub != nil {
				select {
				case <-done:
					return
				case sub.ch <- samples:
				default:
				}
			}
		}
		if err != nil {
			controller.failAudio(sess, err)
			return
		}
	}
}

// failAudio stops a call whose capture or playback pipe failed. An expected
// Stop and a released media port cannot turn another call into a failure.
func (controller *Controller) failAudio(sess *session, err error) {
	controller.mu.Lock()
	if !controller.ownsLocked(sess) || sess.audioStopping {
		controller.mu.Unlock()
		return
	}
	sess.audioStopping = true
	sess.cancelled = true
	controller.mu.Unlock()
	controller.safeHangup(sess)
	controller.stopAudioFor(sess)
	controller.finishCall(sess.call.ID, false, "cellular audio stream failed: "+err.Error(), true)
}

func (controller *Controller) ownsLocked(sess *session) bool {
	return sess != nil && controller.live == sess && !sess.cancelled && sess.call.EndedAt == nil
}

func (controller *Controller) stillOwns(sess *session) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.ownsLocked(sess)
}

func (controller *Controller) stillPreparing(sess *session) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.ownsLocked(sess) && sess.holdCLCC && !sess.onModem
}

func (controller *Controller) stillRinging(sess *session) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.ownsLocked(sess) && sess.call.Direction == "incoming" && sess.call.State == "ringing"
}

func (controller *Controller) liveIs(sess *session) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return sess != nil && controller.live == sess && sess.call.EndedAt == nil
}

func (controller *Controller) bumpEpochLocked() uint64 {
	controller.nextEpoch++
	return controller.nextEpoch
}

func matchAuth(auth Authorization, sess *session) bool {
	if sess == nil {
		return false
	}
	if auth.Kind != sess.kind || auth.PhysicalID == "" || auth.PhysicalID != sess.physicalID {
		return false
	}
	// A known SIM must still be the one we captured. Empty or different means
	// the module was swapped; do not signal the new card.
	if sess.iccid != "" && auth.ICCID != sess.iccid {
		return false
	}
	if sess.usbGeneration != "" && auth.USBGeneration != sess.usbGeneration {
		return false
	}
	return true
}

func sessionIdentityMatches(sess *session, physicalID, iccid, kind string) bool {
	if sess == nil {
		return false
	}
	if kind != "" && sess.kind != "" && kind != sess.kind {
		return false
	}
	if physicalID != "" && sess.physicalID != "" && physicalID != sess.physicalID {
		return false
	}
	if sess.iccid != "" && iccid != sess.iccid {
		return false
	}
	return true
}

type mediaPort struct {
	codec string
	read  <-chan []int16
	done  <-chan struct{}
	write func([]int16) error
}

func (port *mediaPort) Codec() string { return port.codec }

func (port *mediaPort) ReadPCM(ctx context.Context) ([]int16, error) {
	if port.done == nil {
		return nil, io.EOF
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-port.done:
		return nil, io.EOF
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-port.done:
		return nil, io.EOF
	case samples, ok := <-port.read:
		if !ok {
			return nil, io.EOF
		}
		return samples, nil
	}
}

func (port *mediaPort) WritePCM(samples []int16) error {
	if port.write == nil {
		return errUplinkClosed
	}
	return port.write(samples)
}

type clccCall struct {
	index     int
	direction int
	state     int
	mode      int
	number    string
}

func parseCLCC(response modem.Response) []clccCall {
	result := make([]clccCall, 0)
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "+CLCC:") {
			continue
		}
		fields := strings.Split(strings.TrimSpace(line[len("+CLCC:"):]), ",")
		if len(fields) < 5 {
			continue
		}
		call := clccCall{}
		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			continue
		}
		direction, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			continue
		}
		state, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			continue
		}
		mode, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil || !isVoiceMode(mode) {
			continue
		}
		call.index = index
		call.direction = direction
		call.state = state
		call.mode = mode
		if len(fields) > 5 {
			call.number = strings.Trim(strings.TrimSpace(fields[5]), `"`)
		}
		result = append(result, call)
	}
	return result
}

// isVoiceMode reports TS 27.007 bearer modes that are still voice.
func isVoiceMode(mode int) bool {
	switch mode {
	case 0, 3, 4, 5:
		return true
	default:
		return false
	}
}

func matchCall(calls []clccCall, sess *session) (clccCall, bool) {
	if sess != nil && sess.indexBound {
		for _, call := range calls {
			if call.index == sess.index {
				return call, true
			}
		}
		return clccCall{}, false
	}
	wantDir := 0
	if sess != nil && sess.call.Direction == "incoming" {
		wantDir = 1
	}
	for _, call := range calls {
		if sess != nil && call.direction != wantDir {
			continue
		}
		if sess != nil && sess.call.Number != "" && call.number != "" && call.number != sess.call.Number {
			continue
		}
		// Waiting is a second call. Do not bind it over an outbound session.
		if call.state == 5 && (sess == nil || sess.call.Direction != "incoming") {
			continue
		}
		return call, true
	}
	return clccCall{}, false
}

func pickUntracked(calls []clccCall) (clccCall, bool) {
	best := -1
	for i, call := range calls {
		if call.state == 5 {
			continue
		}
		if best < 0 || call.index < calls[best].index {
			best = i
		}
	}
	if best >= 0 {
		return calls[best], true
	}
	for _, call := range calls {
		if call.direction == 1 && call.state == 5 {
			return call, true
		}
	}
	return clccCall{}, false
}

func mapCLCCState(state int, direction string) string {
	switch state {
	case 0, 1:
		return "active"
	case 4, 5:
		return "ringing"
	case 2, 3:
		if direction == "incoming" {
			return "ringing"
		}
		return "dialing"
	default:
		return "dialing"
	}
}

// ValidNumber matches Halo's dial-number rule.
func ValidNumber(value string) bool {
	if len(value) < 2 || len(value) > 32 {
		return false
	}
	for index, character := range value {
		if character >= '0' && character <= '9' || (index == 0 && character == '+') || character == '*' || character == '#' {
			continue
		}
		if unicode.IsSpace(character) {
			return false
		}
		return false
	}
	return true
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "call"
	}
	cleaned := make([]rune, 0, len(value))
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			cleaned = append(cleaned, character)
		} else {
			cleaned = append(cleaned, '_')
		}
	}
	if len(cleaned) > 80 {
		cleaned = cleaned[:80]
	}
	return string(cleaned)
}

func newCallID(device string) string {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		binary.BigEndian.PutUint64(nonce[:], uint64(time.Now().UnixNano())^callNonce.Add(1))
	}
	return "cs-" + hex.EncodeToString(nonce[:]) + "-" + sanitize(device)
}
