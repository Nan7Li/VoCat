//go:build windows && (amd64 || arm64)

package ike

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

const (
	windowsUserspaceTunnelMTU  = 1380
	windowsWintunNamePrefix    = "VoCat ePDG"
	windowsWintunInterfaceWait = 3 * time.Second
	windowsWintunPollInterval  = 100 * time.Millisecond
)

// windowsUserspaceInstaller is the Windows ePDG data plane. WFP's manual SA
// API can install transport SAs, but it does not create the layer-3 interface
// and selector-scoped routes required by an IKE CHILD_SA. This implementation
// therefore uses the signed Wintun layer-3 adapter and reuses the existing
// ESP/NAT-T implementation and session relay. Non-NAT-T sessions remain
// rejected until a native WFP tunnel implementation can provide equivalent
// routing and packet forwarding semantics.
type windowsUserspaceInstaller struct {
	netshCommand string
}

type windowsUserspaceHandle struct {
	command        string
	config         ChildSAConfig
	interfaceIndex uint32
	adapter        *wintun.Adapter
	packetSession  wintun.Session
	tunnel         *espTunnel
	relay          NATTPacketRelay

	runContext context.Context
	cancel     context.CancelFunc
	wait       sync.WaitGroup
	cancelOnce sync.Once
	endOnce    sync.Once

	mu          sync.Mutex
	closed      bool
	terminalErr error
	failures    chan error
	cleanup     []windowsNetworkCleanup
}

type windowsNetworkCleanup struct {
	operation      string
	family         string
	prefix         string
	interfaceIndex uint32
	nextHop        net.IP
	address        string
	deleteAddress  bool
}

type windowsRouteInfo struct {
	family         string
	prefixLength   int
	interfaceIndex uint32
	nextHop        net.IP
	metric         uint32
}

func (*windowsUserspaceHandle) DataplaneMode() string { return "userspace" }

func (installer windowsUserspaceInstaller) Install(
	ctx context.Context,
	config ChildSAConfig,
) (ChildSAHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateWindowsUserspaceConfig(config); err != nil {
		return nil, err
	}
	command := strings.TrimSpace(installer.netshCommand)
	if command == "" {
		command = "netsh"
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, fmt.Errorf("ike: Windows netsh is required to configure the Wintun CHILD_SA: %w", err)
	}

	// newESPTunnel copies the negotiated keys into its direction state. Keep
	// the caller's slices untouched and clear only the temporary copies here;
	// Close zeroes mutable authentication keys and drops the cipher block
	// references retained by the tunnel.
	tunnelConfig := cloneWindowsChildSAConfig(config)
	tunnelConfig.InboundEncKey = append([]byte(nil), config.InboundEncKey...)
	tunnelConfig.InboundAuthKey = append([]byte(nil), config.InboundAuthKey...)
	tunnelConfig.OutboundEncKey = append([]byte(nil), config.OutboundEncKey...)
	tunnelConfig.OutboundAuthKey = append([]byte(nil), config.OutboundAuthKey...)
	tunnel, err := newESPTunnel(tunnelConfig, nil)
	zeroESPBytes(tunnelConfig.InboundEncKey)
	zeroESPBytes(tunnelConfig.InboundAuthKey)
	zeroESPBytes(tunnelConfig.OutboundEncKey)
	zeroESPBytes(tunnelConfig.OutboundAuthKey)
	tunnelConfig.InboundEncKey = nil
	tunnelConfig.InboundAuthKey = nil
	tunnelConfig.OutboundEncKey = nil
	tunnelConfig.OutboundAuthKey = nil
	if err != nil {
		return nil, fmt.Errorf("ike: initialize Windows ESP data plane: %w", err)
	}

	adapterName := windowsWintunAdapterName(config)
	adapter, err := wintun.CreateAdapter(adapterName, "Wintun", nil)
	if err != nil {
		tunnel.zero()
		return nil, fmt.Errorf("ike: create Windows Wintun adapter %q (install the signed wintun.dll beside vocat.exe): %w", adapterName, err)
	}
	packetSession, err := adapter.StartSession(wintun.RingCapacityMin)
	if err != nil {
		_ = adapter.Close()
		tunnel.zero()
		return nil, fmt.Errorf("ike: start Wintun packet session: %w", err)
	}
	interfaceIndex, err := windowsWintunInterfaceIndex(ctx, adapter.LUID())
	if err != nil {
		packetSession.End()
		_ = adapter.Close()
		tunnel.zero()
		return nil, err
	}

	runContext, cancel := context.WithCancel(context.Background())
	handle := &windowsUserspaceHandle{
		command:        command,
		config:         cloneWindowsChildSAConfig(config),
		interfaceIndex: interfaceIndex,
		adapter:        adapter,
		packetSession:  packetSession,
		tunnel:         tunnel,
		relay:          config.Relay,
		runContext:     runContext,
		cancel:         cancel,
		failures:       make(chan error, 1),
	}
	if err := handle.configure(ctx); err != nil {
		cancel()
		handle.endPacketSession()
		cleanupErr := handle.cleanupNetwork(context.Background())
		adapterErr := adapter.Close()
		tunnel.zero()
		return nil, errors.Join(err, cleanupErr, adapterErr)
	}
	handle.wait.Add(2)
	go handle.copyWintunToRelay()
	go handle.copyRelayToWintun()
	return handle, nil
}

