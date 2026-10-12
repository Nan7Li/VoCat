//go:build windows

package modem

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// WindowsUSBDeviceProblem describes a known modem vendor's USB interface that
// Windows enumerated but could not start. It is intentionally diagnostic only:
// Vocat never installs or selects a driver on the user's behalf.
type WindowsUSBDeviceProblem struct {
	InstanceID   string `json:"instanceId"`
	FriendlyName string `json:"friendlyName,omitempty"`
	ProblemCode  uint32 `json:"problemCode"`
}

// WindowsUSBModemProblems reports present Quectel or DJI USB modem interfaces
// with a non-zero ConfigMgr problem code. A composite USB parent can be
// healthy while each functional interface is unbound; reporting the interface
// problem makes that distinction visible in `vocat doctor`.
func WindowsUSBModemProblems(ctx context.Context) ([]WindowsUSBDeviceProblem, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devices, err := windows.SetupDiGetClassDevsEx(nil, "", 0,
		windows.DIGCF_PRESENT|windows.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return nil, fmt.Errorf("enumerate Windows USB modem devices: %w", err)
	}
	defer devices.Close()

	result := make([]WindowsUSBDeviceProblem, 0)
	for index := 0; ; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, enumErr := devices.EnumDeviceInfo(index)
		if enumErr != nil {
			if errors.Is(enumErr, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			return nil, fmt.Errorf("enumerate Windows USB modem device %d: %w", index, enumErr)
		}
		instanceID, idErr := devices.DeviceInstanceID(info)
		if idErr != nil || !isWindowsModemUSBInstance(instanceID) {
			continue
		}
		var status, problemCode uint32
		if statusErr := windows.CM_Get_DevNode_Status(&status, &problemCode, info.DevInst, 0); statusErr != nil {
			continue
		}
		if problemCode == 0 {
			continue
		}
		friendlyName := ""
		if value, propertyErr := devices.DeviceRegistryProperty(info, windows.SPDRP_FRIENDLYNAME); propertyErr == nil {
			friendlyName, _ = value.(string)
		}
		if friendlyName == "" {
			if value, propertyErr := devices.DeviceRegistryProperty(info, windows.SPDRP_DEVICEDESC); propertyErr == nil {
				friendlyName, _ = value.(string)
			}
		}
		result = append(result, WindowsUSBDeviceProblem{
			InstanceID:   instanceID,
			FriendlyName: strings.TrimSpace(friendlyName),
			ProblemCode:  problemCode,
		})
	}
	return result, nil
}

func isWindowsModemUSBInstance(instanceID string) bool {
	instanceID = strings.ToUpper(strings.TrimSpace(instanceID))
	if !strings.HasPrefix(instanceID, "USB\\") {
		return false
	}
	for _, vendorID := range []string{"VID_2C7C", "VID_2CA3"} {
		if strings.Contains(instanceID, vendorID) {
			return true
		}
	}
	return false
}
