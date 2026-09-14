# Windows VoWiFi validation checklist

The Windows port has two separate data-plane paths. The ePDG IKE/CHILD_SA path
uses the signed Wintun layer-3 adapter, the existing user-space ESP/NAT-T
implementation, and the negotiated UDP/4500 relay. The IMS `ipsec-3gpp` path
uses a dynamic `Fwpuclnt.dll` filter session plus a separate ordinary WFP
session for manual SA contexts (Windows rejects `IPsecSaContextCreate1` from
a dynamic session). It installs transport-mode ESP SAs and IP/protocol/port
filters. Windows transport SA contexts accept one filter per direction, so the
common TCP+UDP IMS selector is represented by one exact IP+port filter while a
single-protocol selector keeps its protocol condition; unsupported protocol
combinations fail explicitly. A CHILD_SA is reported as established only after the
selected installer has completed; missing Wintun, an unavailable route, an
unsupported selector, or a non-NAT-T negotiation is an explicit error.

## Automated checks

Run ordinary tests in WSL or on Windows:

```powershell
go test ./...
go vet ./...
go build -trimpath -ldflags "-s -w" -o dist\halo-windows-amd64.exe .\cmd\vocat
dist\halo-windows-amd64.exe version
```

The WFP integration test deliberately changes dynamic host IPsec state, so it
is opt-in and must run only on an isolated, elevated Windows test host:

```powershell
$env:VOCAT_WFP_INTEGRATION = "1"
$env:VOCAT_WFP_LOCAL_IP = "<local IMS address>"
$env:VOCAT_WFP_REMOTE_IP = "<remote P-CSCF address>"
go test .\internal\vowifi\ims -run TestWindowsWFPManualSAIntegration -v
```

The test installs and removes NULL/HMAC-SHA1, AES-CBC/HMAC-SHA1, and
3DES-CBC/HMAC-MD5 transport-SA pairs. It does not prove carrier
interoperability.

## Carrier/device acceptance test

Use a real SIM/eSIM and an operator profile that supports WiFi Calling. Record
the modem, Windows build, adapter name, carrier, and test time, but never put
APN passwords, EAP secrets, or tokens in logs or issue text.

1. Start `WwanSvc` and `SCardSvr`; verify the AT COM port, Cellular adapter,
   ICCID/EID, registration, IPv4/IPv6 addresses, and default route.
2. Start Vocat with the normal `VOCAT_*` configuration. Capture the IKE_SA,
   EAP-AKA, CHILD_SA, Wintun interface, and relay state transitions.
3. Verify that the ePDG outer address remains routed through the original
   cellular/Wi-Fi interface, while each negotiated responder selector is
   routed through Wintun. Check `Get-NetRoute` and `Get-NetIPAddress` before
   and after teardown; no Vocat route or address may remain after `Close`.
4. Confirm bidirectional authenticated ESP traffic, SIP REGISTER followed by
   a 401/200 exchange, periodic re-registration, and IMS SMS. If the carrier
   supports it, place and receive a voice call and check RTP in both
   directions.
5. Disconnect/reconnect the modem, toggle airplane mode, suspend/resume the
   host, restart the service, and reboot Windows. Each cycle must remove the
   old adapter/routes/SA and create exactly one new set.
6. Exercise negative cases: remove `wintun.dll`, deny elevation, break the
   outer route, force non-NAT-T, corrupt an ESP ICV, and use an unsupported
   selector. Each case must fail clearly and must not report a usable tunnel.

Useful elevated diagnostics are:

```powershell
Get-NetAdapter
Get-NetIPAddress -AddressFamily IPv4,IPv6
Get-NetRoute -AddressFamily IPv4,IPv6
netsh mbn show interfaces
pktmon filter remove
pktmon filter add -p 4500
pktmon start --etw -m real-time
# reproduce one attach/packet exchange, then press Ctrl+C
pktmon stop
```

If no real carrier/ePDG environment is available, the supported claim is
limited to compilation, unit tests, the opt-in WFP installation/cleanup test,
and this checklist. Do not describe Windows VoWiFi as carrier-validated until
the acceptance test above has passed.
