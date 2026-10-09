package qdc507

import (
	"context"
	"strings"
	"testing"
)

func TestWrongCardCannotLoadOrAdoptRoute(t *testing.T) {
	previous := append([]runtimeArtifact(nil), trustedRuntimeArtifacts...)
	restoreHashes(t, previous)
	dir := writeRuntime(t, nil)
	for _, cards := range []string{"0 [unrelated]: wrong sound card", "0 [other]: other\n1 [" + expectedCardName + "]"} {
		t.Run(cards, func(t *testing.T) {
			runner := &scriptRunner{reply: func(argv []string) (string, error) {
				args := strings.Join(argv, " ")
				switch {
				case strings.Contains(args, "devices -l"):
					return "serial device usb:1-4.2 transport_id:7", nil
				case strings.Contains(args, "id -u"):
					return "0", nil
				case strings.Contains(args, "uname -r"):
					return expectedKernel, nil
				case strings.Contains(args, "asound/cards"):
					return cards, nil
				default:
					return "ready", nil
				}
			}}
			status, err := Prepare(context.Background(), Config{OptIn: true, Bootstrap: true, DeviceType: "dji_4g", USBPath: "1-4.2", RuntimeDir: dir, ADBPath: "adb", Runner: runner})
			if err == nil || status.Ready || !strings.Contains(status.Reason, "card0") {
				t.Fatalf("status=%+v err=%v", status, err)
			}
			for _, argv := range runner.calls {
				args := strings.Join(argv, " ")
				if strings.Contains(args, "push") || strings.Contains(args, "insmod") || strings.Contains(args, "halo-cellbridge-route.pid") {
					t.Fatalf("unverified target touched: %s", args)
				}
			}
		})
	}
}
