//go:build windows

package main

import "golang.org/x/sys/windows"

// isRootUser treats an elevated Windows Administrator token as the local
// recovery authority.  Credential reset must not be available to an ordinary
// desktop process, even when it can read the application database.
func isRootUser() bool {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}
