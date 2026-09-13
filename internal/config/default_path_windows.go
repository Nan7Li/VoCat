//go:build windows

package config

import (
	"os"
	"path/filepath"
)

func defaultDatabasePath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return filepath.Join(".", "data", "vocat.db")
	}
	return filepath.Join(base, "Halo", "data", "vocat.db")
}
