// Package audiowav writes 8 kHz stereo call recordings.
//
// The frame pairing was extracted from vocat/internal/vowifi/ims so IMS and
// the cellular bridge share one WAV implementation. Channel 0 is the remote
// party and channel 1 is the local microphone.
package audiowav

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const wavHeaderSize = 44

// Recorder persists signed 16-bit PCM. Frames are paired by sample position.
// Whichever side is ahead waits; the lagging side is padded with silence when
// the file is closed. Write errors are returned to the caller of Close and
// swallowed by Write so a recorder failure does not have to break the call.
type Recorder struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	frames  int64
	downBuf []int16
	upBuf   []int16
	closed  bool
}

// New creates a stereo WAV at path and writes a placeholder header.
func New(path string) (*Recorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	header := make([]byte, wavHeaderSize)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 0)
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 2)
	binary.LittleEndian.PutUint32(header[24:], 8000)
	binary.LittleEndian.PutUint32(header[28:], 8000*2*2)
	binary.LittleEndian.PutUint16(header[32:], 4)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], 0)
	if _, err := file.Write(header); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &Recorder{file: file, path: path}, nil
}

// Path returns the file path passed to New.
func (recorder *Recorder) Path() string {
	if recorder == nil {
		return ""
	}
	return recorder.path
}

// WriteDownlink appends remote-party samples to channel 0.
func (recorder *Recorder) WriteDownlink(samples []int16) {
	recorder.append(samples, &recorder.downBuf)
}

// WriteUplink appends local-microphone samples to channel 1.
func (recorder *Recorder) WriteUplink(samples []int16) {
	recorder.append(samples, &recorder.upBuf)
}

func (recorder *Recorder) append(samples []int16, buffer *[]int16) {
	if recorder == nil {
		return
	}
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

// Close flushes buffered samples and patches the RIFF sizes.
func (recorder *Recorder) Close() error {
	if recorder == nil {
		return nil
	}
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
	header := make([]byte, 4)
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