func validateWindowsUserspaceConfig(config ChildSAConfig) error {
	if config.OuterLocal == nil || config.OuterRemote == nil ||
		config.OuterLocal.IsUnspecified() || config.OuterRemote.IsUnspecified() ||
		config.OuterLocal.IsMulticast() || config.OuterRemote.IsMulticast() {
		return errors.New("ike: Windows ePDG CHILD_SA requires valid outer IP endpoints")
	}
	if (config.OuterLocal.To4() != nil) != (config.OuterRemote.To4() != nil) {
		return errors.New("ike: Windows ePDG outer endpoints use different IP families")
	}
	if config.Relay == nil {
		return errors.New("ike: Windows ePDG CHILD_SA requires a UDP/4500 NAT-T relay")
	}
	if !config.UDPEncapsulation {
		return errors.New("ike: Windows ePDG CHILD_SA requires negotiated UDP encapsulation; native WFP tunnel mode is not used until it can provide selector routing")
	}
	if err := validateUserspaceRoutes(config); err != nil {
		return err
	}
	if _, err := windowsSelectorCIDRs(config.ResponderSelectors); err != nil {
		return fmt.Errorf("ike: Windows ePDG selector routes are invalid: %w", err)
	}
	return nil
}

func cloneWindowsChildSAConfig(config ChildSAConfig) ChildSAConfig {
	config.OuterLocal = append(net.IP(nil), config.OuterLocal...)
	config.OuterRemote = append(net.IP(nil), config.OuterRemote...)
	config.InnerLocalIPv4 = append(net.IP(nil), config.InnerLocalIPv4...)
	config.InnerLocalIPv6 = append(net.IP(nil), config.InnerLocalIPv6...)
	config.PCSCF = cloneIPs(config.PCSCF)
	config.DNS = cloneIPs(config.DNS)
	config.InitiatorSelectors = copyESPTrafficSelectors(config.InitiatorSelectors)
	config.ResponderSelectors = copyESPTrafficSelectors(config.ResponderSelectors)
	config.InboundEncKey = nil
	config.InboundAuthKey = nil
	config.OutboundEncKey = nil
	config.OutboundAuthKey = nil
	return config
}

func windowsWintunAdapterName(config ChildSAConfig) string {
	return fmt.Sprintf("%s %08x-%08x", windowsWintunNamePrefix, config.InboundSPI, config.OutboundSPI)
}

