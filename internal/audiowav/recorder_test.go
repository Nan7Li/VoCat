package audiowav

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRecorderStereoHeaderAndSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls", "one.wav")
	recorder, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	recorder.WriteDownlink([]int16{1, 2, 3})
	recorder.WriteUplink([]int16{10, 20})
	recorder.WriteUplink([]int16{30, 40})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 44 {
		t.Fatalf("wav length = %d", len(data))
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[36:40]) != "data" {
		t.Fatalf("wav marks = %q %q %q", data[0:4], data[8:12], data[36:40])
	}
	if binary.LittleEndian.Uint16(data[20:22]) != 1 {
		t.Fatalf("format = %d", binary.LittleEndian.Uint16(data[20:22]))
	}
	if binary.LittleEndian.Uint16(data[22:24]) != 2 {
		t.Fatalf("channels = %d", binary.LittleEndian.Uint16(data[22:24]))
	}
	if binary.LittleEndian.Uint32(data[24:28]) != 8000 {
		t.Fatalf("rate = %d", binary.LittleEndian.Uint32(data[24:28]))
	}
	if binary.LittleEndian.Uint16(data[34:36]) != 16 {
		t.Fatalf("bits = %d", binary.LittleEndian.Uint16(data[34:36]))
	}
	payload := data[44:]
	if len(payload) != 4*4 {
		t.Fatalf("payload = %d bytes, want 16 (4 stereo frames)", len(payload))
	}
	want := []int16{1, 10, 2, 20, 3, 30, 0, 40}
	for index, sample := range want {
		got := int16(binary.LittleEndian.Uint16(payload[index*2:]))
		if got != sample {
			t.Fatalf("sample %d = %d, want %d", index, got, sample)
		}
	}
	riffSize := binary.LittleEndian.Uint32(data[4:8])
	dataSize := binary.LittleEndian.Uint32(data[40:44])
	if dataSize != uint32(len(payload)) || riffSize != 36+dataSize {
		t.Fatalf("sizes riff=%d data=%d payload=%d", riffSize, dataSize, len(payload))
	}
}
