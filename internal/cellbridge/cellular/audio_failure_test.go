package cellular

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type failingPipeAudio struct {
	reviewAudio
	capture bool
}

func (audio *failingPipeAudio) ReadPCM(samples []int16) (int, error) {
	if audio.capture {
		return 0, io.EOF
	}
	return audio.reviewAudio.ReadPCM(samples)
}

func (audio *failingPipeAudio) WritePCM([]int16) error {
	return errors.New("playback process exited")
}

func TestAudioPipeFailureEndsCallAndClearsMediaReady(t *testing.T) {
	for _, capture := range []bool{true, false} {
		name := "playback"
		if capture {
			name = "capture"
		}
		t.Run(name, func(t *testing.T) {
			controller, _ := reviewController(nil)
			at := &recAT{}
			controller.AT = at
			controller.Audio = &failingPipeAudio{capture: capture}
			call, err := controller.Dial(context.Background(), "12345", true)
			if err != nil {
				t.Fatal(err)
			}
			reviewObserve(controller, 0, 0)
			if !capture {
				media, release, err := controller.OpenMedia(call.ID, "browser")
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				if err := media.WritePCM([]int16{123}); err == nil {
					t.Fatal("dead playback pipe accepted microphone samples")
				}
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				calls := controller.Calls()
				if len(calls) == 1 && calls[0].EndedAt != nil {
					if calls[0].ID != call.ID || calls[0].State != "failed" || calls[0].MediaReady || at.count("ATH") != 1 {
						t.Fatalf("pipe failure left a usable or wrong call: %+v, ATH=%d", calls, at.count("ATH"))
					}
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("dead audio pipe left the call active")
		})
	}
}
