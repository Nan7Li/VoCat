//go:build windows && (amd64 || arm64)

package ims

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file maps the documented WFP user-mode ABI directly instead of
// shelling out to netsh or importing Linux XFRM semantics. Filters live in a
// dynamic engine session, while manual SA contexts use a separate ordinary
// engine session because IPsecSaContextCreate1 rejects dynamic sessions.

const (
	wfpEmpty           uint32 = 0
	wfpUint8           uint32 = 1
	wfpUint16          uint32 = 2
	wfpUint32          uint32 = 3
	wfpByteArray16Type uint32 = 11
	wfpMatchEqual      uint32 = 0

	fwpmSessionFlagDynamic      uint32 = 0x1
	fwpActionCalloutTerminating uint32 = 3
	rpcCAuthnWinNT              uint32 = 10

	fwpIPVersionV4 uint32 = 0
	fwpIPVersionV6 uint32 = 1

	ipsecTrafficTypeTransport      uint32 = 0
	ipsecTransformESPAuth          uint32 = 2
	ipsecTransformESPAuthAndCipher uint32 = 4
	ipsecAuthMD5                   uint32 = 0
	ipsecAuthSHA1                  uint32 = 1
	ipsecAuthConfigHMACMD596       uint8  = 0
	ipsecAuthConfigHMACSHA196      uint8  = 1
	ipsecCipherType3DES            uint32 = 2
	ipsecCipherTypeAES128          uint32 = 3
	ipsecCipherConfigCBC3DES       uint8  = 2
	ipsecCipherConfigCBCAES128     uint8  = 3
	windowsIPSecLifetimeSeconds    uint32 = 8 * 60 * 60
	windowsIPSecLifetimeKilobytes  uint32 = 0x7fffffff
	windowsIPSecLifetimePackets    uint32 = 0x7fffffff
)

var (
	fwpmLayerInboundTransportV4  = windows.GUID{Data1: 0x5926dfc8, Data2: 0xe3cf, Data3: 0x4426, Data4: [8]byte{0xa2, 0x83, 0xdc, 0x39, 0x3f, 0x5d, 0x0f, 0x9d}}
	fwpmLayerOutboundTransportV4 = windows.GUID{Data1: 0x09e61aea, Data2: 0xd214, Data3: 0x46e2, Data4: [8]byte{0x9b, 0x21, 0xb2, 0x6b, 0x0b, 0x2f, 0x28, 0xc8}}
	fwpmLayerInboundTransportV6  = windows.GUID{Data1: 0x634a869f, Data2: 0xfc23, Data3: 0x4b90, Data4: [8]byte{0xb0, 0xc1, 0xbf, 0x62, 0x0a, 0x36, 0xae, 0x6f}}
	fwpmLayerOutboundTransportV6 = windows.GUID{Data1: 0xe1735bde, Data2: 0x013f, Data3: 0x4655, Data4: [8]byte{0xb3, 0x51, 0xa4, 0x9e, 0x15, 0x76, 0x2d, 0xf0}}

	fwpmCalloutIPSecInboundTransportV4  = windows.GUID{Data1: 0x5132900d, Data2: 0x5e84, Data3: 0x4b5f, Data4: [8]byte{0x80, 0xe4, 0x01, 0x74, 0x1e, 0x81, 0xff, 0x10}}
	fwpmCalloutIPSecOutboundTransportV4 = windows.GUID{Data1: 0x4b46bf0a, Data2: 0x4523, Data3: 0x4e57, Data4: [8]byte{0xaa, 0x38, 0xa8, 0x79, 0x87, 0xc9, 0x10, 0xd9}}
	fwpmCalloutIPSecInboundTransportV6  = windows.GUID{Data1: 0x49d3ac92, Data2: 0x2a6c, Data3: 0x4dcf, Data4: [8]byte{0x95, 0x5f, 0x1c, 0x3b, 0xe0, 0x09, 0xdd, 0x99}}
	fwpmCalloutIPSecOutboundTransportV6 = windows.GUID{Data1: 0x38d87722, Data2: 0xad83, Data3: 0x4f11, Data4: [8]byte{0xa9, 0x1f, 0xdf, 0x0f, 0xb0, 0x77, 0x22, 0x5b}}

	fwpmConditionIPLocalAddress  = windows.GUID{Data1: 0xd9ee00de, Data2: 0xc1ef, Data3: 0x4617, Data4: [8]byte{0xbf, 0xe3, 0xff, 0xd8, 0xf5, 0xa0, 0x89, 0x57}}
	fwpmConditionIPRemoteAddress = windows.GUID{Data1: 0xb235ae9a, Data2: 0x1d64, Data3: 0x49b8, Data4: [8]byte{0xa4, 0x4c, 0x5f, 0xf3, 0xd9, 0x09, 0x50, 0x45}}
	fwpmConditionIPProtocol      = windows.GUID{Data1: 0x3971ef2b, Data2: 0x623e, Data3: 0x4f9a, Data4: [8]byte{0x8c, 0xb1, 0x6e, 0x79, 0xb8, 0x06, 0xb9, 0xa7}}
	fwpmConditionIPLocalPort     = windows.GUID{Data1: 0x0c1ba1af, Data2: 0x5765, Data3: 0x453f, Data4: [8]byte{0xaf, 0x22, 0xa8, 0xf7, 0x91, 0xac, 0x77, 0x5b}}
	fwpmConditionIPRemotePort    = windows.GUID{Data1: 0xc35a604d, Data2: 0xd22b, Data3: 0x4e1a, Data4: [8]byte{0x91, 0xb4, 0x68, 0xf6, 0x74, 0xee, 0x67, 0x4b}}
)

