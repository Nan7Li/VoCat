//go:build windows

package device

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"vocat/internal/modem"
)

const (
	windowsMBNProfileNamespace = "https://www.microsoft.com/networking/WWAN/profile/v4"
	windowsMBNConnectTimeout   = 45 * time.Second
)

func isWindowsMBNCandidate(candidate modem.Candidate) bool {
	return strings.EqualFold(strings.TrimSpace(candidate.HardwareKind), "windows-com") &&
		strings.TrimSpace(candidate.NetworkInterface) != ""
}

// readWindowsMBNICCID performs the small amount of AT work still needed by
// the Windows WWAN path. MBN owns packet data, while the modem's AT channel
// remains the authoritative SIM/eSIM identity source used by the rest of the
// device manager.
func (manager *Manager) readWindowsMBNICCID(ctx context.Context, state *managedDevice, candidate modem.Candidate) string {
	client, err := manager.clientLocked(ctx, state, candidate)
	if err != nil {
		return ""
	}
	for _, command := range []string{"AT+CCID", "AT+QCCID"} {
		response, commandErr := manager.command(ctx, client, command)
		if commandErr != nil {
			continue
		}
		if iccid := parseICCIDIdentifier(response, []string{"+CCID:", "+QCCID:"}, 18, 22); iccid != "" {
			return iccid
		}
	}
	return ""
}

type windowsMBNProfile struct {
	XMLName               xml.Name          `xml:"MBNProfileExt"`
	XMLNS                 string            `xml:"xmlns,attr"`
	Name                  string            `xml:"Name"`
	Description           string            `xml:"Description,omitempty"`
	IsDefault             bool              `xml:"IsDefault"`
	ProfileCreationType   string            `xml:"ProfileCreationType,omitempty"`
	SubscriberID          string            `xml:"SubscriberID,omitempty"`
	SimIccID              string            `xml:"SimIccID,omitempty"`
	HomeProviderName      string            `xml:"HomeProviderName,omitempty"`
	AutoConnectOnInternet bool              `xml:"AutoConnectOnInternet,omitempty"`
	ConnectionMode        string            `xml:"ConnectionMode"`
	Context               windowsMBNContext `xml:"Context"`
}

type windowsMBNContext struct {
	AccessString  string                   `xml:"AccessString,omitempty"`
	UserLogonCred *windowsMBNUserLogonCred `xml:"UserLogonCred,omitempty"`
	Compression   string                   `xml:"Compression,omitempty"`
	AuthProtocol  string                   `xml:"AuthProtocol,omitempty"`
	IPType        string                   `xml:"IPType,omitempty"`
}

type windowsMBNUserLogonCred struct {
	UserName string `xml:"UserName"`
	Password string `xml:"Password,omitempty"`
}

