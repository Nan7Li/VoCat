package voice

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestConcurrentStartSameCallHasOneProcessPair(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var starts atomic.Int32
	audio := testAudio(t, func(name string, _ ...string) (child, error) {
		proc := &fakeChild{name: name}
		if name == "arecord" {
			proc.startFn = func() error {
				starts.Add(1)
				close(entered)
				<-release
				return nil
			}
		}
		return proc, nil
	})
	done := make(chan error, 1)
	go func() { done <- audio.Start(context.Background(), "same-call") }()
	<-entered
	err := audio.Start(context.Background(), "same-call")
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if err == nil || starts.Load() != 1 {
		t.Fatalf("second Start=%v, capture starts=%d", err, starts.Load())
	}
}

func TestClosedAudioCannotRestart(t *testing.T) {
	audio := testAudio(t, func(name string, _ ...string) (child, error) { return &fakeChild{name: name}, nil })
	if err := audio.Close(); err != nil {
		t.Fatal(err)
	}
	if err := audio.Start(context.Background(), "after-close"); err == nil {
		t.Fatal("closed audio restarted")
	}
}

func TestCancelDuringPlaybackStartReapsUnpublishedChildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var children []*fakeChild
	audio := testAudio(t, func(name string, _ ...string) (child, error) {
		proc := &fakeChild{name: name}
		children = append(children, proc)
		if name == "aplay" {
			proc.startFn = func() error { cancel(); return nil }
		}
		return proc, nil
	})
	if err := audio.Start(ctx, "cancelled"); err != context.Canceled {
		t.Fatalf("Start error = %v", err)
	}
	for _, proc := range children {
		if !proc.killed.Load() || !proc.waited.Load() {
			t.Fatalf("%s was not reaped", proc.name)
		}
	}
	if audio.ActiveCall() != "" {
		t.Fatal("cancelled start published its call")
	}
}
