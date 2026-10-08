// Adapted from CellBridge gateway/internal/voice/alsa.go
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: a playback start failure reaps the capture process that
// already started; Start and Stop do not hold the mutex across process
// waits; device names are validated before exec; process creation is
// injectable so the lifecycle can be tested without hardware.

package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

const (
	SampleRate   = 8000
	Channels     = 1
	FrameSamples = SampleRate / 50
)

var deviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_:,.=-]{1,80}$`)

// waitTimeout bounds a child Wait after Kill. Tests shorten it.
var waitTimeout = 3 * time.Second

// Audio runs arecord and aplay against one USB audio device. Capture is
// started first: on the QDC507 gadget, starting playback first locks the
// full-duplex clock and capture stays silent.
type Audio struct {
	captureDevice  string
	playbackDevice string

	mu        sync.Mutex
	epoch     uint64
	call      string
	starting  bool
	closed    bool
	capture   child
	playback  child
	rx        io.ReadCloser
	tx        io.WriteCloser
	txMu      sync.Mutex
	primeStop chan struct{}
	lastWrite time.Time
	prime     bool
	spawn     spawner
	once      sync.Once
	watch     sync.WaitGroup
}

type child interface {
	Start() error
	Kill() error
	Wait() error
	StdoutPipe() (io.ReadCloser, error)
	StdinPipe() (io.WriteCloser, error)
}

type spawner func(name string, args ...string) (child, error)

type execChild struct {
	cmd      *exec.Cmd
	waitOnce sync.Once
	waitErr  error
}

func (child *execChild) Start() error { return child.cmd.Start() }
func (child *execChild) Kill() error {
	if child.cmd.Process == nil {
		return nil
	}
	return child.cmd.Process.Kill()
}
func (child *execChild) Wait() error {
	child.waitOnce.Do(func() { child.waitErr = child.cmd.Wait() })
	return child.waitErr
}
func (child *execChild) StdoutPipe() (io.ReadCloser, error) { return child.cmd.StdoutPipe() }
func (child *execChild) StdinPipe() (io.WriteCloser, error) { return child.cmd.StdinPipe() }

func defaultSpawn(name string, args ...string) (child, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = io.Discard
	return &execChild{cmd: cmd}, nil
}

// OpenALSA checks the device names and returns a stopped audio pair.
func OpenALSA(captureDevice, playbackDevice string) (*Audio, error) {
	if err := ValidateDevice(captureDevice); err != nil {
		return nil, fmt.Errorf("capture device: %w", err)
	}
	if err := ValidateDevice(playbackDevice); err != nil {
		return nil, fmt.Errorf("playback device: %w", err)
	}
	return &Audio{
		captureDevice:  captureDevice,
		playbackDevice: playbackDevice,
		prime:          true,
		spawn:          defaultSpawn,
	}, nil
}

// ValidateDevice rejects empty names and shell metacharacters. arecord and
// aplay receive the name as one argument, and this keeps a configured value
// from being mistaken for a command.
func ValidateDevice(name string) error {
	if !deviceNamePattern.MatchString(name) {
		return fmt.Errorf("ALSA device %q is invalid", name)
	}
	return nil
}

// Probe reports whether the host has arecord and aplay. A missing tool is an
// unavailable reason, not a successful audio path.
func Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := exec.LookPath("arecord"); err != nil {
		return fmt.Errorf("arecord is unavailable: %w", err)
	}
	if _, err := exec.LookPath("aplay"); err != nil {
		return fmt.Errorf("aplay is unavailable: %w", err)
	}
	return nil
}

func reap(proc child) {
	if proc == nil {
		return
	}
	_ = proc.Kill()
	done := make(chan struct{})
	go func() {
		_ = proc.Wait()
		close(done)
	}()
	timer := time.NewTimer(waitTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// Start opens capture, then playback. The mutex is not held while a child
// starts or while a failed child is reaped.
func (audio *Audio) Start(ctx context.Context, callID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if callID == "" {
		return errors.New("ALSA call id is required")
	}
	audio.mu.Lock()
	if audio.closed {
		audio.mu.Unlock()
		return errors.New("ALSA audio is closed")
	}
	if audio.starting {
		audio.mu.Unlock()
		return errors.New("ALSA audio is starting")
	}
	if audio.call != "" && audio.call != callID {
		current := audio.call
		audio.mu.Unlock()
		return fmt.Errorf("ALSA audio is already active for call %s", current)
	}
	if audio.call == callID && !audio.starting {
		audio.mu.Unlock()
		return nil
	}
	epoch := audio.epoch
	audio.call = callID
	audio.starting = true
	audio.mu.Unlock()

	abort := func(capture, playback child, rx io.ReadCloser, tx io.WriteCloser, err error) error {
		if rx != nil {
			_ = rx.Close()
		}
		if tx != nil {
			_ = tx.Close()
		}
		reap(capture)
		reap(playback)
		audio.mu.Lock()
		if audio.epoch == epoch && audio.call == callID {
			audio.call = ""
			audio.starting = false
		}
		audio.mu.Unlock()
		return err
	}

	capture, err := audio.spawn("arecord", "-q", "-D", audio.captureDevice, "-t", "raw", "-f", "S16_LE", "-r", fmt.Sprint(SampleRate), "-c", fmt.Sprint(Channels), "--buffer-size=8192", "--period-size=1024")
	if err != nil {
		return abort(nil, nil, nil, nil, fmt.Errorf("create ALSA capture: %w", err))
	}
	playback, err := audio.spawn("aplay", "-q", "-D", audio.playbackDevice, "-t", "raw", "-f", "S16_LE", "-r", fmt.Sprint(SampleRate), "-c", fmt.Sprint(Channels), "--buffer-size=8192", "--period-size=1024")
	if err != nil {
		return abort(nil, nil, nil, nil, fmt.Errorf("create ALSA playback: %w", err))
	}
	rx, err := capture.StdoutPipe()
	if err != nil {
		return abort(capture, playback, nil, nil, fmt.Errorf("create ALSA capture pipe: %w", err))
	}
	tx, err := playback.StdinPipe()
	if err != nil {
		return abort(capture, playback, rx, nil, fmt.Errorf("create ALSA playback pipe: %w", err))
	}
	if err := capture.Start(); err != nil {
		return abort(capture, playback, rx, tx, fmt.Errorf("start ALSA capture: %w", err))
	}
	if err := playback.Start(); err != nil {
		// arecord is already running. Reap it even though it was never published.
		return abort(capture, playback, rx, tx, fmt.Errorf("start ALSA playback: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return abort(capture, playback, rx, tx, err)
	}

	audio.mu.Lock()
	if audio.epoch != epoch || audio.call != callID {
		audio.mu.Unlock()
		_ = rx.Close()
		_ = tx.Close()
		reap(capture)
		reap(playback)
		return errors.New("ALSA start was cancelled")
	}
	audio.capture = capture
	audio.playback = playback
	audio.rx = rx
	audio.tx = tx
	audio.starting = false
	var primeStop chan struct{}
	if audio.prime {
		primeStop = make(chan struct{})
		audio.primeStop = primeStop
	}
	proc, monitored := playback.(*execChild)
	if monitored {
		audio.watch.Add(1)
	}
	audio.mu.Unlock()
	if monitored {
		go func() {
			defer audio.watch.Done()
			_ = proc.Wait()
			// A dead playback child closes capture too. Its EOF lets the
			// controller clear MediaReady and report the audio failure.
			_ = audio.stop(callID, epoch, true)
		}()
	}
	if primeStop != nil {
		go audio.primeLoop(callID, primeStop)
	}
	return nil
}

func (audio *Audio) primeLoop(callID string, stop chan struct{}) {
	silence := make([]int16, FrameSamples)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			audio.mu.Lock()
			active := audio.call == callID
			last := audio.lastWrite
			audio.mu.Unlock()
			if !active {
				return
			}
			if !last.IsZero() && time.Since(last) < 15*time.Millisecond {
				continue
			}
			_ = audio.WritePCM(silence)
		}
	}
}

func (audio *Audio) snapshot() (io.ReadCloser, io.WriteCloser, string) {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	return audio.rx, audio.tx, audio.call
}

// ReadPCM reads one caller-sized block of signed little-endian samples.
func (audio *Audio) ReadPCM(samples []int16) (int, error) {
	rx, _, callID := audio.snapshot()
	if callID == "" || rx == nil {
		return 0, errors.New("ALSA audio is not active")
	}
	if len(samples) == 0 {
		return 0, nil
	}
	buffer := make([]byte, len(samples)*2)
	count, err := io.ReadFull(rx, buffer)
	for index := 0; index+1 < count; index += 2 {
		samples[index/2] = int16(binary.LittleEndian.Uint16(buffer[index : index+2]))
	}
	return count / 2, err
}

// WritePCM writes signed little-endian samples to playback.
func (audio *Audio) WritePCM(samples []int16) error {
	_, tx, callID := audio.snapshot()
	if callID == "" || tx == nil {
		return errors.New("ALSA audio is not active")
	}
	if len(samples) == 0 {
		return nil
	}
	buffer := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(buffer[index*2:], uint16(sample))
	}
	audio.txMu.Lock()
	_, err := tx.Write(buffer)
	audio.mu.Lock()
	audio.lastWrite = time.Now()
	audio.mu.Unlock()
	audio.txMu.Unlock()
	return err
}

// Stop reaps only the processes this audio value published or, if Start is
// still in flight, bumps the epoch so Start reaps its own children.
func (audio *Audio) Stop(context.Context) error {
	return audio.stop("", 0, false)
}

func (audio *Audio) stop(callID string, epoch uint64, conditional bool) error {
	audio.mu.Lock()
	if conditional && (audio.call != callID || audio.epoch != epoch) {
		audio.mu.Unlock()
		return nil
	}
	audio.epoch++
	capture, playback := audio.capture, audio.playback
	rx, tx := audio.rx, audio.tx
	primeStop := audio.primeStop
	audio.capture, audio.playback, audio.rx, audio.tx = nil, nil, nil, nil
	audio.primeStop = nil
	audio.call = ""
	audio.starting = false
	audio.mu.Unlock()

	if primeStop != nil {
		close(primeStop)
	}
	if rx != nil {
		_ = rx.Close()
	}
	if tx != nil {
		_ = tx.Close()
	}
	reap(capture)
	reap(playback)
	return nil
}

// Close stops the pair once.
func (audio *Audio) Close() error {
	var err error
	audio.once.Do(func() {
		audio.mu.Lock()
		audio.closed = true
		audio.mu.Unlock()
		err = audio.Stop(context.Background())
		audio.watch.Wait()
	})
	return err
}

// ActiveCall returns the call id that owns the pair, if any.
func (audio *Audio) ActiveCall() string {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	return audio.call
}
