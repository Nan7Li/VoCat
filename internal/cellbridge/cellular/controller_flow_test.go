package cellular

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vocat/internal/modem"
)

func cellularAuth(physical, iccid string) Authorization {
	return Authorization{Kind: "cellular", DeviceID: "dji", PhysicalID: physical, ICCID: iccid}
}

type memGate struct {
	mu   sync.Mutex
	auth Authorization
}

func (gate *memGate) Authorize(context.Context) (Authorization, error) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.auth, nil
}

func (gate *memGate) set(auth Authorization) {
	gate.mu.Lock()
	gate.auth = auth
	gate.mu.Unlock()
}

type recAT struct {
	mu    sync.Mutex
	cmds  []string
	devs  []string
	onATD func(context.Context) (modem.Response, error)
}

func (at *recAT) ExecuteAT(ctx context.Context, device, command string) (modem.Response, error) {
	at.mu.Lock()
	at.cmds = append(at.cmds, command)
	at.devs = append(at.devs, device)
	hook := at.onATD
	at.mu.Unlock()
	if hook != nil && len(command) >= 3 && command[:3] == "ATD" {
		return hook(ctx)
	}
	return modem.Response{Final: "OK"}, nil
}

func (at *recAT) count(prefix string) int {
	at.mu.Lock()
	defer at.mu.Unlock()
	n := 0
	for _, command := range at.cmds {
		if len(command) >= len(prefix) && command[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (at *recAT) devices(prefix string) []string {
	at.mu.Lock()
	defer at.mu.Unlock()
	var result []string
	for i, command := range at.cmds {
		if len(command) >= len(prefix) && command[:len(prefix)] == prefix {
			result = append(result, at.devs[i])
		}
	}
	return result
}

type scriptAudio struct {
	mu          sync.Mutex
	stop        chan struct{}
	entered     chan struct{}
	readEntered chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	readOnce    sync.Once

	prepareErr error
	startErr   error
	downlink   []int16
	// holdFirst makes the first Read return its samples even if Stop already
	// ran, so a late capture cannot be confused with a clean shutdown.
	holdFirst bool

	prepared atomic.Int32
	started  atomic.Int32
	writes   atomic.Int32
	reads    atomic.Int32
}

func (audio *scriptAudio) Prepare(ctx context.Context, _ string) error {
	audio.prepared.Add(1)
	if audio.prepareErr != nil {
		return audio.prepareErr
	}
	if audio.entered != nil {
		audio.enterOnce.Do(func() { close(audio.entered) })
		select {
		case <-audio.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (audio *scriptAudio) Start(context.Context, string) error {
	audio.started.Add(1)
	if audio.startErr != nil {
		return audio.startErr
	}
	audio.mu.Lock()
	audio.stop = make(chan struct{})
	audio.mu.Unlock()
	return nil
}

func (audio *scriptAudio) ReadPCM(buf []int16) (int, error) {
	if audio.reads.Add(1) == 1 && audio.release != nil && len(audio.downlink) > 0 {
		if audio.readEntered != nil {
			audio.readOnce.Do(func() { close(audio.readEntered) })
		}
		if audio.holdFirst {
			<-audio.release
			n := copy(buf, audio.downlink)
			return n, nil
		}
		select {
		case <-audio.release:
			n := copy(buf, audio.downlink)
			return n, nil
		case <-audio.stopped():
			return 0, errors.New("capture stopped")
		}
	}
	<-audio.stopped()
	return 0, errors.New("capture stopped")
}

func (audio *scriptAudio) stopped() <-chan struct{} {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	if audio.stop == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return audio.stop
}

func (audio *scriptAudio) WritePCM([]int16) error {
	audio.writes.Add(1)
	return nil
}

func (audio *scriptAudio) Stop(context.Context) error {
	audio.mu.Lock()
	defer audio.mu.Unlock()
	if audio.stop != nil {
		select {
		case <-audio.stop:
		default:
			close(audio.stop)
		}
	}
	return nil
}

func clccLine(index, direction, state, mode int, number string) string {
	return "+CLCC: " + itoa(index) + "," + itoa(direction) + "," + itoa(state) + "," + itoa(mode) + ",0,\"" + number + "\",129"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [4]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

func observeLines(controller *Controller, lines ...string) {
	controller.Observe(modem.Response{Final: "OK", Lines: lines}, "phys-a", "8900000000000000001", "cellular")
}

func TestDialActiveMediaHangupWritesWav(t *testing.T) {
	audio := &scriptAudio{
		readEntered: make(chan struct{}),
		release:     make(chan struct{}),
		downlink:    []int16{11, 22, 33},
	}
	gate := &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}
	at := &recAT{}
	dir := t.TempDir()
	controller := &Controller{
		DeviceID: "dji", Gate: gate, AT: at, Audio: audio, RecordingsDir: dir,
	}
	call, err := controller.Dial(context.Background(), "12345", true)
	if err != nil {
		t.Fatal(err)
	}
	observeLines(controller, clccLine(1, 0, 0, 0, "12345"))
	<-audio.readEntered
	if audio.started.Load() != 1 {
		t.Fatalf("start=%d", audio.started.Load())
	}
	calls := controller.Calls()
	if len(calls) != 1 || !calls[0].MediaReady || calls[0].Codec != "PCMU" || calls[0].Recording == "" {
		t.Fatalf("calls=%+v", calls)
	}
	port, release, err := controller.OpenMedia(call.ID, "browser")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	readDone := make(chan []int16, 1)
	go func() {
		samples, err := port.ReadPCM(context.Background())
		if err != nil {
			t.Errorf("read pcm: %v", err)
		}
		readDone <- samples
	}()
	if err := port.WritePCM([]int16{101, 202, 303}); err != nil {
		t.Fatal(err)
	}
	close(audio.release)
	select {
	case samples := <-readDone:
		if len(samples) != 3 || samples[0] != 11 || samples[1] != 22 || samples[2] != 33 {
			t.Fatalf("downlink=%v", samples)
		}
	case <-time.After(time.Second):
		t.Fatal("downlink was not delivered")
	}
	deadline := time.Now().Add(time.Second)
	recording := filepath.Join(dir, calls[0].Recording)
	for time.Now().Before(deadline) {
		info, err := os.Stat(recording)
		if err == nil && info.Size() >= 44+12 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := controller.Hangup(context.Background(), call.ID); err != nil {
		t.Fatal(err)
	}
	down, up := readStereoWav(t, recording)
	if len(down) < 3 || down[0] != 11 || down[1] != 22 || down[2] != 33 {
		t.Fatalf("wav downlink=%v", down)
	}
	if len(up) < 3 || up[0] != 101 || up[1] != 202 || up[2] != 303 {
		t.Fatalf("wav uplink=%v", up)
	}
	if at.count("ATH") != 1 || at.devices("ATH")[0] != "phys-a" {
		t.Fatalf("ath=%v", at.devices("ATH"))
	}
	ended := controller.Calls()
	if len(ended) != 1 || ended[0].State != "ended" || ended[0].EndedAt == nil {
		t.Fatalf("ended=%+v", ended)
	}
}

func TestPrepareFailureAndReaderDoNotDial(t *testing.T) {
	audio := &scriptAudio{prepareErr: errors.New("qdc down")}
	at := &recAT{}
	controller := &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: at, Audio: audio}
	_, err := controller.Dial(context.Background(), "12345", true)
	if err == nil || at.count("ATD") != 0 || audio.started.Load() != 0 {
		t.Fatalf("err=%v atd=%d started=%d", err, at.count("ATD"), audio.started.Load())
	}
	calls := controller.Calls()
	if len(calls) != 1 || calls[0].State != "failed" {
		t.Fatalf("calls=%+v", calls)
	}

	readerAT := &recAT{}
	reader := &Controller{
		DeviceID: "dji",
		Gate:     &memGate{auth: Authorization{Kind: "reader", DeviceID: "dji", PhysicalID: "reader-1", Reason: "sim reader"}},
		AT:       readerAT,
		Audio:    &scriptAudio{},
	}
	_, err = reader.Dial(context.Background(), "12345", true)
	if !errors.Is(err, ErrNotCellular) || readerAT.count("ATD") != 0 || len(reader.Calls()) != 0 {
		t.Fatalf("err=%v atd=%d calls=%+v", err, readerAT.count("ATD"), reader.Calls())
	}
}

func TestCancelledPrepareDoesNotDial(t *testing.T) {
	audio := &scriptAudio{entered: make(chan struct{}), release: make(chan struct{})}
	at := &recAT{}
	controller := &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: at, Audio: audio}
	done := make(chan error, 1)
	go func() {
		_, err := controller.Dial(context.Background(), "12345", true)
		done <- err
	}()
	<-audio.entered
	if err := controller.Hangup(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	close(audio.release)
	if err := <-done; err == nil {
		t.Fatal("cancelled prepare still dialed")
	}
	if at.count("ATD") != 0 || at.count("ATH") != 0 {
		t.Fatalf("commands=%v", at.cmds)
	}
}

func TestLateATDDeadlineHangsUpOriginalIdentity(t *testing.T) {
	gate := &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}
	started := make(chan struct{})
	at := &recAT{onATD: func(ctx context.Context) (modem.Response, error) {
		close(started)
		<-ctx.Done()
		return modem.Response{Final: "OK"}, nil
	}}
	controller := &Controller{DeviceID: "dji", Gate: gate, AT: at}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := controller.Dial(ctx, "12345", false)
		done <- err
	}()
	<-started
	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if at.count("ATD") != 1 || at.count("ATH") != 1 || at.devices("ATH")[0] != "phys-a" {
		t.Fatalf("cmds=%v devs=%v", at.cmds, at.devs)
	}
	if len(controller.Calls()) != 1 || controller.Calls()[0].EndedAt == nil {
		t.Fatalf("calls=%+v", controller.Calls())
	}

	gate = &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}
	at = &recAT{onATD: func(context.Context) (modem.Response, error) {
		return modem.Response{}, context.DeadlineExceeded
	}}
	controller = &Controller{DeviceID: "dji", Gate: gate, AT: at}
	_, err = controller.Dial(context.Background(), "12345", false)
	if !errors.Is(err, context.DeadlineExceeded) || at.count("ATH") != 1 || at.devices("ATH")[0] != "phys-a" {
		t.Fatalf("err=%v ath=%v", err, at.devices("ATH"))
	}

	gate = &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}
	started = make(chan struct{})
	at = &recAT{onATD: func(ctx context.Context) (modem.Response, error) {
		close(started)
		<-ctx.Done()
		gate.set(cellularAuth("phys-b", "8900000000000000002"))
		return modem.Response{Final: "OK"}, nil
	}}
	controller = &Controller{DeviceID: "dji", Gate: gate, AT: at}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done = make(chan error, 1)
	go func() {
		_, dialErr := controller.Dial(ctx, "12345", false)
		done <- dialErr
	}()
	<-started
	err = <-done
	if err == nil || at.count("ATH") != 0 {
		t.Fatalf("err=%v ath=%d cmds=%v", err, at.count("ATH"), at.cmds)
	}
}

func TestIdentityChangeEndsCallWithoutSignallingNewSIM(t *testing.T) {
	gate := &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}
	at := &recAT{}
	controller := &Controller{DeviceID: "dji", Gate: gate, AT: at}
	if _, err := controller.Dial(context.Background(), "12345", false); err != nil {
		t.Fatal(err)
	}
	gate.set(cellularAuth("phys-a", ""))
	if err := controller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if at.count("ATH") != 0 || at.count("AT+CLCC") != 0 {
		t.Fatalf("cmds=%v", at.cmds)
	}
	calls := controller.Calls()
	if len(calls) != 1 || calls[0].EndedAt == nil || calls[0].Number == "" {
		t.Fatalf("calls=%+v", calls)
	}

	gate.set(cellularAuth("phys-a", "8900000000000000001"))
	at = &recAT{}
	controller = &Controller{DeviceID: "dji", Gate: gate, AT: at, Audio: &scriptAudio{}}
	observeLines(controller, clccLine(1, 1, 4, 0, "12345"))
	gate.set(cellularAuth("phys-b", "8900000000000000099"))
	if _, err := controller.Answer(context.Background(), ""); err == nil {
		t.Fatal("answer signaled a different device")
	}
	if at.count("ATA") != 0 || at.count("ATH") != 0 {
		t.Fatalf("cmds=%v", at.cmds)
	}

	gate.set(cellularAuth("phys-a", "8900000000000000001"))
	at = &recAT{}
	controller = &Controller{DeviceID: "dji", Gate: gate, AT: at}
	if _, err := controller.Dial(context.Background(), "12345", false); err != nil {
		t.Fatal(err)
	}
	gate.set(cellularAuth("phys-a", "8900000000000000099"))
	if err := controller.Hangup(context.Background(), ""); err == nil {
		t.Fatal("hangup signaled a different sim")
	}
	if at.count("ATH") != 0 {
		t.Fatalf("ath devices=%v", at.devices("ATH"))
	}
}

func TestAnswerOnlyPreparesIncomingRinging(t *testing.T) {
	audio := &scriptAudio{}
	at := &recAT{}
	controller := &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: at, Audio: audio}
	if _, err := controller.Dial(context.Background(), "12345", false); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Answer(context.Background(), ""); err == nil || at.count("ATA") != 0 {
		t.Fatalf("outgoing answer err=%v ata=%d", err, at.count("ATA"))
	}
	_ = controller.Hangup(context.Background(), "")

	audio = &scriptAudio{prepareErr: errors.New("qdc down")}
	at = &recAT{}
	controller = &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: at, Audio: audio}
	observeLines(controller, clccLine(1, 1, 4, 0, "12345"))
	if _, err := controller.Answer(context.Background(), ""); err == nil || at.count("ATA") != 0 || audio.prepared.Load() != 1 {
		t.Fatalf("err=%v ata=%d prepare=%d", err, at.count("ATA"), audio.prepared.Load())
	}
	calls := controller.Calls()
	if len(calls) != 1 || calls[0].State != "ringing" || calls[0].Direction != "incoming" {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestStartFailureHangsUpOriginalAndSkipsSwappedSIM(t *testing.T) {
	audio := &scriptAudio{startErr: errors.New("arecord failed")}
	at := &recAT{}
	controller := &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: at, Audio: audio}
	if _, err := controller.Dial(context.Background(), "12345", true); err != nil {
		t.Fatal(err)
	}
	observeLines(controller, clccLine(1, 0, 0, 0, "12345"))
	calls := controller.Calls()
	if len(calls) != 1 || calls[0].State != "failed" || calls[0].MediaReady {
		t.Fatalf("calls=%+v", calls)
	}
	if audio.started.Load() != 1 || at.count("ATH") != 1 || at.devices("ATH")[0] != "phys-a" {
		t.Fatalf("start=%d ath=%v", audio.started.Load(), at.devices("ATH"))
	}

	gate := &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}
	audio = &scriptAudio{startErr: errors.New("arecord failed")}
	at = &recAT{}
	controller = &Controller{DeviceID: "dji", Gate: gate, AT: at, Audio: audio}
	if _, err := controller.Dial(context.Background(), "12345", true); err != nil {
		t.Fatal(err)
	}
	gate.set(cellularAuth("phys-b", "8900000000000000002"))
	observeLines(controller, clccLine(1, 0, 0, 0, "12345"))
	if at.count("ATH") != 0 {
		t.Fatalf("ath sent to swapped line: %v", at.devices("ATH"))
	}
	if calls = controller.Calls(); len(calls) != 1 || calls[0].State != "failed" {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestCLCCBindsVoiceIndexNotCallWaiting(t *testing.T) {
	at := &recAT{}
	controller := &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: at}
	call, err := controller.Dial(context.Background(), "12345", false)
	if err != nil {
		t.Fatal(err)
	}
	observeLines(controller,
		clccLine(2, 1, 5, 0, "99999"),
		clccLine(3, 0, 2, 1, ""),
		clccLine(1, 0, 0, 0, "12345"),
	)
	calls := controller.Calls()
	if len(calls) != 1 || calls[0].ID != call.ID || calls[0].Number != "12345" || calls[0].State != "active" {
		t.Fatalf("calls=%+v", calls)
	}
	observeLines(controller, clccLine(2, 1, 5, 0, "99999"))
	calls = controller.Calls()
	for _, item := range calls {
		if item.ID == call.ID && (item.Number == "99999" || item.EndedAt == nil) {
			t.Fatalf("call waiting replaced original: %+v", item)
		}
	}
	if len(calls) == 0 || calls[0].ID != call.ID || calls[0].Number != "12345" {
		t.Fatalf("history=%+v", calls)
	}

	controller = &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: &recAT{}}
	controller.Observe(modem.Response{Final: "OK", Lines: []string{clccLine(1, 1, 4, 1, "555")}}, "phys-a", "8900000000000000001", "cellular")
	if len(controller.Calls()) != 0 {
		t.Fatalf("data call became voice: %+v", controller.Calls())
	}
	observeLines(controller, clccLine(4, 1, 4, 3, "777"))
	calls = controller.Calls()
	if len(calls) != 1 || calls[0].Number != "777" || calls[0].State != "ringing" {
		t.Fatalf("voice mode 3 was ignored: %+v", calls)
	}
}

