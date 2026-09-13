//go:build windows

package ike

import (
	"context"
	"strings"
	"testing"
)

func TestWindowsChildSAInstallerFailsWithoutDataplane(t *testing.T) {
	handle, err := defaultChildSAInstaller().Install(context.Background(), ChildSAConfig{})
	if err == nil || handle != nil {
		t.Fatalf("Windows installer returned handle=%v err=%v; want explicit failure", handle, err)
	}
	message := strings.ToLower(err.Error())
	for _, required := range []string{"windows", "wfp", "wintun", "refusing"} {
		if !strings.Contains(message, required) {
			t.Fatalf("Windows installer error %q does not mention %q", err, required)
		}
	}
}