var (
	modFwpuclnt                    = windows.NewLazySystemDLL("fwpuclnt.dll")
	procFwpmEngineOpen0            = modFwpuclnt.NewProc("FwpmEngineOpen0")
	procFwpmEngineClose0           = modFwpuclnt.NewProc("FwpmEngineClose0")
	procFwpmFilterAdd0             = modFwpuclnt.NewProc("FwpmFilterAdd0")
	procFwpmFilterDeleteByID0      = modFwpuclnt.NewProc("FwpmFilterDeleteById0")
	procIPSecSaContextCreate1      = modFwpuclnt.NewProc("IPsecSaContextCreate1")
	procIPSecSaContextSetSPI0      = modFwpuclnt.NewProc("IPsecSaContextSetSpi0")
	procIPSecSaContextAddInbound0  = modFwpuclnt.NewProc("IPsecSaContextAddInbound0")
	procIPSecSaContextAddOutbound0 = modFwpuclnt.NewProc("IPsecSaContextAddOutbound0")
	procIPSecSaContextDeleteByID0  = modFwpuclnt.NewProc("IPsecSaContextDeleteById0")
)

type wfpByteBlob struct {
	size uint32
	_    uint32
	data *byte
}

type wfpValue0 struct {
	type_ uint32
	_     uint32
	value uintptr
}

type wfpDisplayData0 struct {
	name        *uint16
	description *uint16
}

type wfpAction0 struct {
	type_      uint32
	calloutKey windows.GUID
}

type wfpFilterCondition0 struct {
	fieldKey       windows.GUID
	matchType      uint32
	_              uint32
	conditionValue wfpValue0
}

type wfpFilter0 struct {
	filterKey           windows.GUID
	displayData         wfpDisplayData0
	flags               uint32
	_                   uint32
	providerKey         *windows.GUID
	providerData        wfpByteBlob
	layerKey            windows.GUID
	subLayerKey         windows.GUID
	weight              wfpValue0
	numFilterConditions uint32
	_                   uint32
	filterCondition     *wfpFilterCondition0
	action              wfpAction0
	_                   [4]byte
	providerContextKey  windows.GUID
	reserved            *windows.GUID
	filterID            uint64
	effectiveWeight     wfpValue0
}

type wfpSession0 struct {
	sessionKey           windows.GUID
	displayData          wfpDisplayData0
	flags                uint32
	txnWaitTimeoutInMSec uint32
	processID            uint32
	_                    uint32
	sid                  *windows.SID
	username             *uint16
	kernelMode           uint8
	_                    [7]byte
}

