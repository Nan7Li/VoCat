//go:build !windows

package config

func defaultDatabasePath() string { return "./data/vocat.db" }
