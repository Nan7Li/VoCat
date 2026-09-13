//go:build windows

package server

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"vocat/internal/winifmib"
)

var (
	modKernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes          = modKernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx    = modKernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpaceExW     = modKernel32.NewProc("GetDiskFreeSpaceExW")
)

type windowsFileTime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

type windowsMemoryStatusEx struct {
	Length                  uint32
	MemoryLoad              uint32
	TotalPhys               uint64
	AvailPhys               uint64
	TotalPageFile           uint64
	AvailPageFile           uint64
	TotalVirtual             uint64
	AvailVirtual             uint64
	AvailExtendedVirtual     uint64
}

func probeHostStatic() hostStaticInfo {
	cpu := strings.TrimSpace(os.Getenv("PROCESSOR_IDENTIFIER"))
	if cpu == "" {
		cpu = runtime.GOARCH
	}
	board := strings.TrimSpace(os.Getenv("COMPUTERNAME"))
	if board == "" {
		board, _ = os.Hostname()
	}
	_, _, memoryTotal := readHostMemory()
	_, _, diskTotal := readHostDisk()
	return hostStaticInfo{
		CPUModel:    cpu,
		BoardModel:  board,
		MemoryModel: formatWindowsHostBytes(memoryTotal),
		DiskModel:   formatWindowsHostBytes(diskTotal),
	}
}

func readHostCPUTimes() (hostCPUTimes, bool) {
	var idle, kernel, user windowsFileTime
	result, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return hostCPUTimes{}, false
	}
	toUint64 := func(value windowsFileTime) uint64 {
		return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
	}
	return hostCPUTimes{
		idle:  toUint64(idle),
		total: toUint64(kernel) + toUint64(user),
	}, true
}

func readHostMemory() (percent float64, used, total uint64) {
	status := windowsMemoryStatusEx{Length: uint32(unsafe.Sizeof(windowsMemoryStatusEx{}))}
	result, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 || status.TotalPhys == 0 {
		return 0, 0, 0
	}
	available := status.AvailPhys
	if available > status.TotalPhys {
		available = status.TotalPhys
	}
	used = status.TotalPhys - available
	return clampPercent(float64(used) * 100 / float64(status.TotalPhys)), used, status.TotalPhys
}

func readHostDisk() (percent float64, used, total uint64) {
	root := windowsHostVolumeRoot()
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0, 0
	}
	var available, totalBytes, freeBytes uint64
	result, _, _ := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(&available)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&freeBytes)),
	)
	if result == 0 || totalBytes == 0 {
		return 0, 0, 0
	}
	used = totalBytes - available
	return clampPercent(float64(used) * 100 / float64(totalBytes)), used, totalBytes
}

func readHostNetTotals() (rx, tx uint64, ok bool) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return 0, 0, false
	}
	for _, iface := range interfaces {
		if !hostNetInterfaceCounted(iface.Name) {
			continue
		}
		inOctets, outOctets, err := winifmib.InterfaceCounters(iface.Name)
		if err != nil {
			continue
		}
		rx += inOctets
		tx += outOctets
		ok = true
	}
	return rx, tx, ok
}

func windowsHostVolumeRoot() string {
	workingDirectory, err := os.Getwd()
	if err == nil {
		if volume := filepath.VolumeName(workingDirectory); volume != "" {
			return volume + `\`
		}
	}
	return `C:\`
}

func formatWindowsHostBytes(value uint64) string {
	if value == 0 {
		return ""
	}
	return formatLiveBytes(float64(value))
}
