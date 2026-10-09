package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vocat/internal/cellbridge/qdc507"
	"vocat/internal/store"
)

func TestBridgeAudioPrepareKeepsOwnerAndCloseDoesNotRepublish(t *testing.T) {
	newAudio := func() *bridgeAudio {
		return &bridgeAudio{
			spec: cellBridgeAudioSpec{DeviceID: "dji-1", CaptureDevice: "plughw:1,0", PlaybackDevice: "plughw:1,0"},
			usbLookup: func(context.Context) (string, string, error) {
				return "1-1.2", "1:8", nil
			},
			check: func(string, string, string) error { return nil },
			probe: func(context.Context) error { return nil },
		}
	}

	t.Run("concurrent prepare does not replace the owner", func(t *testing.T) {
		audio := newAudio()
		var mu sync.Mutex
		owners := map[*qdc507.Runtime]bool{}
		var prepared atomic.Int32
		var inFlight atomic.Int32
		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		audio.ownerCheck = func(rt *qdc507.Runtime) bool {
			mu.Lock()
			defer mu.Unlock()
			return owners[rt]
		}
		audio.prepare = func(context.Context, qdc507.Config) (qdc507.Status, error) {
			if inFlight.Add(1) != 1 {
				t.Errorf("route prepare overlapped")
			}
			defer inFlight.Add(-1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			rt := &qdc507.Runtime{}
			mu.Lock()
			owners[rt] = prepared.Add(1) == 1
			mu.Unlock()
			return qdc507.Status{Ready: true, Runtime: rt}, nil
		}
		errCh := make(chan error, 2)
		go func() { errCh <- audio.ensureRoute(context.Background()) }()
		go func() { errCh <- audio.ensureRoute(context.Background()) }()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("prepare did not start")
		}
		time.Sleep(30 * time.Millisecond)
		if prepared.Load() != 0 {
			t.Fatal("second prepare ran before the first finished")
		}
		close(release)
		for i := 0; i < 2; i++ {
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("ensureRoute did not finish")
			}
		}
		audio.mu.Lock()
		defer audio.mu.Unlock()
		if !audio.ready || len(audio.owners) != 1 || audio.runtime != audio.owners[0] {
			t.Fatalf("published ready=%v owners=%d runtime=%p", audio.ready, len(audio.owners), audio.runtime)
		}
		if prepared.Load() != 1 {
			t.Fatalf("serialized prepare ran %d times", prepared.Load())
		}
	})

	t.Run("later prepare keeps the stop owner", func(t *testing.T) {
		audio := newAudio()
		var mu sync.Mutex
		owners := map[*qdc507.Runtime]bool{}
		var calls atomic.Int32
		audio.ownerCheck = func(rt *qdc507.Runtime) bool {
			mu.Lock()
			defer mu.Unlock()
			return owners[rt]
		}
		audio.live = func(*qdc507.Runtime) bool { return false }
		audio.prepare = func(context.Context, qdc507.Config) (qdc507.Status, error) {
			rt := &qdc507.Runtime{}
			n := calls.Add(1)
			mu.Lock()
			owners[rt] = n == 1
			mu.Unlock()
			return qdc507.Status{Ready: true, Runtime: rt}, nil
		}
		if err := audio.ensureRoute(context.Background()); err != nil {
			t.Fatal(err)
		}
		audio.mu.Lock()
		first := audio.runtime
		audio.verifiedAt = time.Now().Add(-cellBridgeRouteVerifyInterval - time.Second)
		audio.mu.Unlock()
		if err := audio.ensureRoute(context.Background()); err != nil {
			t.Fatal(err)
		}
		audio.mu.Lock()
		defer audio.mu.Unlock()
		if len(audio.owners) != 1 || audio.owners[0] != first || audio.runtime != first || !audio.ready {
			t.Fatalf("owner lost ready=%v owners=%d runtime=%p first=%p", audio.ready, len(audio.owners), audio.runtime, first)
		}
	})

	t.Run("close during prepare does not publish", func(t *testing.T) {
		audio := newAudio()
		entered := make(chan struct{})
		var late *qdc507.Runtime
		audio.prepare = func(context.Context, qdc507.Config) (qdc507.Status, error) {
			close(entered)
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				audio.mu.Lock()
				closed := audio.closed
				audio.mu.Unlock()
				if closed {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			late = &qdc507.Runtime{}
			return qdc507.Status{Ready: true, Runtime: late}, nil
		}
		errCh := make(chan error, 1)
		go func() { errCh <- audio.ensureRoute(context.Background()) }()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("prepare did not start")
		}
		closed := make(chan error, 1)
		go func() { closed <- audio.Close(context.Background()) }()
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("closed prepare was published as ready")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("prepare did not finish after Close")
		}
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Close did not finish")
		}
		audio.mu.Lock()
		defer audio.mu.Unlock()
		if audio.ready || audio.runtime != nil || len(audio.owners) != 0 {
			t.Fatalf("closed route was republished ready=%v runtime=%p owners=%d", audio.ready, audio.runtime, len(audio.owners))
		}
	})

	t.Run("generation change after prepare is not published", func(t *testing.T) {
		audio := newAudio()
		generation := "1:8"
		audio.usbLookup = func(context.Context) (string, string, error) {
			return "1-1.2", generation, nil
		}
		audio.prepare = func(context.Context, qdc507.Config) (qdc507.Status, error) {
			generation = "1:9"
			return qdc507.Status{Ready: true, Runtime: &qdc507.Runtime{}}, nil
		}
		if err := audio.ensureRoute(context.Background()); err == nil {
			t.Fatal("prepare published a route after the USB generation changed")
		}
		audio.mu.Lock()
		defer audio.mu.Unlock()
		if audio.ready || audio.runtime != nil {
			t.Fatal("mismatched generation was published")
		}
	})
}

func TestModuleUSBIgnoresEditableStorePath(t *testing.T) {
	app := newBridgeApp(t, false)
	if err := app.handler.store.UpsertDevice(context.Background(), store.Device{
		ID: "dji-1", Name: "dji-1", DeviceType: store.DeviceTypeDJI4G, USBPath: "9-9",
	}); err != nil {
		t.Fatal(err)
	}
	entry := djiEntry("dji-1")
	entry.Candidate.USBPath = ""
	app.modem.entries["dji-1"] = entry
	audio := newBridgeAudio(app.handler, cellBridgeAudioSpec{DeviceID: "dji-1"})
	if _, _, err := audio.moduleUSB(context.Background()); err == nil {
		t.Fatal("missing candidate USB path fell back to the editable store path")
	}
}
