package main

import (
	"os"

	"vocat/internal/desktop"
)

// vocat-desktop is the double-clickable Windows desktop executable.  The
// Windows CI builds it with the windowsgui subsystem, while the shared
// internal package keeps `go test ./...` and non-Windows builds intact.
func main() {
	if err := desktop.Run(); err != nil {
		desktop.ShowError(err)
		os.Exit(1)
	}
}
