//go:build windows

// Package winifmib contains the small Windows IP Helper mapping needed by
// Vocat. MIB_IF_ROW2 is the Windows equivalent of Linux's
// /sys/class/net/*/statistics counters.
package winifmib

import (
	"net"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	ifMaxStringSize        = 256
	ifMaxPhysAddressLength = 32
	mibIfEntryNormal       = 0
)

// MibIfRow2 mirrors the Windows SDK's MIB_IF_ROW2 layout. The explicit
// eight-byte status field is intentional: the C compiler pads that field more
// aggressively than Go does when the status flags are represented as a byte.
type MibIfRow2 struct {
	InterfaceLuid               uint64
	InterfaceIndex              uint32
	InterfaceGuid               windows.GUID
	Alias                       [ifMaxStringSize + 1]uint16
	Description                 [ifMaxStringSize + 1]uint16
	PhysicalAddressLength       uint32
	PhysicalAddress             [ifMaxPhysAddressLength]byte
	PermanentPhysicalAddress    [ifMaxPhysAddressLength]byte
	Mtu                         uint32
	Type                        uint32
	TunnelType                  uint32
	MediaType                   uint32
	PhysicalMediumType          uint32
	AccessType                  uint32
	DirectionType               uint32
	InterfaceAndOperStatusFlags [8]byte
	OperStatus                  uint32
	AdminStatus                 uint32
	MediaConnectState           uint32
	NetworkGuid                 windows.GUID
	ConnectionType              uint32
	TransmitLinkSpeed           uint64
	ReceiveLinkSpeed            uint64
	InOctets                    uint64
	InUcastPkts                 uint64
	InNUcastPkts                uint64
	InDiscards                  uint64
	InErrors                    uint64
	InUnknownProtos             uint64
	InUcastOctets               uint64
	InMulticastOctets           uint64
	InBroadcastOctets           uint64
	OutOctets                   uint64
	OutUcastPkts                uint64
	OutNUcastPkts               uint64
	OutDiscards                 uint64
	OutErrors                   uint64
	OutUcastOctets              uint64
	OutMulticastOctets          uint64
	OutBroadcastOctets          uint64
	OutQLen                     uint64
}

// GetIfEntry2Ex returns the current cumulative byte counters for a Windows
// interface index. The API is available on supported desktop Windows hosts;
// callers should treat a missing adapter as a transient sampling failure.
func GetIfEntry2Ex(index int) (MibIfRow2, error) {
	if index <= 0 {
		return MibIfRow2{}, windows.ERROR_INVALID_PARAMETER
	}
	row := MibIfRow2{InterfaceIndex: uint32(index)}
	if err := getIfEntry2Ex(mibIfEntryNormal, &row); err != nil {
		return MibIfRow2{}, err
	}
	return row, nil
}

// InterfaceCounters resolves a Windows interface by its display name and
// returns cumulative receive/transmit octets.
func InterfaceCounters(name string) (uint64, uint64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, 0, windows.ERROR_INVALID_PARAMETER
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, 0, err
	}
	row, err := GetIfEntry2Ex(iface.Index)
	if err != nil {
		return 0, 0, err
	}
	return row.InOctets, row.OutOctets, nil
}

