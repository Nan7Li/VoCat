package ims

import "vocat/internal/audiowav"

// wavRecorder is the IMS adapter over the shared call WAV writer.
type wavRecorder struct {
	inner *audiowav.Recorder
}

func newWAVRecorder(path string) (*wavRecorder, error) {
	recorder, err := audiowav.New(path)
	if err != nil {
		return nil, err
	}
	return &wavRecorder{inner: recorder}, nil
}

func (recorder *wavRecorder) writeDownlink(samples []int16) {
	if recorder == nil || recorder.inner == nil {
		return
	}
	recorder.inner.WriteDownlink(samples)
}

func (recorder *wavRecorder) writeUplink(samples []int16) {
	if recorder == nil || recorder.inner == nil {
		return
	}
	recorder.inner.WriteUplink(samples)
}

func (recorder *wavRecorder) Close() error {
	if recorder == nil || recorder.inner == nil {
		return nil
	}
	return recorder.inner.Close()
}
