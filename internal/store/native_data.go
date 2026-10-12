package store

import (
	"encoding/json"
	"runtime"
	"strings"
)

func NativeCellularICCID(config Device) string {
	var extra struct {
		ICCID string `json:"native_cellular_iccid"`
	}
	_ = json.Unmarshal(config.Extra, &extra)
	return strings.TrimSpace(extra.ICCID)
}

func SetNativeCellularICCID(config *Device, iccid string) error {
	extra := make(map[string]json.RawMessage)
	if len(config.Extra) > 0 {
		if err := json.Unmarshal(config.Extra, &extra); err != nil {
			return err
		}
	}
	if extra == nil {
		extra = make(map[string]json.RawMessage)
	}
	value, err := json.Marshal(strings.TrimSpace(iccid))
	if err != nil {
		return err
	}
	extra["native_cellular_iccid"] = value
	config.Extra, err = json.Marshal(extra)
	return err
}

// NativeCellularData identifies Windows WWAN configurations whose data path is
// owned by Mobile Broadband rather than the experimental Linux export proxy.
func NativeCellularData(config Device) bool {
	return runtime.GOOS == "windows" && strings.EqualFold(strings.TrimSpace(config.DeviceBackend), "mbn") && strings.TrimSpace(config.Interface) != "" && NormalizeDeviceType(config.DeviceType) != DeviceTypeUSBSIMReader
}