func TestEmptyCLCCAfterDialEndsCall(t *testing.T) {
	controller := &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: &recAT{}}
	call, err := controller.Dial(context.Background(), "12345", false)
	if err != nil {
		t.Fatal(err)
	}
	observeLines(controller)
	calls := controller.Calls()
	if len(calls) != 1 || calls[0].ID != call.ID || calls[0].EndedAt == nil {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestLateReadDoesNotCrossIntoNextCall(t *testing.T) {
	audio := &scriptAudio{
		readEntered: make(chan struct{}),
		release:     make(chan struct{}),
		downlink:    []int16{0x1111, 0x2222},
		holdFirst:   true,
	}
	dir := t.TempDir()
	controller := &Controller{
		DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")},
		AT: &recAT{}, Audio: audio, RecordingsDir: dir,
	}
	first, err := controller.Dial(context.Background(), "12345", true)
	if err != nil {
		t.Fatal(err)
	}
	observeLines(controller, clccLine(1, 0, 0, 0, "12345"))
	<-audio.readEntered
	if err := controller.Hangup(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := controller.Dial(context.Background(), "12345", true)
	if err != nil {
		t.Fatal(err)
	}
	observeLines(controller, clccLine(1, 0, 0, 0, "12345"))
	port, release, err := controller.OpenMedia(second.ID, "browser")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	close(audio.release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	samples, err := port.ReadPCM(ctx)
	if err == nil && len(samples) > 0 && samples[0] == 0x1111 {
		t.Fatalf("stale downlink reached the next call: %v", samples)
	}
	_ = controller.Hangup(context.Background(), second.ID)
	recording := ""
	for _, call := range controller.Calls() {
		if call.ID == second.ID {
			recording = call.Recording
		}
	}
	if recording == "" {
		t.Fatal("second call has no recording")
	}
	payload, err := os.ReadFile(filepath.Join(dir, recording))
	if err != nil {
		t.Fatal(err)
	}
	if bytesContain(payload, []byte{0x11, 0x11}) {
		t.Fatalf("stale sample landed in %s", recording)
	}
	if audio.writes.Load() != 0 {
		t.Fatalf("writes=%d", audio.writes.Load())
	}
}

func TestConcurrentMediaReadWriteStop(t *testing.T) {
	audio := &scriptAudio{downlink: []int16{7, 8, 9}}
	controller := &Controller{
		DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")},
		AT: &recAT{}, Audio: audio, RecordingsDir: t.TempDir(),
	}
	call, err := controller.Dial(context.Background(), "12345", true)
	if err != nil {
		t.Fatal(err)
	}
	observeLines(controller, clccLine(1, 0, 0, 0, "12345"))
	port, release, err := controller.OpenMedia(call.ID, "browser")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			_, _ = port.ReadPCM(ctx)
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			_ = port.WritePCM([]int16{int16(i)})
		}
	}()
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		release()
		_ = port.WritePCM([]int16{99})
		_ = controller.Hangup(context.Background(), call.ID)
	}()
	wg.Wait()
}

