//go:build !windows

package update

import (
	"context"
	"fmt"
	"log/slog"
)

func installVerifiedBinary(
	_ context.Context,
	logger *slog.Logger,
	target string,
	replacement string,
	latest string,
	restart bool,
) error {
	if err := backupAndReplace(target, replacement); err != nil {
		return err
	}
	logger.Info("installed new binary", "target", target, "version", latest)
	fmt.Printf("vocat updated to %s.\n", latest)
	if !restart {
		return nil
	}
	if err := RestartService(logger); err != nil {
		fmt.Printf("Binary replaced, but automatic restart failed: %v\n", err)
		fmt.Println("Restart the vocat service manually to apply the new build.")
	}
	return nil
}

func restartWindowsService(*slog.Logger) error {
	return fmt.Errorf("Windows service management is unavailable on this platform")
}

func RunHelper([]string) error {
	return fmt.Errorf("update: update-helper is available only on Windows")
}