func setWindowsCellularNetwork(
	ctx context.Context,
	candidate modem.Candidate,
	enabled bool,
	apn string,
	ipVersion string,
	username string,
	password string,
	authentication string,
	simICCID string,
) (NetworkResult, error) {
	interfaceName := strings.TrimSpace(candidate.NetworkInterface)
	result := NetworkResult{
		Enabled:   enabled,
		Backend:   "mbn",
		Interface: interfaceName,
		APN:       apn,
		IPVersion: ipVersion,
	}
	if !isWindowsMBNCandidate(candidate) {
		return NetworkResult{}, fmt.Errorf("%w: Windows MBN requires a cellular interface", ErrDataBackendUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !enabled {
		output, err := runWindowsMBN(ctx, "disconnect", "interface="+windowsMBNArgument(interfaceName))
		if err != nil && !windowsMBNDisconnectAlreadyInactive(output) {
			return NetworkResult{}, err
		}
		result.Detail = "Windows Mobile Broadband connection disconnected"
		return result, nil
	}
	if authentication != "NONE" && strings.TrimSpace(username) == "" {
		return NetworkResult{}, fmt.Errorf("Windows WWAN %s authentication requires a username", authentication)
	}
	profile, err := windowsMBNProfileXML(apn, ipVersion, username, password, authentication, simICCID)
	if err != nil {
		return NetworkResult{}, err
	}
	temporary, err := os.CreateTemp("", "vocat-mbn-*.xml")
	if err != nil {
		return NetworkResult{}, fmt.Errorf("create temporary Windows WWAN profile: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return NetworkResult{}, fmt.Errorf("secure temporary Windows WWAN profile: %w", err)
	}
	if _, err := temporary.Write(profile); err != nil {
		_ = temporary.Close()
		return NetworkResult{}, fmt.Errorf("write temporary Windows WWAN profile: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return NetworkResult{}, fmt.Errorf("close temporary Windows WWAN profile: %w", err)
	}
	if _, err := runWindowsMBN(
		ctx,
		"connect",
		"interface="+windowsMBNArgument(interfaceName),
		"connmode=tmp",
		"name="+windowsMBNArgument(temporaryPath),
	); err != nil {
		return NetworkResult{}, err
	}
	if err := waitForWindowsMBNAddress(ctx, interfaceName); err != nil {
		return NetworkResult{}, err
	}
	result.Detail = "Windows Mobile Broadband connection activated"
	return result, nil
}

func windowsMBNProfileXML(
	apn string,
	ipVersion string,
	username string,
	password string,
	authentication string,
	simICCID string,
) ([]byte, error) {
	contextProfile := windowsMBNContext{
		AccessString: strings.TrimSpace(apn),
		Compression:  "DISABLE",
		AuthProtocol: windowsMBNAuthProtocol(authentication),
		IPType:       windowsMBNIPType(ipVersion),
	}
	if username != "" {
		contextProfile.UserLogonCred = &windowsMBNUserLogonCred{UserName: username, Password: password}
	}
	profile := windowsMBNProfile{
		XMLName:               xml.Name{Local: "MBNProfileExt"},
		XMLNS:                 windowsMBNProfileNamespace,
		Name:                  "vocat-temporary-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		IsDefault:             false,
		ProfileCreationType:   "UserProvisioned",
		SimIccID:              strings.TrimSpace(simICCID),
		AutoConnectOnInternet: false,
		ConnectionMode:        "manual",
		Context:               contextProfile,
	}
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	encoder := xml.NewEncoder(&buffer)
	encoder.Indent("", "  ")
	if err := encoder.Encode(profile); err != nil {
		return nil, fmt.Errorf("encode Windows WWAN profile: %w", err)
	}
	if err := encoder.Flush(); err != nil {
		return nil, fmt.Errorf("flush Windows WWAN profile: %w", err)
	}
	return buffer.Bytes(), nil
}

func windowsMBNAuthProtocol(authentication string) string {
	if authentication == "PAP_OR_CHAP" {
		return "AutoSelection"
	}
	return authentication
}

func windowsMBNIPType(ipVersion string) string {
	switch ipVersion {
	case "IP":
		return "IPv4"
	case "IPV6":
		return "IPv6"
	case "IPV4V6":
		return "IPv4v6"
	default:
		return "Default"
	}
}

func runWindowsMBN(ctx context.Context, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandArgs := append([]string{"mbn"}, args...)
	command := exec.CommandContext(ctx, "netsh.exe", commandArgs...)
	output, err := command.CombinedOutput()
	if err == nil {
		return output, nil
	}
	detail := strings.TrimSpace(string(output))
	if len(detail) > 512 {
		detail = detail[:512] + "..."
	}
	if detail != "" {
		return output, fmt.Errorf("netsh mbn %s: %w (%s)", strings.Join(args, " "), err, detail)
	}
	return output, fmt.Errorf("netsh mbn %s: %w", strings.Join(args, " "), err)
}

// netsh reports a non-zero exit status when disconnect is requested without an
// active packet context. That is already the desired fail-closed state, so the
// disable operation treats only these explicit inactive-context messages as an
// idempotent success and continues surfacing all other failures.
func windowsMBNDisconnectAlreadyInactive(output []byte) bool {
	text := strings.ToLower(strings.TrimSpace(string(output)))
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"context not activated", "no active connection", "not connected",
		"上下文未激活", "未连接",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func windowsMBNArgument(value string) string {
	// exec.Command passes each string as one argv element and performs the
	// Windows command-line quoting itself. Adding shell quotes here would make
	// the quote characters part of the value received by netsh, which breaks
	// interface names and temporary profile paths containing spaces.
	return strings.TrimSpace(value)
}

func waitForWindowsMBNAddress(ctx context.Context, interfaceName string) error {
	waitContext, cancel := context.WithTimeout(ctx, windowsMBNConnectTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		connected, _ := windowsMBNHasUsableAddress(interfaceName)
		if connected {
			return nil
		}
		select {
		case <-waitContext.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Windows WWAN interface %q did not obtain an IP address", interfaceName)
		case <-ticker.C:
		}
	}
}

func windowsNetworkStatus(ctx context.Context, candidate modem.Candidate) (NetworkStatus, error) {
	status := NetworkStatus{Backend: "mbn", Interface: strings.TrimSpace(candidate.NetworkInterface)}
	if !isWindowsMBNCandidate(candidate) {
		status.Detail = "Windows WWAN interface is not available"
		return status, ErrDataBackendUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	connected, err := windowsMBNHasUsableAddress(candidate.NetworkInterface)
	if err != nil {
		status.Detail = err.Error()
		return status, nil
	}
	if connected {
		status.Connected = true
		status.Detail = "Windows Mobile Broadband interface has a usable IP address"
		return status, nil
	}
	status.Detail = "Windows Mobile Broadband interface is present but has no usable IP address"
	return status, nil
}

func windowsMBNHasUsableAddress(interfaceName string) (bool, error) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return false, fmt.Errorf("Windows WWAN interface name is empty")
	}
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return false, nil
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return false, err
	}
	for _, address := range addresses {
		value := strings.TrimSpace(address.String())
		if host, _, splitErr := net.ParseCIDR(value); splitErr == nil {
			value = host.String()
		}
		ip := net.ParseIP(value)
		if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return true, nil
	}
	return false, nil
}