func TestCallIDsDifferOnOneController(t *testing.T) {
	controller := &Controller{DeviceID: "dji", Gate: &memGate{auth: cellularAuth("phys-a", "8900000000000000001")}, AT: &recAT{}}
	first, err := controller.Dial(context.Background(), "12345", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Hangup(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := controller.Dial(context.Background(), "12345", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(first.ID) < 12 {
		t.Fatalf("ids=%q %q", first.ID, second.ID)
	}
	_ = controller.Hangup(context.Background(), second.ID)
}

func readStereoWav(t *testing.T, path string) (down, up []int16) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) < 44 || string(payload[0:4]) != "RIFF" || string(payload[8:12]) != "WAVE" {
		t.Fatalf("missing wav header (%d bytes)", len(payload))
	}
	if binary.LittleEndian.Uint16(payload[20:22]) != 1 || binary.LittleEndian.Uint16(payload[22:24]) != 2 {
		t.Fatalf("format=%d channels=%d", binary.LittleEndian.Uint16(payload[20:22]), binary.LittleEndian.Uint16(payload[22:24]))
	}
	if binary.LittleEndian.Uint32(payload[24:28]) != 8000 || binary.LittleEndian.Uint16(payload[34:36]) != 16 {
		t.Fatalf("rate=%d bits=%d", binary.LittleEndian.Uint32(payload[24:28]), binary.LittleEndian.Uint16(payload[34:36]))
	}
	data := payload[44:]
	for index := 0; index+3 < len(data); index += 4 {
		down = append(down, int16(binary.LittleEndian.Uint16(data[index:])))
		up = append(up, int16(binary.LittleEndian.Uint16(data[index+2:])))
	}
	return down, up
}

func bytesContain(payload, needle []byte) bool {
	if len(needle) == 0 || len(payload) < len(needle) {
		return false
	}
	for index := 0; index+len(needle) <= len(payload); index++ {
		match := true
		for offset := range needle {
			if payload[index+offset] != needle[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
