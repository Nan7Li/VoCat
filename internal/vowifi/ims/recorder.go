package ims

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const wavHeaderSize = 44

// wavRecorder persists 8 kHz mono PCM from both call directions into one
// stereo WAV: channel 0 is the remote party (downlink), channel 1 is the
// local microphone (uplink). Frames are paired by absolute sample position;
// whichever side is ahead is held back in a small buffer and the lagging
// side is padded with silence at close. Recording must never break the call,
// so every write error is swallowed by the callers.
type wavRecorder struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	frames  int64
	downBuf []int16
	upBuf   []int16
	closed  bool
}

func newWAVRecorder(path string) (*wavRecorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	header := make([]byte, wavHeaderSize)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 0) // patched at close
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)        // PCM
	binary.LittleEndian.PutUint16(header[22:], 2)        // channels
	binary.LittleEndian.PutUint32(header[24:], 8000)     // sample rate
	binary.LittleEndian.PutUint32(header[28:], 8000*2*2) // byte rate
	binary.LittleEndian.PutUint16(header[32:], 4)        // block align
	binary.LittleEndian.PutUint16(header[34:], 16)       // bits per sample
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], 0)
	if _, err := file.Write(header); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &wavRecorder{file: file, path: path}, nil
}

// writeDownlink appends remote-party samples to channel 0.
func (recorder *wavRecorder) writeDownlink(samples []int16) {
	recorder.append(samples, &recorder.downBuf)
}

// writeUplink appends local-microphone samples to channel 1.
func (recorder *wavRecorder) writeUplink(samples []int16) {
	recorder.append(samples, &recorder.upBuf)
}

// append buffers one side and flushes the longest frame-aligned prefix of
// both channels. Buffers are capped at 30 s so a one-sided stream cannot
// exhaust memory; the oldest buffered samples are dropped when the cap is
// hit, which only happens when the other side has been silent for 30 s.
func (recorder *wavRecorder) append(samples []int16, buffer *[]int16) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.closed || recorder.file == nil || len(samples) == 0 {
		return
	}
	const maxBuffered = 8000 * 30
	*buffer = append(*buffer, samples...)
	if len(*buffer) > maxBuffered {
		drop := len(*buffer) - maxBuffered
		*buffer = (*buffer)[drop:]
	}
	frame := make([]byte, 0, 4096)
	for len(recorder.downBuf) > 0 && len(recorder.upBuf) > 0 {
		count := len(recorder.downBuf)
		if len(recorder.upBuf) < count {
			count = len(recorder.upBuf)
		}
		if cap(frame) < count*4 {
			frame = make([]byte, 0, count*4)
		}
		frame = frame[:count*4]
		for index := 0; index < count; index++ {
			binary.LittleEndian.PutUint16(frame[index*4:], uint16(recorder.downBuf[index]))
			binary.LittleEndian.PutUint16(frame[index*4+2:], uint16(recorder.upBuf[index]))
		}
		if _, err := recorder.file.Write(frame); err != nil {
			_ = recorder.file.Close()
			recorder.file = nil
			return
		}
		recorder.frames += int64(count)
		recorder.downBuf = recorder.downBuf[count:]
		recorder.upBuf = recorder.upBuf[count:]
	}
}

// Close flushes remaining buffered samples (padding the lagging side with
// silence) and patches the RIFF sizes so the file is a valid WAV.
func (recorder *wavRecorder) Close() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.closed {
		return nil
	}
	recorder.closed = true
	if recorder.file == nil {
		return errors.New("wav recorder already failed")
	}
	count := len(recorder.downBuf)
	if len(recorder.upBuf) > count {
		count = len(recorder.upBuf)
	}
	if count > 0 {
		frame := make([]byte, count*4)
		for index := 0; index < count; index++ {
			if index < len(recorder.downBuf) {
				binary.LittleEndian.PutUint16(frame[index*4:], uint16(recorder.downBuf[index]))
			}
			if index < len(recorder.upBuf) {
				binary.LittleEndian.PutUint16(frame[index*4+2:], uint16(recorder.upBuf[index]))
			}
		}
		if _, err := recorder.file.Write(frame); err != nil {
			_ = recorder.file.Close()
			return err
		}
		recorder.frames += int64(count)
		recorder.downBuf = nil
		recorder.upBuf = nil
	}
	dataBytes := recorder.frames * 4
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:], uint32(wavHeaderSize-8+dataBytes))
	if _, err := recorder.file.WriteAt(header, 4); err != nil {
		_ = recorder.file.Close()
		return err
	}
	binary.LittleEndian.PutUint32(header[0:], uint32(dataBytes))
	if _, err := recorder.file.WriteAt(header, 40); err != nil {
		_ = recorder.file.Close()
		return err
	}
	return recorder.file.Close()
}
