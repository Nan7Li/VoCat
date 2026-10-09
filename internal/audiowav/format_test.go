package audiowav

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestStereoWAVHeaderAndSamplesAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call.wav")
	recorder, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	recorder.WriteDownlink([]int16{1000, -2000})
	recorder.WriteUplink([]int16{3000})
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
	if len(data) != 52 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" || string(data[36:40]) != "data" {
		t.Fatalf("invalid WAV structure: %x", data)
	}
	if binary.LittleEndian.Uint32(data[4:8]) != 44 || binary.LittleEndian.Uint32(data[40:44]) != 8 || binary.LittleEndian.Uint16(data[22:24]) != 2 {
		t.Fatalf("invalid WAV sizes/channels: %x", data[:44])
	}
	want := make([]byte, 8)
	for i, sample := range []int16{1000, 3000, -2000, 0} {
		binary.LittleEndian.PutUint16(want[i*2:], uint16(sample))
	}
	if !bytes.Equal(data[44:], want) {
		t.Fatalf("stereo PCM = %x, want %x", data[44:], want)
	}
}
