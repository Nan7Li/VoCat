package server

import "testing"

func TestKnownUSBGenerationRequiresMatchingCurrentGeneration(t *testing.T) {
	for _, current := range []string{"", "1:9"} {
		if shouldSignalRuntime("1:8", current) {
			t.Errorf("old runtime may signal a missing or replacement USB generation %q", current)
		}
	}
	if !shouldSignalRuntime("1:8", "1:8") {
		t.Fatal("the original USB generation cannot be cleaned up")
	}
}
