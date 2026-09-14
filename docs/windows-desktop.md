# Windows native desktop application

`vocat-desktop-windows-amd64.exe` is the native Windows 11 control center. It
uses Win32 windows and standard controls directly; it does not start a browser,
embed WebView2, or require Node.js. The normal `halo-windows-amd64.exe` binary
also exposes the same window through `vocat.exe desktop` (or
`vocat.exe windows-ui`).

## First run

1. Install the service and keep `vocat.exe`, `vocat-desktop.exe`, and
   `wintun.dll` in `%ProgramFiles%\Halo` with `scripts/windows/install.ps1`.
2. Complete `bootstrap-admin` once if the data directory is new.
3. Double-click **VoCat Windows** in the Start Menu, or launch
   `vocat-desktop-windows-amd64.exe` directly.
4. Enter the administrator account and password. The native client stores the
   session in memory only and sends credentials only to the configured service
   URL over the normal login endpoint.

The first panel reports the Windows Service state and has a **Start service**
button. The second panel displays the service version, Windows host resource
usage, and configured cellular/eSIM devices. **Open advanced Web console** is
only an optional link for settings that are not yet represented as native
controls; the desktop dashboard and authentication do not depend on it.

The client uses `VOCAT_DESKTOP_URL` when set. Otherwise it uses `VOCAT_ADDR`,
falling back to `http://127.0.0.1:7575`; wildcard service binds such as
`0.0.0.0:7575` are mapped to local loopback for the desktop process. URL
credentials, query strings, and fragments are rejected.

## Troubleshooting

- **Service not installed**: run `install.ps1` as Administrator. The Start
  service button cannot create a service; it only starts an installed one.
- **Service is stopped**: use the button, or run `Start-Service Halo` from an
  elevated PowerShell. If the service starts and immediately stops, inspect
  the Windows Event Viewer and the service's `.update-result.json` beside the
  executable.
- **Login or status connection failed**: verify the address in the native form,
  `Get-Service Halo`, and `Invoke-WebRequest http://127.0.0.1:7575/healthz`.
  When HTTPS is enabled, use the HTTPS URL and install/trust the service's
  certificate; the client does not silently disable certificate verification.
- **ePDG reports Wintun unavailable**: place the architecture-matched signed
  `wintun.dll` next to `vocat.exe` and restart the service. The installer copies
  the DLL automatically when it is included in the release artifact.
- **Shortcut should not be created**: pass `-NoDesktopShortcut` to the
  installer. Uninstall removes the shortcut and binaries but keeps data unless
  `-RemoveData` is explicitly supplied.

The complete device-management pages, SMS/eSIM operations, WireGuard settings,
and VoWiFi diagnostics remain available in the embedded Web console. Their
service and API behavior is unchanged by the native desktop entry point.