type ipsecTraffic1 struct {
	ipVersion       uint32
	localAddress    [16]byte
	remoteAddress   [16]byte
	trafficType     uint32
	filterOrPolicy  uint64
	remotePort      uint16
	localPort       uint16
	ipProtocol      uint8
	_               [3]byte
	localIfLUID     uint64
	realIfProfileID uint32
	_               uint32
}

type ipsecGetSPI1 struct {
	inboundTraffic ipsecTraffic1
	ipVersion      uint32
	_              uint32
	udpEncap       uintptr
	rngModuleID    uintptr
}

type ipsecSALifetime0 struct {
	seconds   uint32
	kilobytes uint32
	packets   uint32
}

type ipsecAuthTransformID0 struct {
	authType   uint32
	authConfig uint8
	_          [3]byte
}

type ipsecAuthTransform0 struct {
	id             ipsecAuthTransformID0
	cryptoModuleID uintptr
}

type ipsecCipherTransformID0 struct {
	cipherType   uint32
	cipherConfig uint8
	_            [3]byte
}

type ipsecCipherTransform0 struct {
	id             ipsecCipherTransformID0
	cryptoModuleID uintptr
}

type ipsecSAAuthInformation0 struct {
	transform ipsecAuthTransform0
	key       wfpByteBlob
}

type ipsecSACipherInformation0 struct {
	transform ipsecCipherTransform0
	key       wfpByteBlob
}

type ipsecSAAuthAndCipherInformation0 struct {
	cipher ipsecSACipherInformation0
	auth   ipsecSAAuthInformation0
}

type ipsecSA0 struct {
	spi           uint32
	transformType uint32
	information   uintptr
}

type ipsecSABundle0 struct {
	flags                uint32
	lifetime             ipsecSALifetime0
	idleTimeoutSeconds   uint32
	ndAllowClearSeconds  uint32
	ipsecID              uintptr
	napContext           uint32
	qmSAID               uint32
	numSAs               uint32
	saList               *ipsecSA0
	keyModuleState       uintptr
	ipVersion            uint32
	peerV4PrivateAddress uint32
	mmSAID               uint64
	pfsGroup             uint32
	_                    uint32
}

type windowsIPSecInstaller struct{}

type windowsIPSecHandle struct {
	mu           sync.Mutex
	filterEngine uintptr
	saEngine     uintptr
	contextIDs   []uint64
	filterIDs    []uint64
	closed       bool
}

type windowsIPSecPair struct {
	name         string
	localPort    int
	remotePort   int
	inboundSPI   uint32
	outboundSPI  uint32
	inProtocols  []uint8
	outProtocols []uint8
}

func defaultIPSecInstaller() IPSecSAInstaller { return windowsIPSecInstaller{} }

func (windowsIPSecInstaller) Install(ctx context.Context, config IPSecSAConfig) (IPSecSAHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateIPSecSAConfig(config); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keyConfig := cloneIPSecSAConfig(config)
	defer zeroBytes(keyConfig.EncryptionKey)
	defer zeroBytes(keyConfig.IntegrityKey)

	filterEngine, err := openWFPEngine(true)
	if err != nil {
		return nil, fmt.Errorf("ims: open Windows WFP dynamic filter session: %w", err)
	}
	saEngine, err := openWFPEngine(false)
	if err != nil {
		_ = closeWFPEngine(filterEngine)
		return nil, fmt.Errorf("ims: open Windows WFP SA session: %w", err)
	}
	handle := &windowsIPSecHandle{filterEngine: filterEngine, saEngine: saEngine}
	pairs := []windowsIPSecPair{
		{
			name: "UE client and P-CSCF server", localPort: config.UEClientPort, remotePort: config.PCSCFServerPort,
			inboundSPI: config.UEClientSPI, outboundSPI: config.PCSCFServerSPI,
			inProtocols: []uint8{6}, outProtocols: []uint8{6, 17},
		},
		{
			name: "UE server and P-CSCF client", localPort: config.UEServerPort, remotePort: config.PCSCFClientPort,
			inboundSPI: config.UEServerSPI, outboundSPI: config.PCSCFClientSPI,
			inProtocols: []uint8{6, 17}, outProtocols: []uint8{6},
		},
	}
	for _, pair := range pairs {
		if err := handle.installPair(ctx, keyConfig, pair); err != nil {
			cleanupErr := handle.Close(context.Background())
			if cleanupErr != nil {
				return nil, errors.Join(err, fmt.Errorf("ims: roll back partial Windows WFP install: %w", cleanupErr))
			}
			return nil, err
		}
	}
	return handle, nil
}

