//go:build windows

package winifmib

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIPHelper      = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIfEntry2Ex = modIPHelper.NewProc("GetIfEntry2Ex")
)

func getIfEntry2Ex(level uint32, row *MibIfRow2) error {
	result, _, callErr := syscall.Syscall(
		procGetIfEntry2Ex.Addr(),
		2,
		uintptr(level),
		uintptr(unsafe.Pointer(row)),
		0,
	)
	if result != 0 {
		// NETIO_STATUS is the function's return value. syscall's secondary
		// error is not consistently populated for all IP Helper builds.
		if result <= uintptr(^uint32(0)) {
			return syscall.Errno(result)
		}
		return callErr
	}
	return nil
}