func (handle *windowsUserspaceHandle) configure(ctx context.Context) error {
	// Capture the route used by IKE/UDP 4500 before installing any selector
	// route. If a negotiated selector is 0/0, the /32 or /128 anchor keeps the
	// encrypted outer session on the original WWAN/Wi-Fi interface.
	outerRoute, err := windowsBestRoute(handle.config.OuterRemote, handle.interfaceIndex)
	if err != nil {
		return fmt.Errorf("ike: find existing route to ePDG outer address: %w", err)
	}
	if handle.config.InnerLocalIPv4 != nil {
		if err := handle.run(ctx, "assign Wintun IPv4 address",
			"interface", "ipv4", "add", "address",
			fmt.Sprintf("name=%d", handle.interfaceIndex),
			"address="+handle.config.InnerLocalIPv4.String(),
			"mask=255.255.255.255", "store=active",
		); err != nil {
			return err
		}
		handle.recordAddressCleanup("remove Wintun IPv4 address", "ipv4", handle.config.InnerLocalIPv4.String())
		if err := handle.run(ctx, "set Wintun IPv4 MTU",
			"interface", "ipv4", "set", "subinterface",
			fmt.Sprintf("%d", handle.interfaceIndex),
			fmt.Sprintf("mtu=%d", windowsUserspaceTunnelMTU), "store=active",
		); err != nil {
			return err
		}
	}
	if handle.config.InnerLocalIPv6 != nil {
		prefix := handle.config.InnerIPv6Prefix
		if prefix == 0 || prefix > 128 {
			prefix = 128
		}
		if err := handle.run(ctx, "assign Wintun IPv6 address",
			"interface", "ipv6", "add", "address",
			fmt.Sprintf("interface=%d", handle.interfaceIndex),
			fmt.Sprintf("address=%s/%d", handle.config.InnerLocalIPv6.String(), prefix),
			"store=active",
		); err != nil {
			return err
		}
		handle.recordAddressCleanup("remove Wintun IPv6 address", "ipv6", handle.config.InnerLocalIPv6.String())
		if err := handle.run(ctx, "set Wintun IPv6 MTU",
			"interface", "ipv6", "set", "subinterface",
			fmt.Sprintf("interface=%d", handle.interfaceIndex),
			fmt.Sprintf("mtu=%d", windowsUserspaceTunnelMTU), "store=active",
		); err != nil {
			return err
		}
	}

	if outerRoute.prefixLength < windowsIPBits(handle.config.OuterRemote) {
		prefix := fmt.Sprintf("%s/%d", windowsNormalizedIP(handle.config.OuterRemote), windowsIPBits(handle.config.OuterRemote))
		if err := handle.addRoute(ctx, "preserve ePDG outer route", outerRoute.family, prefix, outerRoute.interfaceIndex, outerRoute.nextHop); err != nil {
			return err
		}
	}

	routes, err := windowsSelectorCIDRs(handle.config.ResponderSelectors)
	if err != nil {
		return fmt.Errorf("ike: build Windows ePDG selector routes: %w", err)
	}
	for _, route := range routes {
		family := "ipv4"
		if strings.Contains(route, ":") {
			family = "ipv6"
		}
		if err := handle.addRoute(ctx, "install negotiated ePDG selector route", family, route, handle.interfaceIndex, nil); err != nil {
			return err
		}
	}
	return nil
}

func (handle *windowsUserspaceHandle) run(ctx context.Context, operation string, arguments ...string) error {
	command := exec.CommandContext(ctx, handle.command, arguments...)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("ike: %s: %s", operation, message)
}

func (handle *windowsUserspaceHandle) addRoute(
	ctx context.Context,
	operation string,
	family string,
	prefix string,
	interfaceIndex uint32,
	nextHop net.IP,
) error {
	arguments := []string{
		"interface", family, "add", "route",
		"prefix=" + prefix,
		fmt.Sprintf("interface=%d", interfaceIndex),
		"metric=1", "store=active",
	}
	if nextHop != nil && !nextHop.IsUnspecified() {
		arguments = append(arguments, "nexthop="+windowsNormalizedIP(nextHop).String())
	}
	if err := handle.run(ctx, operation, arguments...); err != nil {
		return err
	}
	handle.cleanup = append(handle.cleanup, windowsNetworkCleanup{
		operation:      "remove " + operation,
		family:         family,
		prefix:         prefix,
		interfaceIndex: interfaceIndex,
		nextHop:        windowsNormalizedIP(nextHop),
	})
	return nil
}

func (handle *windowsUserspaceHandle) recordAddressCleanup(operation, family, address string) {
	handle.cleanup = append(handle.cleanup, windowsNetworkCleanup{
		operation:      operation,
		family:         family,
		address:        address,
		interfaceIndex: handle.interfaceIndex,
		deleteAddress:  true,
	})
}