func openWFPEngine(dynamic bool) (uintptr, error) {
	for _, procedure := range []*windows.LazyProc{
		procFwpmEngineOpen0, procFwpmEngineClose0, procFwpmFilterAdd0, procFwpmFilterDeleteByID0,
		procIPSecSaContextCreate1, procIPSecSaContextSetSPI0, procIPSecSaContextAddInbound0,
		procIPSecSaContextAddOutbound0, procIPSecSaContextDeleteByID0,
	} {
		if err := procedure.Find(); err != nil {
			return 0, fmt.Errorf("required Fwpuclnt.dll procedure %s is unavailable: %w", procedure.Name, err)
		}
	}
	var session wfpSession0
	if dynamic {
		session.flags = fwpmSessionFlagDynamic
	}
	var engine uintptr
	result, _, _ := procFwpmEngineOpen0.Call(
		0,
		uintptr(rpcCAuthnWinNT),
		0,
		uintptr(unsafe.Pointer(&session)),
		uintptr(unsafe.Pointer(&engine)),
	)
	if result != 0 {
		return 0, wfpError("FwpmEngineOpen0", result)
	}
	return engine, nil
}

func closeWFPEngine(engine uintptr) error {
	if engine == 0 {
		return nil
	}
	result, _, _ := procFwpmEngineClose0.Call(engine)
	if result != 0 {
		return wfpError("FwpmEngineClose0", result)
	}
	return nil
}

func (handle *windowsIPSecHandle) installPair(ctx context.Context, config IPSecSAConfig, pair windowsIPSecPair) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inProtocol, err := wfpFilterProtocol(pair.inProtocols)
	if err != nil {
		return fmt.Errorf("ims: %s inbound transport selector: %w", pair.name, err)
	}
	outProtocol, err := wfpFilterProtocol(pair.outProtocols)
	if err != nil {
		return fmt.Errorf("ims: %s outbound transport selector: %w", pair.name, err)
	}
	// A Windows transport SA context accepts exactly one inbound and one
	// outbound filter. A zero protocol is intentional for the common TCP+UDP
	// IMS selector: exact local/remote ports still constrain the filter to
	// transports that expose those ports, while one filter remains attachable
	// to the SA context.
	inboundFilter, err := addWFPIPSecFilter(handle.filterEngine, config.LocalIP, config.RemoteIP, pair.localPort, pair.remotePort, inProtocol, true)
	if err != nil {
		return fmt.Errorf("ims: add %s inbound WFP filter: %w", pair.name, err)
	}
	handle.filterIDs = append(handle.filterIDs, inboundFilter)
	outboundFilter, err := addWFPIPSecFilter(handle.filterEngine, config.LocalIP, config.RemoteIP, pair.localPort, pair.remotePort, outProtocol, false)
	if err != nil {
		return fmt.Errorf("ims: add %s outbound WFP filter: %w", pair.name, err)
	}
	handle.filterIDs = append(handle.filterIDs, outboundFilter)

	traffic, version, err := makeIPSecTraffic(config.LocalIP, config.RemoteIP, outboundFilter)
	if err != nil {
		return err
	}
	var contextID uint64
	result, _, _ := procIPSecSaContextCreate1.Call(
		handle.saEngine,
		uintptr(unsafe.Pointer(&traffic)),
		0,
		0,
		uintptr(unsafe.Pointer(&contextID)),
	)
	if result != 0 {
		return wfpError("IPsecSaContextCreate1", result)
	}
	handle.contextIDs = append(handle.contextIDs, contextID)

	inboundTraffic := traffic
	inboundTraffic.filterOrPolicy = inboundFilter
	getSPI := ipsecGetSPI1{inboundTraffic: inboundTraffic, ipVersion: version}
	result, _, _ = procIPSecSaContextSetSPI0.Call(
		handle.saEngine,
		uintptr(contextID),
		uintptr(unsafe.Pointer(&getSPI)),
		uintptr(pair.inboundSPI),
	)
	if result != 0 {
		return wfpError("IPsecSaContextSetSpi0", result)
	}
	if err := addWFPESPAssociation(handle.saEngine, contextID, pair.inboundSPI, version, config, true); err != nil {
		return fmt.Errorf("ims: add %s inbound ESP SA: %w", pair.name, err)
	}
	if err := addWFPESPAssociation(handle.saEngine, contextID, pair.outboundSPI, version, config, false); err != nil {
		return fmt.Errorf("ims: add %s outbound ESP SA: %w", pair.name, err)
	}
	return nil
}

