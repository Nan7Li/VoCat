package voice

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeChild struct {
	name     string
	startErr error
	startFn  func() error
	killed   atomic.Bool
	waited   atomic.Bool
	stdout   io.ReadCloser
	stdin    io.WriteCloser
}

func (child *fakeChild) Start() error {
	if child.startFn != nil {
		return child.startFn()
	}
	return child.startErr
}
func (child *fakeChild) Kill() error {
	child.killed.Store(true)
	return nil
}
func (child *fakeChild) Wait() error {
	child.waited.Store(true)
	return nil
}
func (child *fakeChild) StdoutPipe() (io.ReadCloser, error) {
	reader, writer := io.Pipe()
	child.stdout = reader
	go func() { _, _ = writer.Write(make([]byte, 320)); _ = writer.Close() }()
	return reader, nil
}
func (child *fakeChild) StdinPipe() (io.WriteCloser, error) {
	reader, writer := io.Pipe()
	child.stdin = writer
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	return writer, nil
}

func testAudio(t *testing.T, spawn spawner) *Audio {
	t.Helper()
	audio, err := OpenALSA("plughw:1,0", "plughw:1,0")
	if err != nil {
		t.Fatal(err)
	}
	audio.prime = false
	audio.spawn = spawn
	previous := waitTimeout
	waitTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		waitTimeout = previous
		_ = audio.Close()
	})
	return audio
}

func TestPlaybackStartFailureReapsCapture(t *testing.T) {
	var capture, playback *fakeChild
	audio := testAudio(t, func(name string, _ ...string) (child, error) {
		created := &fakeChild{name: name}
		switch name {
		case "arecord":
			capture = created
		case "aplay":
			playback = created
			created.startErr = errors.New("aplay device busy")
		}
		return created, nil
	})
	err := audio.Start(context.Background(), "call-1")
	if err == nil || capture == nil || playback == nil {
		t.Fatalf("start err=%v capture=%v playback=%v", err, capture, playback)
	}
	if !capture.killed.Load() || !capture.waited.Load() {
		t.Fatalf("capture killed=%v waited=%v", capture.killed.Load(), capture.waited.Load())
	}
	if audio.ActiveCall() != "" {
		t.Fatalf("call still active: %q", audio.ActiveCall())
	}
	audio.spawn = func(name string, args ...string) (child, error) {
		return &fakeChild{name: name}, nil
	}
	if err := audio.Start(context.Background(), "call-2"); err != nil {
		t.Fatal(err)
	}
	if audio.ActiveCall() != "call-2" {
		t.Fatalf("active = %q", audio.ActiveCall())
	}
}

func TestStopDuringStartReapsBothChildren(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var capture, playback *fakeChild
	audio := testAudio(t, func(name string, _ ...string) (child, error) {
		created := &fakeChild{name: name}
		if name == "arecord" {
			capture = created
		}
		if name == "aplay" {
			playback = created
			created.startFn = func() error {
				once.Do(func() { close(started) })
				<-release
				return nil
			}
		}
		return created, nil
	})
	done := make(chan error, 1)
	go func() {
		done <- audio.Start(context.Background(), "call-race")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("playback start did not block")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- audio.Stop(context.Background()) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked while Start was in flight")
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Start succeeded after Stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not finish after Stop")
	}
	if !capture.killed.Load() || !playback.killed.Load() {
		t.Fatalf("capture killed=%v playback killed=%v", capture.killed.Load(), playback.killed.Load())
	}
	if audio.ActiveCall() != "" {
		t.Fatalf("active call = %q", audio.ActiveCall())
	}
}

func TestALSADeviceNameRejectsShellMetacharacters(t *testing.T) {
	if _, err := OpenALSA("default;rm", "plughw:1,0"); err == nil {
		t.Fatal("expected invalid capture device")
	}
	if err := ValidateDevice("plughw:CARD=Device,DEV=0"); err != nil {
		t.Fatal(err)
	}
}