func (handle *windowsUserspaceHandle) cleanupNetwork(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	cleanupContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var errs []error
	for index := len(handle.cleanup) - 1; index >= 0; index-- {
		item := handle.cleanup[index]
		var arguments []string
		if item.deleteAddress {
			if item.family == "ipv4" {
				arguments = []string{"interface", "ipv4", "delete", "address", fmt.Sprintf("name=%d", item.interfaceIndex), "address=" + item.address, "store=active"}
			} else {
				arguments = []string{"interface", "ipv6", "delete", "address", fmt.Sprintf("interface=%d", item.interfaceIndex), "address=" + item.address, "store=active"}
			}
		} else {
			arguments = []string{"interface", item.family, "delete", "route", "prefix=" + item.prefix, fmt.Sprintf("interface=%d", item.interfaceIndex), "store=active"}
			if item.nextHop != nil && !item.nextHop.IsUnspecified() {
				arguments = append(arguments, "nexthop="+item.nextHop.String())
			}
		}
		if err := handle.run(cleanupContext, item.operation, arguments...); err != nil {
			errs = append(errs, err)
		}
	}
	handle.cleanup = nil
	return errors.Join(errs...)
}

func (handle *windowsUserspaceHandle) copyWintunToRelay() {
	defer handle.wait.Done()
	for {
		packet, err := handle.receivePacket()
		if err != nil {
			if handle.runContext.Err() == nil {
				handle.fail(fmt.Errorf("ike: read Wintun packet: %w", err))
			}
			return
		}
		if len(packet) == 0 {
			handle.fail(errors.New("ike: Wintun returned an empty packet"))
			return
		}
		innerPacket := append([]byte(nil), packet...)
		handle.packetSession.ReleaseReceivePacket(packet)
		protected, err := handle.tunnel.seal(innerPacket)
		if err != nil {
			if errors.Is(err, errESPPolicyDrop) {
				continue
			}
			handle.fail(err)
			return
		}
		if err := handle.relay.SendESP(handle.runContext, protected); err != nil {
			if handle.runContext.Err() == nil {
				handle.fail(fmt.Errorf("ike: relay outbound ESP: %w", err))
			}
			return
		}
	}
}

func (handle *windowsUserspaceHandle) receivePacket() ([]byte, error) {
	for {
		if err := handle.runContext.Err(); err != nil {
			return nil, err
		}
		packet, err := handle.packetSession.ReceivePacket()
		if err == nil {
			return packet, nil
		}
		if !errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			return nil, err
		}
		event := handle.packetSession.ReadWaitEvent()
		if event == 0 {
			return nil, errors.New("Wintun returned an invalid receive event")
		}
		if _, waitErr := windows.WaitForSingleObject(event, uint32(windowsWintunPollInterval/time.Millisecond)); waitErr != nil {
			return nil, waitErr
		}
	}
}

func (handle *windowsUserspaceHandle) copyRelayToWintun() {
	defer handle.wait.Done()
	buffer := make([]byte, wintun.PacketSizeMax)
	for {
		count, err := handle.relay.ReceiveESP(handle.runContext, buffer)
		if err != nil {
			if handle.runContext.Err() == nil {
				handle.fail(fmt.Errorf("ike: relay inbound ESP: %w", err))
			}
			return
		}
		if count <= 0 || count > len(buffer) {
			handle.fail(fmt.Errorf("ike: relay returned invalid ESP length %d", count))
			return
		}
		cleartext, err := handle.tunnel.open(buffer[:count])
		if err != nil {
			// Network input is unauthenticated until the ESP ICV and selectors
			// have been checked. Drop malformed/replayed packets without tearing
			// down an otherwise healthy CHILD_SA.
			continue
		}
		if len(cleartext) > wintun.PacketSizeMax {
			handle.fail(errors.New("ike: decrypted ESP packet exceeds Wintun maximum packet size"))
			return
		}
		packet, err := handle.packetSession.AllocateSendPacket(len(cleartext))
		if err != nil {
			if handle.runContext.Err() == nil {
				handle.fail(fmt.Errorf("ike: allocate Wintun packet: %w", err))
			}
			return
		}
		copy(packet, cleartext)
		handle.packetSession.SendPacket(packet)
	}
}

func (handle *windowsUserspaceHandle) fail(err error) {
	if err == nil {
		return
	}
	handle.mu.Lock()
	notify := false
	if handle.terminalErr == nil {
		handle.terminalErr = err
		notify = true
	}
	handle.mu.Unlock()
	if notify {
		select {
		case handle.failures <- err:
		default:
		}
	}
	handle.cancelRun()
	handle.wakePacketSession()
}

func (handle *windowsUserspaceHandle) Failures() <-chan error { return handle.failures }

func (handle *windowsUserspaceHandle) cancelRun() {
	handle.cancelOnce.Do(func() { handle.cancel() })
}