func addWFPIPSecFilter(
	engine uintptr,
	localIP net.IP,
	remoteIP net.IP,
	localPort int,
	remotePort int,
	protocol uint8,
	inbound bool,
) (uint64, error) {
	version, localValue, local6, err := wfpAddressValue(localIP)
	if err != nil {
		return 0, err
	}
	remoteVersion, remoteValue, remote6, err := wfpAddressValue(remoteIP)
	if err != nil {
		return 0, err
	}
	if version != remoteVersion {
		return 0, errors.New("WFP filter endpoints use different IP families")
	}
	conditions := make([]wfpFilterCondition0, 0, 5)
	conditions = append(conditions,
		wfpFilterCondition0{fieldKey: fwpmConditionIPLocalAddress, matchType: wfpMatchEqual, conditionValue: localValue},
		wfpFilterCondition0{fieldKey: fwpmConditionIPRemoteAddress, matchType: wfpMatchEqual, conditionValue: remoteValue},
	)
	if protocol != 0 {
		conditions = append(conditions, wfpFilterCondition0{
			fieldKey: fwpmConditionIPProtocol, matchType: wfpMatchEqual,
			conditionValue: wfpValue0{type_: wfpUint8, value: uintptr(protocol)},
		})
	}
	conditions = append(conditions,
		wfpFilterCondition0{fieldKey: fwpmConditionIPLocalPort, matchType: wfpMatchEqual, conditionValue: wfpValue0{type_: wfpUint16, value: uintptr(uint16(localPort))}},
		wfpFilterCondition0{fieldKey: fwpmConditionIPRemotePort, matchType: wfpMatchEqual, conditionValue: wfpValue0{type_: wfpUint16, value: uintptr(uint16(remotePort))}},
	)
	name, _ := windows.UTF16PtrFromString("Halo IMS ipsec-3gpp transport policy")
	description, _ := windows.UTF16PtrFromString("Dynamic IP/transport/port scoped ESP policy")
	filter := wfpFilter0{
		displayData:         wfpDisplayData0{name: name, description: description},
		numFilterConditions: uint32(len(conditions)),
		filterCondition:     &conditions[0],
		action:              wfpAction0{type_: fwpActionCalloutTerminating},
		weight:              wfpValue0{type_: wfpEmpty},
	}
	if version == fwpIPVersionV4 {
		if inbound {
			filter.layerKey = fwpmLayerInboundTransportV4
			filter.action.calloutKey = fwpmCalloutIPSecInboundTransportV4
		} else {
			filter.layerKey = fwpmLayerOutboundTransportV4
			filter.action.calloutKey = fwpmCalloutIPSecOutboundTransportV4
		}
	} else if inbound {
		filter.layerKey = fwpmLayerInboundTransportV6
		filter.action.calloutKey = fwpmCalloutIPSecInboundTransportV6
	} else {
		filter.layerKey = fwpmLayerOutboundTransportV6
		filter.action.calloutKey = fwpmCalloutIPSecOutboundTransportV6
	}
	var filterID uint64
	result, _, _ := procFwpmFilterAdd0.Call(
		engine,
		uintptr(unsafe.Pointer(&filter)),
		0,
		uintptr(unsafe.Pointer(&filterID)),
	)
	runtime.KeepAlive(local6)
	runtime.KeepAlive(remote6)
	runtime.KeepAlive(conditions)
	if result != 0 {
		return 0, wfpError("FwpmFilterAdd0", result)
	}
	return filterID, nil
}

