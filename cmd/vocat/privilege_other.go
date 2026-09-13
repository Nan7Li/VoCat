//go:build !linux

package main

func isRootUser() bool { return false }
