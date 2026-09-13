//go:build windows

package modem

import "testing"

func TestIsWindowsModemUSBInstance(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "Quectel interface", id: `USB\VID_2C7C&PID_0125&MI_02\7&123&0&0002`, want: true},
		{name: "DJI interface", id: `USB\VID_2CA3&PID_4006&MI_04\7&123&0&0004`, want: true},
		{name: "composite parent", id: `USB\VID_2C7C&PID_0125\6&123&0&2`, want: true},
		{name: "other USB device", id: `USB\VID_046D&PID_C534\6&123&0&1`, want: false},
		{name: "non USB device", id: `PCI\VEN_8086&DEV_1234\0`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isWindowsModemUSBInstance(test.id); got != test.want {
				t.Fatalf("isWindowsModemUSBInstance(%q) = %v, want %v", test.id, got, test.want)
			}
		})
	}
}