func wfpFilterProtocol(protocols []uint8) (uint8, error) {
	seen := make(map[uint8]struct{}, len(protocols))
	for _, protocol := range protocols {
		if protocol == 0 {
			return 0, errors.New("protocol selector contains wildcard zero")
		}
		seen[protocol] = struct{}{}
	}
	switch len(seen) {
	case 0:
		return 0, errors.New("protocol selector is empty")
	case 1:
		for protocol := range seen {
			return protocol, nil
		}
	case 2:
		if _, tcp := seen[6]; tcp {
			if _, udp := seen[17]; udp {
				// One filter with exact ports safely covers the TCP+UDP IMS
				// selector while respecting the one-filter SA-context limit.
				return 0, nil
			}
		}
	}
	return 0, errors.New("Windows transport-mode SA supports one protocol or the TCP+UDP combination only")
}

func wfpAddressValue(ip net.IP) (uint32, wfpValue0, *[16]byte, error) {
	if ip4 := ip.To4(); ip4 != nil {
		return fwpIPVersionV4, wfpValue0{type_: wfpUint32, value: uintptr(binary.LittleEndian.Uint32(ip4))}, nil, nil
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return 0, wfpValue0{}, nil, errors.New("invalid WFP IP address")
	}
	address := new([16]byte)
	copy(address[:], ip16)
	return fwpIPVersionV6, wfpValue0{type_: wfpByteArray16Type, value: uintptr(unsafe.Pointer(address))}, address, nil
}

func makeIPSecTraffic(localIP, remoteIP net.IP, filterID uint64) (ipsecTraffic1, uint32, error) {
	var traffic ipsecTraffic1
	traffic.trafficType = ipsecTrafficTypeTransport
	traffic.filterOrPolicy = filterID
	if local4 := localIP.To4(); local4 != nil {
		remote4 := remoteIP.To4()
		if remote4 == nil {
			return traffic, 0, errors.New("WFP SA endpoints use different IP families")
		}
		traffic.ipVersion = fwpIPVersionV4
		copy(traffic.localAddress[:4], local4)
		copy(traffic.remoteAddress[:4], remote4)
		return traffic, fwpIPVersionV4, nil
	}
	local16, remote16 := localIP.To16(), remoteIP.To16()
	if local16 == nil || remote16 == nil || remoteIP.To4() != nil {
		return traffic, 0, errors.New("invalid WFP SA endpoints")
	}
	traffic.ipVersion = fwpIPVersionV6
	copy(traffic.localAddress[:], local16)
	copy(traffic.remoteAddress[:], remote16)
	return traffic, fwpIPVersionV6, nil
}

