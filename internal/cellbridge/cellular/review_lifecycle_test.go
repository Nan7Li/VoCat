package cellular

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vocat/internal/modem"
)

type reviewGate struct{}

func (reviewGate) Authorize(context.Context) (Authorization, error) {
	return Authorization{Kind: "cellular", DeviceID: "dji", PhysicalID: "physical", ICCID: "8900000000000000001"}, nil
}

type reviewAT struct{ dials atomic.Int32 }

func (at *reviewAT) ExecuteAT(_ context.Context, _ string, command string) (modem.Response, error) {
	if len(command) > 3 && command[:3] == "ATD" {
		at.dials.Add(1)
	}
	return modem.Response{Final: "OK"}, nil
}

type reviewAudio struct {
	prepared, started, writes atomic.Int32
	entered, release          chan struct{}
	mu                        sync.Mutex
	done                      chan struct{}
}

func (audio *reviewAudio) Prepare(context.Context, string) error {
	audio.prepared.Add(1)
	if audio.entered != nil {
		close(audio.entered)
		<-audio.release
	}
	return nil
}
func (audio *reviewAudio) Start(context.Context, string) error {
	audio.started.Add(1)
	audio.mu.Lock()
	audio.done = make(chan struct{})
	audio.mu.Unlock()
	return nil
}
func (audio *reviewAudio) ReadPCM([]int16) (int, error) {
	audio.mu.Lock()
	done := audio.done
	audio.mu.Unlock()
	if done == nil {
		return 0, errors.New("capture before active")
	}
	<-done
	return 0, errors.New("capture stopped")
}
func (audio *reviewAudio) WritePCM([]int16) error { audio.writes.Add(1); return nil }
func (audio *reviewAudio) Stop(context.Context) error {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	if audio.done != nil {
		close(audio.done)
		audio.done = nil
	}
	return nil
}
func reviewController(audio *reviewAudio) (*Controller, *reviewAT) {
	at := &reviewAT{}
	controller := &Controller{DeviceID: "dji", Gate: reviewGate{}, AT: at}
	if audio != nil {
		controller.Audio = audio
	}
	return controller, at
}
func reviewObserve(controller *Controller, direction, state int) {
	line := "+CLCC: 1,0,0,0,0,\"12345\",129"
	if direction == 1 {
		line = "+CLCC: 1,1,4,0,0,\"12345\",129"
	}
	if direction == 1 && state == 0 {
		line = "+CLCC: 1,1,0,0,0,\"12345\",129"
	}
	controller.Observe(modem.Response{Final: "OK", Lines: []string{line}}, "physical", "8900000000000000001", "cellular")
}
func reviewWaitMedia(t *testing.T, controller *Controller) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		calls := controller.Calls()
		if len(calls) > 0 && calls[0].MediaReady {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("active call did not expose media")
}

func TestReviewCallIDsSurviveControllerRecreation(t *testing.T) {
	first, _ := reviewController(nil)
	second, _ := reviewController(nil)
	a, err := first.Dial(context.Background(), "12345", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Dial(context.Background(), "12345", false)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("recreated controller reused history identity %q", a.ID)
	}
}

func TestReviewPollDoesNotEndPendingPreparation(t *testing.T) {
	audio := &reviewAudio{entered: make(chan struct{}), release: make(chan struct{})}
	controller, at := reviewController(audio)
	done := make(chan error, 1)
	go func() { _, err := controller.Dial(context.Background(), "12345", true); done <- err }()
	<-audio.entered
	pollDone := make(chan error, 1)
	go func() { pollDone <- controller.Poll(context.Background()) }()
	var pollErr error
	pollReturned := false
	// A controller may ignore CLCC while pending, or serialize Poll behind
	// preparation. Both are valid; let the latter proceed after release.
	select {
	case pollErr = <-pollDone:
		pollReturned = true
	case <-time.After(50 * time.Millisecond):
	}
	close(audio.release)
	if err := <-done; err != nil {
		t.Fatalf("pending dial was lost: %v", err)
	}
	if !pollReturned {
		pollErr = <-pollDone
	}
	if pollErr != nil || at.dials.Load() != 1 {
		t.Fatalf("poll=%v dials=%d", pollErr, at.dials.Load())
	}
	if audio.started.Load() != 0 {
		t.Fatal("capture started before actual active state")
	}
	_ = controller.Hangup(context.Background(), "")
}

func TestReviewIncomingCallPreparesThenStartsOnActive(t *testing.T) {
	audio := &reviewAudio{}
	controller, _ := reviewController(audio)
	reviewObserve(controller, 1, 4)
	calls := controller.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls=%+v", calls)
	}
	if _, err := controller.Answer(context.Background(), calls[0].ID); err != nil {
		t.Fatal(err)
	}
	if audio.prepared.Load() != 1 || audio.started.Load() != 0 {
		t.Fatalf("prepare=%d start=%d", audio.prepared.Load(), audio.started.Load())
	}
	reviewObserve(controller, 1, 0)
	reviewWaitMedia(t, controller)
	if audio.started.Load() != 1 {
		t.Fatalf("capture starts=%d", audio.started.Load())
	}
	_ = controller.Hangup(context.Background(), calls[0].ID)
}

func TestReviewReleasedMediaCannotWriteNextCall(t *testing.T) {
	audio := &reviewAudio{}
	controller, _ := reviewController(audio)
	first, err := controller.Dial(context.Background(), "12345", true)
	if err != nil {
		t.Fatal(err)
	}
	reviewObserve(controller, 0, 0)
	reviewWaitMedia(t, controller)
	port, release, err := controller.OpenMedia(first.ID, "browser")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := port.WritePCM([]int16{1}); err == nil {
		t.Fatal("released media can still write")
	}
	_ = controller.Hangup(context.Background(), first.ID)
	second, err := controller.Dial(context.Background(), "12345", true)
	if err != nil {
		t.Fatal(err)
	}
	reviewObserve(controller, 0, 0)
	reviewWaitMedia(t, controller)
	if err := port.WritePCM([]int16{2}); err == nil || audio.writes.Load() != 0 {
		t.Fatalf("old media reached new call: err=%v writes=%d", err, audio.writes.Load())
	}
	_ = controller.Hangup(context.Background(), second.ID)
}
