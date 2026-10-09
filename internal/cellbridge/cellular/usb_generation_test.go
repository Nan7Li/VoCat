package cellular

import (
	"context"
	"testing"
)

func TestUSBReplacementDoesNotSignalNewDeviceWithSameSIM(t *testing.T) {
	for _, next := range []string{"1:8", ""} {
		t.Run("generation_"+next, func(t *testing.T) {
			auth := cellularAuth("same-port", "8900000000000000001")
			auth.USBGeneration = "1:7"
			gate, at := &memGate{auth: auth}, &recAT{}
			controller := &Controller{DeviceID: "dji", Gate: gate, AT: at}
			call, err := controller.Dial(context.Background(), "12345", false)
			if err != nil {
				t.Fatal(err)
			}
			auth.USBGeneration = next
			gate.set(auth)
			_ = controller.Hangup(context.Background(), call.ID)
			if at.count("ATH") != 0 {
				t.Fatal("hangup was sent to the replacement USB device")
			}
			calls := controller.Calls()
			if len(calls) != 1 || calls[0].EndedAt == nil {
				t.Fatalf("old session was not released: %+v", calls)
			}
		})
	}
}