func addWFPESPAssociation(
	engine uintptr,
	contextID uint64,
	spi uint32,
	version uint32,
	config IPSecSAConfig,
	inbound bool,
) error {
	authTransform := ipsecAuthTransform0{}
	switch ipsecIntegrityAlgorithm(config) {
	case "hmac-md5-96":
		authTransform.id = ipsecAuthTransformID0{authType: ipsecAuthMD5, authConfig: ipsecAuthConfigHMACMD596}
	case "hmac-sha-1-96":
		authTransform.id = ipsecAuthTransformID0{authType: ipsecAuthSHA1, authConfig: ipsecAuthConfigHMACSHA196}
	default:
		return errors.New("unsupported WFP ESP integrity transform")
	}
	authInfo := ipsecSAAuthInformation0{
		transform: authTransform,
		key:       byteBlob(config.IntegrityKey),
	}
	sa := ipsecSA0{spi: spi}
	var cipherInfo ipsecSACipherInformation0
	var combined ipsecSAAuthAndCipherInformation0
	switch ipsecEncryptionAlgorithm(config) {
	case "null":
		sa.transformType = ipsecTransformESPAuth
		sa.information = uintptr(unsafe.Pointer(&authInfo))
	case "aes-cbc":
		cipherInfo = ipsecSACipherInformation0{
			transform: ipsecCipherTransform0{id: ipsecCipherTransformID0{cipherType: ipsecCipherTypeAES128, cipherConfig: ipsecCipherConfigCBCAES128}},
			key:       byteBlob(config.EncryptionKey),
		}
		combined = ipsecSAAuthAndCipherInformation0{cipher: cipherInfo, auth: authInfo}
		sa.transformType = ipsecTransformESPAuthAndCipher
		sa.information = uintptr(unsafe.Pointer(&combined))
	case "des-ede3-cbc":
		cipherInfo = ipsecSACipherInformation0{
			transform: ipsecCipherTransform0{id: ipsecCipherTransformID0{cipherType: ipsecCipherType3DES, cipherConfig: ipsecCipherConfigCBC3DES}},
			key:       byteBlob(config.EncryptionKey),
		}
		combined = ipsecSAAuthAndCipherInformation0{cipher: cipherInfo, auth: authInfo}
		sa.transformType = ipsecTransformESPAuthAndCipher
		sa.information = uintptr(unsafe.Pointer(&combined))
	default:
		return errors.New("unsupported WFP ESP cipher transform")
	}
	bundle := ipsecSABundle0{
		lifetime: ipsecSALifetime0{
			seconds: windowsIPSecLifetimeSeconds, kilobytes: windowsIPSecLifetimeKilobytes, packets: windowsIPSecLifetimePackets,
		},
		numSAs:    1,
		saList:    &sa,
		ipVersion: version,
	}
	procedure := procIPSecSaContextAddOutbound0
	operation := "IPsecSaContextAddOutbound0"
	if inbound {
		procedure = procIPSecSaContextAddInbound0
		operation = "IPsecSaContextAddInbound0"
	}
	result, _, _ := procedure.Call(engine, uintptr(contextID), uintptr(unsafe.Pointer(&bundle)))
	runtime.KeepAlive(config.IntegrityKey)
	runtime.KeepAlive(config.EncryptionKey)
	runtime.KeepAlive(authInfo)
	runtime.KeepAlive(cipherInfo)
	runtime.KeepAlive(combined)
	runtime.KeepAlive(sa)
	runtime.KeepAlive(bundle)
	if result != 0 {
		return wfpError(operation, result)
	}
	return nil
}

func byteBlob(value []byte) wfpByteBlob {
	if len(value) == 0 {
		return wfpByteBlob{}
	}
	return wfpByteBlob{size: uint32(len(value)), data: &value[0]}
}

func (handle *windowsIPSecHandle) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return nil
	}
	handle.closed = true
	var cleanupErrors []error
	if handle.saEngine != 0 {
		for index := len(handle.contextIDs) - 1; index >= 0; index-- {
			result, _, _ := procIPSecSaContextDeleteByID0.Call(handle.saEngine, uintptr(handle.contextIDs[index]))
			if result != 0 {
				cleanupErrors = append(cleanupErrors, wfpError("IPsecSaContextDeleteById0", result))
			}
		}
	}
	if handle.filterEngine != 0 {
		for index := len(handle.filterIDs) - 1; index >= 0; index-- {
			result, _, _ := procFwpmFilterDeleteByID0.Call(handle.filterEngine, uintptr(handle.filterIDs[index]))
			if result != 0 {
				cleanupErrors = append(cleanupErrors, wfpError("FwpmFilterDeleteById0", result))
			}
		}
	}
	if err := closeWFPEngine(handle.saEngine); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := closeWFPEngine(handle.filterEngine); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	handle.saEngine = 0
	handle.filterEngine = 0
	if err := ctx.Err(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func wfpError(operation string, result uintptr) error {
	return fmt.Errorf("%s failed with Windows status 0x%08x: %w", operation, uint32(result), windows.Errno(result))
}
