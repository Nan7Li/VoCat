package voice

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestPlaybackProcessExitStopsCapture(t *testing.T) {
	audio, err := OpenALSA("plughw:1,0", "plughw:1,0")
	if err != nil {
		t.Fatal(err)
	}
	audio.prime = false
	audio.spawn = func(name string, _ ...string) (child, error) {
		if name == "arecord" {
			return &execChild{cmd: exec.Command("sleep", "30")}, nil
		}
		return &execChild{cmd: exec.Command("sh", "-c", "exit 1")}, nil
	}
	t.Cleanup(func() { _ = audio.Close() })
	if err := audio.Start(context.Background(), "dead-playback"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := audio.ReadPCM(make([]int16, 160))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("capture stayed usable after playback exited")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback failure left capture blocked")
	}
	if err := audio.Close(); err != nil {
		t.Fatal(err)
	}
	if audio.ActiveCall() != "" {
		t.Fatal("dead process pair remained active")
	}
}