func (handle *windowsUserspaceHandle) endPacketSession() {
	handle.endOnce.Do(func() { handle.packetSession.End() })
}

func (handle *windowsUserspaceHandle) wakePacketSession() {
	event := handle.packetSession.ReadWaitEvent()
	if event != 0 {
		_ = windows.SetEvent(event)
	}
}

func (handle *windowsUserspaceHandle) Close(ctx context.Context) error {
	handle.mu.Lock()
	if handle.closed {
		handle.mu.Unlock()
		return nil
	}
	handle.closed = true
	handle.mu.Unlock()

	handle.cancelRun()
	handle.wakePacketSession()
	handle.wait.Wait()
	handle.endPacketSession()
	cleanupErr := handle.cleanupNetwork(ctx)
	adapterErr := error(nil)
	if handle.adapter != nil {
		adapterErr = handle.adapter.Close()
	}
	if handle.tunnel != nil {
		handle.tunnel.zero()
	}
	return errors.Join(cleanupErr, adapterErr)
}

func windowsWintunInterfaceIndex(ctx context.Context, luid uint64) (uint32, error) {
	if luid == 0 {
		return 0, errors.New("ike: Wintun returned an empty interface LUID")
	}
	deadline := time.NewTimer(windowsWintunInterfaceWait)
	defer deadline.Stop()
	ticker := time.NewTicker(windowsWintunPollInterval)
	defer ticker.Stop()
	for {
		index, err := lookupWindowsInterfaceIndex(luid)
		if err == nil {
			return index, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("ike: locate Wintun interface: %w", ctx.Err())
		case <-deadline.C:
			return 0, fmt.Errorf("ike: locate Wintun interface with LUID %d: %w", luid, err)
		case <-ticker.C:
		}
	}
}

func lookupWindowsInterfaceIndex(luid uint64) (uint32, error) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormal, &table); err != nil {
		return 0, fmt.Errorf("GetIfTable2Ex failed: %w", err)
	}
	if table == nil || table.NumEntries == 0 {
		if table != nil {
			windows.FreeMibTable(unsafe.Pointer(table))
		}
		return 0, errors.New("Wintun interface table is empty")
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	for _, row := range unsafe.Slice(&table.Table[0], table.NumEntries) {
		if row.InterfaceLuid == luid {
			if row.InterfaceIndex == 0 {
				return 0, errors.New("Wintun interface has no numeric index")
			}
			return row.InterfaceIndex, nil
		}
	}
	return 0, errors.New("Wintun interface is not visible in the IP helper table yet")
}

