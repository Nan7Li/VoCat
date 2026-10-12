//go:build !windows

package main

import (
	"log/slog"

	"vocat/internal/loghub"
)

func runWindowsServiceIfNeeded(*slog.Logger, *loghub.Hub) bool { return false }