func windowsBestRoute(target net.IP, excludedInterface uint32) (windowsRouteInfo, error) {
	familyValue, family, bits := windowsIPFamily(target)
	if familyValue == 0 {
		return windowsRouteInfo{}, errors.New("invalid route target address")
	}
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(familyValue, &table); err != nil {
		return windowsRouteInfo{}, fmt.Errorf("GetIpForwardTable2(%s) failed: %w", family, err)
	}
	if table == nil || table.NumEntries == 0 {
		if table != nil {
			windows.FreeMibTable(unsafe.Pointer(table))
		}
		return windowsRouteInfo{}, errors.New("IP helper route table is empty")
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	target = windowsNormalizedIP(target)
	var best windowsRouteInfo
	found := false
	for _, row := range table.Rows() {
		prefixIP, prefixLength, ok := windowsRoutePrefix(row.DestinationPrefix)
		if !ok || int(prefixLength) > bits || row.InterfaceIndex == 0 ||
			row.InterfaceIndex == excludedInterface {
			continue
		}
		mask := net.CIDRMask(int(prefixLength), bits)
		network := &net.IPNet{IP: prefixIP.Mask(mask), Mask: mask}
		if !network.Contains(target) {
			continue
		}
		candidate := windowsRouteInfo{
			family:         family,
			prefixLength:   int(prefixLength),
			interfaceIndex: row.InterfaceIndex,
			nextHop:        windowsRawSockaddrIP(row.NextHop),
			metric:         row.Metric,
		}
		if !found || candidate.prefixLength > best.prefixLength ||
			(candidate.prefixLength == best.prefixLength && candidate.metric < best.metric) {
			best = candidate
			found = true
		}
	}
	if !found {
		return windowsRouteInfo{}, errors.New("no usable route exists")
	}
	return best, nil
}

func windowsRoutePrefix(prefix windows.IpAddressPrefix) (net.IP, uint8, bool) {
	ip := windowsRawSockaddrIP(prefix.Prefix)
	if ip == nil {
		return nil, 0, false
	}
	bits := windowsIPBits(ip)
	if bits == 0 || int(prefix.PrefixLength) > bits {
		return nil, 0, false
	}
	return windowsNormalizedIP(ip), prefix.PrefixLength, true
}

func windowsRawSockaddrIP(value windows.RawSockaddrInet) net.IP {
	switch value.Family {
	case windows.AF_INET:
		address := (*windows.RawSockaddrInet4)(unsafe.Pointer(&value))
		return net.IPv4(address.Addr[0], address.Addr[1], address.Addr[2], address.Addr[3]).To4()
	case windows.AF_INET6:
		address := (*windows.RawSockaddrInet6)(unsafe.Pointer(&value))
		return append(net.IP(nil), address.Addr[:]...)
	default:
		return nil
	}
}

func windowsIPFamily(ip net.IP) (uint16, string, int) {
	if ip.To4() != nil {
		return windows.AF_INET, "ipv4", 32
	}
	if ip.To16() != nil {
		return windows.AF_INET6, "ipv6", 128
	}
	return 0, "", 0
}

func windowsIPBits(ip net.IP) int {
	_, _, bits := windowsIPFamily(ip)
	return bits
}

func windowsNormalizedIP(ip net.IP) net.IP {
	if ip4 := ip.To4(); ip4 != nil {
		return append(net.IP(nil), ip4...)
	}
	return append(net.IP(nil), ip.To16()...)
}

// windowsSelectorCIDRs converts the inclusive IKE traffic-selector ranges to
// the smallest set of Windows address-prefix routes. Ports and protocols stay
// enforced by espTunnel; Windows routes are intentionally only the address
// part of the selector and never broaden the user-space ESP policy.
func windowsSelectorCIDRs(selectors []trafficSelector) ([]string, error) {
	seen := make(map[string]struct{})
	var routes []string
	for _, selector := range selectors {
		cidrs, err := windowsIPRangeCIDRs(selector.StartIP, selector.EndIP)
		if err != nil {
			return nil, err
		}
		for _, cidr := range cidrs {
			value := cidr.String()
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			routes = append(routes, value)
		}
	}
	sort.Strings(routes)
	return routes, nil
}

func windowsIPRangeCIDRs(start, end net.IP) ([]*net.IPNet, error) {
	start = windowsNormalizedIP(start)
	end = windowsNormalizedIP(end)
	bits := windowsIPBits(start)
	if bits == 0 || windowsIPBits(end) != bits {
		return nil, errors.New("traffic-selector IP range uses an invalid or mixed address family")
	}
	startValue := new(big.Int).SetBytes(start)
	endValue := new(big.Int).SetBytes(end)
	if startValue.Cmp(endValue) > 0 {
		return nil, errors.New("traffic-selector IP range start is greater than end")
	}
	result := make([]*net.IPNet, 0, bits)
	for startValue.Cmp(endValue) <= 0 {
		remaining := new(big.Int).Sub(endValue, startValue)
		remaining.Add(remaining, big.NewInt(1))
		alignmentBits := windowsTrailingZeroBits(startValue, bits)
		blockBits := remaining.BitLen() - 1
		if alignmentBits > blockBits {
			alignmentBits = blockBits
		}
		prefixLength := bits - alignmentBits
		addressBytes := make([]byte, bits/8)
		startValue.FillBytes(addressBytes)
		result = append(result, &net.IPNet{IP: net.IP(addressBytes), Mask: net.CIDRMask(prefixLength, bits)})
		startValue.Add(startValue, new(big.Int).Lsh(big.NewInt(1), uint(alignmentBits)))
	}
	return result, nil
}

func windowsTrailingZeroBits(value *big.Int, bits int) int {
	if value.Sign() == 0 {
		return bits
	}
	count := 0
	for count < bits && value.Bit(count) == 0 {
		count++
	}
	return count
}

var _ ChildSAInstaller = windowsChildSAInstaller{}
var _ ChildSAHandle = (*windowsUserspaceHandle)(nil)
var _ DataplaneEvidence = (*windowsUserspaceHandle)(nil)
var _ DataplaneFailureNotifier = (*windowsUserspaceHandle)(nil)
