//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The desktop client deliberately uses the Win32 control library instead of a
// browser/WebView.  This keeps the executable small, works on a clean Windows
// 11 installation, and leaves the existing embedded web UI available only as
// an optional advanced view.
const (
	wmCreate    = 0x0001
	wmDestroy   = 0x0002
	wmSize      = 0x0005
	wmClose     = 0x0010
	wmSetFont   = 0x0030
	wmCommand   = 0x0111
	wmTimer     = 0x0113
	wmAppUpdate = 0x8001

	bnClicked = 0

	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsBorder           = 0x00800000
	wsTabStop          = 0x00010000
	wsVScroll          = 0x00200000
	wsClipChildren     = 0x02000000

	wsExClientEdge = 0x00000200
	wsExAppWindow  = 0x00040000

	esLeft        = 0x0000
	esMultiline   = 0x0004
	esPassword    = 0x0020
	esAutovscroll = 0x0040
	esReadonly    = 0x0800

	bsPushButton = 0x00000000
	bsDefButton  = 0x00000001
	bsGroupBox   = 0x00000007
	ssLeft       = 0x00000000
	ssCenter     = 0x00000001

	colorWindow       = 5
	defaultGUIFont    = 17
	idcArrow          = 32512
	idiApplication    = 32512
	cwUseDefault      = 0x80000000
	showNormal        = 1
	swEnable          = 1
	swDisable         = 0
	mbOK              = 0x00000000
	mbIconError       = 0x00000010
	mbIconInformation = 0x00000040

	dpiAwarenessContextPerMonitorV2 = ^uintptr(3) // (DPI_AWARENESS_CONTEXT)-4

	desktopTimerID = 42

	controlService = 1001
	controlRefresh = 1002
	controlLogin   = 1003
	controlLogout  = 1004
	controlWeb     = 1005
	controlData    = 1006
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	uxtheme = windows.NewLazySystemDLL("uxtheme.dll")
	dwmapi  = windows.NewLazySystemDLL("dwmapi.dll")
	gdi32   = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procSetWindowTextW   = user32.NewProc("SetWindowTextW")
	procGetWindowTextW   = user32.NewProc("GetWindowTextW")
	procGetWindowTextLen = user32.NewProc("GetWindowTextLengthW")
	procMoveWindow       = user32.NewProc("MoveWindow")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procSetTimer         = user32.NewProc("SetTimer")
	procKillTimer        = user32.NewProc("KillTimer")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procEnableWindow     = user32.NewProc("EnableWindow")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procGetModuleHandleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procGetSysColorBrush = user32.NewProc("GetSysColorBrush")
	procGetStockObject   = gdi32.NewProc("GetStockObject")
	procSetDPIContext    = user32.NewProc("SetProcessDpiAwarenessContext")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")
	procSetWindowTheme   = uxtheme.NewProc("SetWindowTheme")
	procDwmSetAttribute  = dwmapi.NewProc("DwmSetWindowAttribute")
)

type desktopPoint struct {
	x int32
	y int32
}

type desktopRect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

type desktopMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      desktopPoint
}

type desktopWndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type desktopSystemInfo struct {
	Version      string `json:"version"`
	BuildTime    string `json:"build_time"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Uptime       string `json:"uptime"`
	Developer    bool   `json:"developer"`
}

type desktopHostInfo struct {
	CPUModel    string `json:"cpu_model"`
	BoardModel  string `json:"board_model"`
	MemoryModel string `json:"memory_model"`
	DiskModel   string `json:"disk_model"`
}

type desktopHostPerf struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	MemoryUsed    uint64  `json:"memory_used_bytes"`
	MemoryTotal   uint64  `json:"memory_total_bytes"`
	DiskPercent   float64 `json:"disk_percent"`
	DiskUsed      uint64  `json:"disk_used_bytes"`
	DiskTotal     uint64  `json:"disk_total_bytes"`
	NetRxBps      float64 `json:"net_rx_bps"`
	NetTxBps      float64 `json:"net_tx_bps"`
}

type desktopHost struct {
	Host desktopHostInfo `json:"host"`
	Perf desktopHostPerf `json:"perf"`
}

// Dashboard fields intentionally stay small.  The full device object is still
// available through the optional Web console, while this view keeps the
// native window readable on a laptop and never renders SIM secrets.
type desktopDevice struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DeviceType       string `json:"device_type"`
	Interface        string `json:"interface"`
	PublicIP         string `json:"public_ip"`
	Operator         string `json:"operator"`
	Model            string `json:"model"`
	NetworkMode      string `json:"network_mode"`
	NetworkDuplex    string `json:"network_duplex"`
	SignalDBM        int    `json:"signal_dbm"`
	Healthy          bool   `json:"healthy"`
	VoWiFiActive     bool   `json:"vowifi_active"`
	NetworkConnected bool   `json:"network_connected"`
}

type desktopSnapshot struct {
	Info    desktopSystemInfo
	Host    desktopHost
	Devices []desktopDevice
}

type desktopEnvelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type desktopAPI struct {
	client  *http.Client
	baseURL string

	mu   sync.RWMutex
	csrf string
}

func newDesktopAPI(rawURL string) (*desktopAPI, error) {
	baseURL, err := normalizeDesktopBaseURL(rawURL)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create desktop session cookie jar: %w", err)
	}
	return &desktopAPI{
		client:  &http.Client{Jar: jar, Timeout: 8 * time.Second},
		baseURL: baseURL,
	}, nil
}

func (api *desktopAPI) endpoint(path string) string {
	return strings.TrimRight(api.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func (api *desktopAPI) request(ctx context.Context, method, path string, payload []byte) (json.RawMessage, error) {
	var body io.Reader
	if len(payload) != 0 {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, api.endpoint(path), body)
	if err != nil {
		return nil, fmt.Errorf("create desktop API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if len(payload) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		api.mu.RLock()
		csrf := api.csrf
		api.mu.RUnlock()
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
	}
	response, err := api.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("desktop service connection failed: %w", err)
	}
	defer response.Body.Close()
	// API responses are small.  Refuse an unexpected large response rather
	// than retaining arbitrary data in a desktop process.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read desktop API response: %w", err)
	}
	var envelope desktopEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("desktop service returned HTTP %d", response.StatusCode)
		}
		return nil, errors.New("desktop service returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized {
			api.mu.Lock()
			api.csrf = ""
			api.mu.Unlock()
		}
		if envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
			return nil, fmt.Errorf("desktop service: %s", strings.TrimSpace(envelope.Error.Message))
		}
		return nil, fmt.Errorf("desktop service returned HTTP %d", response.StatusCode)
	}
	if len(envelope.Data) == 0 {
		return nil, nil
	}
	return envelope.Data, nil
}

func (api *desktopAPI) login(ctx context.Context, username string, password string) error {
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return fmt.Errorf("encode desktop login: %w", err)
	}
	defer zeroDesktopBytes(payload)
	data, err := api.request(ctx, http.MethodPost, "/api/auth/login", payload)
	if err != nil {
		return err
	}
	var result struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(data, &result); err != nil || strings.TrimSpace(result.CSRFToken) == "" {
		return errors.New("desktop service login did not return a CSRF token")
	}
	api.mu.Lock()
	api.csrf = result.CSRFToken
	api.mu.Unlock()
	return nil
}

func (api *desktopAPI) logout(ctx context.Context) error {
	_, err := api.request(ctx, http.MethodPost, "/api/auth/logout", nil)
	api.mu.Lock()
	api.csrf = ""
	api.mu.Unlock()
	return err
}

func (api *desktopAPI) snapshot(ctx context.Context) (desktopSnapshot, error) {
	var snapshot desktopSnapshot
	data, err := api.request(ctx, http.MethodGet, "/api/system/info", nil)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot.Info); err != nil {
		return snapshot, errors.New("desktop service returned invalid system information")
	}
	data, err = api.request(ctx, http.MethodGet, "/api/dashboard/host", nil)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot.Host); err != nil {
		return snapshot, errors.New("desktop service returned invalid host information")
	}
	data, err = api.request(ctx, http.MethodGet, "/api/dashboard/devices", nil)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot.Devices); err != nil {
		return snapshot, errors.New("desktop service returned invalid device information")
	}
	return snapshot, nil
}

func zeroDesktopBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type desktopServiceSnapshot struct {
	Name    string
	Label   string
	Running bool
	Exists  bool
	Err     error
}

func desktopServiceName() string {
	value := strings.TrimSpace(getenvDesktop("VOCAT_WINDOWS_SERVICE_NAME"))
	if value == "" {
		return "Halo"
	}
	return value
}

func getenvDesktop(name string) string {
	// Kept as a function to make it obvious that no credentials are read by the
	// desktop's service status path.
	return os.Getenv(name)
}

func queryDesktopService(name string) desktopServiceSnapshot {
	snapshot := desktopServiceSnapshot{Name: name, Label: "未安装", Err: nil}
	manager, err := mgr.Connect()
	if err != nil {
		snapshot.Label = "无法访问服务管理器"
		snapshot.Err = fmt.Errorf("连接 Windows 服务管理器失败: %w", err)
		return snapshot
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return snapshot
		}
		snapshot.Label = "无法打开服务"
		snapshot.Err = fmt.Errorf("打开 Windows 服务 %s 失败: %w", name, err)
		return snapshot
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		snapshot.Label = "无法读取服务状态"
		snapshot.Err = fmt.Errorf("读取 Windows 服务 %s 状态失败: %w", name, err)
		return snapshot
	}
	snapshot.Exists = true
	switch status.State {
	case svc.Running:
		snapshot.Label = "运行中"
		snapshot.Running = true
	case svc.StartPending:
		snapshot.Label = "正在启动"
	case svc.StopPending:
		snapshot.Label = "正在停止"
	case svc.Stopped:
		snapshot.Label = "已停止"
	default:
		snapshot.Label = fmt.Sprintf("状态 %d", status.State)
	}
	return snapshot
}

func startDesktopService(name string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接 Windows 服务管理器失败: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("Windows 服务 %s 未安装；请先运行 install.ps1", name)
		}
		return fmt.Errorf("打开 Windows 服务 %s 失败: %w", name, err)
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("读取 Windows 服务 %s 状态失败: %w", name, err)
	}
	if status.State == svc.Running {
		return nil
	}
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动 Windows 服务 %s 失败（可能需要管理员权限）: %w", name, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err = service.Query()
		if err != nil {
			return fmt.Errorf("等待 Windows 服务 %s 启动失败: %w", name, err)
		}
		if status.State == svc.Running {
			return nil
		}
		if status.State == svc.Stopped {
			return fmt.Errorf("Windows 服务 %s 启动后立即停止（退出码 %d）", name, status.Win32ExitCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("Windows 服务 %s 在 30 秒内没有进入运行状态", name)
}

type desktopApp struct {
	hwnd uintptr
	font uintptr

	serviceLabel  uintptr
	serviceButton uintptr
	refreshButton uintptr
	loginGroup    uintptr
	urlLabel      uintptr
	urlEdit       uintptr
	userLabel     uintptr
	userEdit      uintptr
	passwordLabel uintptr
	passwordEdit  uintptr
	loginButton   uintptr
	logoutButton  uintptr
	dataGroup     uintptr
	dataEdit      uintptr
	webButton     uintptr

	api *desktopAPI

	mu            sync.Mutex
	service       desktopServiceSnapshot
	notice        string
	lastError     string
	authenticated bool
	username      string
	busy          bool
	closing       bool
	snapshot      desktopSnapshot
	lastRefresh   time.Time
	tasks         sync.WaitGroup
}

var activeDesktopApp *desktopApp

func Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	app, err := newDesktopApp()
	if err != nil {
		return err
	}
	defer app.close()
	if err := app.messageLoop(); err != nil {
		return err
	}
	return nil
}

func ShowError(err error) {
	if err == nil {
		return
	}
	text := mustDesktopUTF16(err.Error())
	title := mustDesktopUTF16("VoCat Windows 桌面版")
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), mbOK|mbIconError)
}

func newDesktopApp() (*desktopApp, error) {
	baseURL, err := desktopBaseURLFromEnvironment(getenvDesktop)
	if err != nil {
		return nil, err
	}
	api, err := newDesktopAPI(baseURL)
	if err != nil {
		return nil, err
	}
	app := &desktopApp{
		api:     api,
		notice:  "请输入管理员账号和密码，然后点击“登录”。",
		service: desktopServiceSnapshot{Name: desktopServiceName(), Label: "正在检测"},
	}
	// Per-monitor awareness keeps the controls crisp on mixed-DPI Windows 11
	// desktops.  Older Windows versions simply ignore the optional call.
	procSetDPIContext.Call(dpiAwarenessContextPerMonitorV2)
	activeDesktopApp = app
	className := mustDesktopUTF16("VoCatDesktopWindowClass")
	hinstance, _, callErr := procGetModuleHandleW.Call(0)
	if hinstance == 0 {
		activeDesktopApp = nil
		return nil, desktopWin32Error("获取应用实例", callErr)
	}
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	brush, _, _ := procGetSysColorBrush.Call(colorWindow)
	class := desktopWndClassEx{
		cbSize:        uint32(unsafe.Sizeof(desktopWndClassEx{})),
		style:         0x0003, // CS_HREDRAW | CS_VREDRAW
		lpfnWndProc:   syscall.NewCallback(desktopWndProc),
		hInstance:     hinstance,
		hIcon:         icon,
		hCursor:       cursor,
		hbrBackground: brush,
		lpszClassName: className,
		hIconSm:       icon,
	}
	registered, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if registered == 0 && !errors.Is(registerErr, windows.ERROR_CLASS_ALREADY_EXISTS) {
		activeDesktopApp = nil
		return nil, desktopWin32Error("注册桌面窗口类", registerErr)
	}
	title := mustDesktopUTF16("VoCat · Windows 控制中心")
	hwnd, _, createErr := procCreateWindowExW.Call(
		wsExAppWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsClipChildren,
		cwUseDefault,
		cwUseDefault,
		980,
		700,
		0,
		0,
		hinstance,
		0,
	)
	if hwnd == 0 {
		activeDesktopApp = nil
		return nil, desktopWin32Error("创建桌面窗口", createErr)
	}
	app.hwnd = hwnd
	app.applyWindowStyle()
	procSetTimer.Call(hwnd, desktopTimerID, 5000, 0)
	procShowWindow.Call(hwnd, showNormal)
	procUpdateWindow.Call(hwnd)
	app.scheduleServiceRefresh()
	return app, nil
}

func (app *desktopApp) close() {
	app.mu.Lock()
	app.closing = true
	hwnd := app.hwnd
	app.mu.Unlock()
	if hwnd != 0 {
		procKillTimer.Call(hwnd, desktopTimerID)
	}
	app.tasks.Wait()
	if activeDesktopApp == app {
		activeDesktopApp = nil
	}
}

func (app *desktopApp) messageLoop() error {
	var message desktopMsg
	for {
		result, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if result == ^uintptr(0) {
			return desktopWin32Error("读取桌面消息", callErr)
		}
		if result == 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (app *desktopApp) createControls(parent uintptr) error {
	app.font, _, _ = procGetStockObject.Call(defaultGUIFont)
	create := func(class, text string, exStyle, style uint32, x, y, width, height int32, id uintptr) (uintptr, error) {
		classPtr := mustDesktopUTF16(class)
		textPtr := mustDesktopUTF16(text)
		handle, _, callErr := procCreateWindowExW.Call(
			uintptr(exStyle), uintptr(unsafe.Pointer(classPtr)), uintptr(unsafe.Pointer(textPtr)), uintptr(style),
			uintptr(x), uintptr(y), uintptr(width), uintptr(height), parent, id, 0, 0,
		)
		if handle == 0 {
			return 0, desktopWin32Error("创建桌面控件", callErr)
		}
		procSendMessageW.Call(handle, wmSetFont, app.font, 1)
		procSetWindowTheme.Call(handle, uintptr(unsafe.Pointer(mustDesktopUTF16("Explorer"))), 0)
		return handle, nil
	}
	var err error
	if app.serviceLabel, err = create("STATIC", "服务：正在检测", 0, wsChild|wsVisible|ssLeft, 24, 22, 500, 30, 0); err != nil {
		return err
	}
	if app.serviceButton, err = create("BUTTON", "启动服务", 0, wsChild|wsVisible|wsTabStop|bsPushButton, 735, 18, 105, 34, controlService); err != nil {
		return err
	}
	if app.refreshButton, err = create("BUTTON", "刷新", 0, wsChild|wsVisible|wsTabStop|bsPushButton, 850, 18, 90, 34, controlRefresh); err != nil {
		return err
	}
	if app.loginGroup, err = create("BUTTON", "连接到 Halo 服务", 0, wsChild|wsVisible|bsGroupBox, 24, 78, 420, 270, 0); err != nil {
		return err
	}
	if app.urlLabel, err = create("STATIC", "服务地址", 0, wsChild|wsVisible|ssLeft, 48, 115, 110, 24, 0); err != nil {
		return err
	}
	if app.urlEdit, err = create("EDIT", app.api.baseURL, wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft, 48, 140, 350, 28, 0); err != nil {
		return err
	}
	if app.userLabel, err = create("STATIC", "管理员账号", 0, wsChild|wsVisible|ssLeft, 48, 178, 110, 24, 0); err != nil {
		return err
	}
	if app.userEdit, err = create("EDIT", "", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft, 48, 203, 350, 28, 0); err != nil {
		return err
	}
	if app.passwordLabel, err = create("STATIC", "管理员密码", 0, wsChild|wsVisible|ssLeft, 48, 241, 110, 24, 0); err != nil {
		return err
	}
	if app.passwordEdit, err = create("EDIT", "", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft|esPassword, 48, 266, 350, 28, 0); err != nil {
		return err
	}
	if app.loginButton, err = create("BUTTON", "登录并加载状态", 0, wsChild|wsVisible|wsTabStop|bsDefButton, 48, 303, 165, 32, controlLogin); err != nil {
		return err
	}
	if app.logoutButton, err = create("BUTTON", "退出登录", 0, wsChild|wsVisible|wsTabStop|bsPushButton, 225, 303, 100, 32, controlLogout); err != nil {
		return err
	}
	if app.dataGroup, err = create("BUTTON", "设备与主机状态", 0, wsChild|wsVisible|bsGroupBox, 468, 78, 472, 520, 0); err != nil {
		return err
	}
	if app.dataEdit, err = create("EDIT", app.notice, wsExClientEdge, wsChild|wsVisible|wsBorder|wsVScroll|esLeft|esMultiline|esAutovscroll|esReadonly, 492, 115, 424, 425, controlData); err != nil {
		return err
	}
	if app.webButton, err = create("BUTTON", "打开高级 Web 控制台", 0, wsChild|wsVisible|wsTabStop|bsPushButton, 492, 555, 190, 32, controlWeb); err != nil {
		return err
	}
	app.layout()
	app.render()
	return nil
}

func (app *desktopApp) applyWindowStyle() {
	// Windows 11 rounds the frame when this optional DWM attribute exists;
	// older versions ignore the call and retain the normal Win32 frame.
	cornerPreference := uint32(2) // DWMWCP_ROUND
	procDwmSetAttribute.Call(app.hwnd, 33, uintptr(unsafe.Pointer(&cornerPreference)), unsafe.Sizeof(cornerPreference))
}

func (app *desktopApp) layout() {
	if app.hwnd == 0 {
		return
	}
	var client desktopRect
	if result, _, _ := procGetClientRect.Call(app.hwnd, uintptr(unsafe.Pointer(&client))); result == 0 {
		return
	}
	width := client.right - client.left
	height := client.bottom - client.top
	if width < 720 {
		width = 720
	}
	if height < 520 {
		height = 520
	}
	move := func(handle uintptr, x, y, w, h int32) {
		if handle != 0 {
			procMoveWindow.Call(handle, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
		}
	}
	move(app.serviceLabel, 24, 22, width-220, 30)
	move(app.serviceButton, width-205, 18, 105, 34)
	move(app.refreshButton, width-90, 18, 70, 34)
	leftWidth := (width - 72) * 43 / 100
	if leftWidth < 360 {
		leftWidth = 360
	}
	rightX := int32(48 + leftWidth)
	rightWidth := width - rightX - 24
	if rightWidth < 330 {
		rightWidth = 330
	}
	move(app.loginGroup, 24, 78, leftWidth, 270)
	move(app.dataGroup, rightX, 78, rightWidth, height-102)
	move(app.dataEdit, rightX+24, 115, rightWidth-48, height-173)
	move(app.webButton, rightX+24, height-63, 190, 32)
	for _, handle := range []uintptr{app.urlLabel, app.urlEdit, app.userLabel, app.userEdit, app.passwordLabel, app.passwordEdit, app.loginButton, app.logoutButton} {
		// The login controls stay inside the group box and are intentionally not
		// stretched with the window; this gives the compact Win11 form its stable
		// visual rhythm.
		_ = handle
	}
}

func (app *desktopApp) render() {
	app.mu.Lock()
	service := app.service
	notice := app.notice
	lastError := app.lastError
	authenticated := app.authenticated
	username := app.username
	busy := app.busy
	snapshot := app.snapshot
	lastRefresh := app.lastRefresh
	app.mu.Unlock()
	if service.Err != nil {
		// The detailed error is shown in the status line below.
	}
	serviceText := "服务：" + service.Label
	if service.Name != "" {
		serviceText += "（" + service.Name + "）"
	}
	if service.Err != nil {
		serviceText += " · " + service.Err.Error()
	}
	setDesktopText(app.serviceLabel, serviceText)
	if service.Running {
		setDesktopText(app.serviceButton, "服务已运行")
	} else {
		setDesktopText(app.serviceButton, "启动服务")
	}
	setDesktopEnabled(app.serviceButton, !busy && !service.Running)
	setDesktopEnabled(app.refreshButton, !busy)
	setDesktopEnabled(app.loginButton, !busy)
	setDesktopEnabled(app.logoutButton, authenticated && !busy)
	setDesktopEnabled(app.webButton, !busy)
	if authenticated {
		setDesktopText(app.loginButton, "刷新状态")
	} else {
		setDesktopText(app.loginButton, "登录并加载状态")
	}
	var builder strings.Builder
	if notice != "" {
		builder.WriteString(notice)
		builder.WriteString("\r\n\r\n")
	}
	if lastError != "" {
		builder.WriteString("错误：")
		builder.WriteString(lastError)
		builder.WriteString("\r\n\r\n")
	}
	if authenticated {
		builder.WriteString("登录用户：")
		builder.WriteString(username)
		builder.WriteString("\r\n\r\n")
	}
	if snapshot.Info.Version != "" {
		builder.WriteString("版本：")
		builder.WriteString(snapshot.Info.Version)
		builder.WriteString("  ·  ")
		builder.WriteString(snapshot.Info.OS)
		builder.WriteString("/")
		builder.WriteString(snapshot.Info.Architecture)
		builder.WriteString("  ·  运行时间 ")
		builder.WriteString(snapshot.Info.Uptime)
		builder.WriteString("\r\n")
		builder.WriteString("开发者模式：")
		if snapshot.Info.Developer {
			builder.WriteString("已启用")
		} else {
			builder.WriteString("关闭")
		}
		builder.WriteString("\r\n\r\n")
	}
	if snapshot.Host.Host.CPUModel != "" || snapshot.Host.Host.BoardModel != "" {
		builder.WriteString("主机：")
		builder.WriteString(firstDesktopNonEmpty(snapshot.Host.Host.CPUModel, "未知 CPU"))
		if snapshot.Host.Host.BoardModel != "" {
			builder.WriteString(" / ")
			builder.WriteString(snapshot.Host.Host.BoardModel)
		}
		builder.WriteString("\r\n")
		builder.WriteString(fmt.Sprintf("资源：CPU %.1f%%  内存 %.1f%%  磁盘 %.1f%%\r\n", snapshot.Host.Perf.CPUPercent, snapshot.Host.Perf.MemoryPercent, snapshot.Host.Perf.DiskPercent))
		builder.WriteString("\r\n")
	}
	if authenticated {
		builder.WriteString(fmt.Sprintf("设备（%d）\r\n", len(snapshot.Devices)))
		if len(snapshot.Devices) == 0 {
			builder.WriteString("  暂无已配置设备。可在高级 Web 控制台添加设备。\r\n")
		}
		for _, device := range snapshot.Devices {
			builder.WriteString("  • ")
			builder.WriteString(firstDesktopNonEmpty(device.Name, device.ID, "未命名设备"))
			builder.WriteString(" [")
			builder.WriteString(firstDesktopNonEmpty(device.DeviceType, "unknown"))
			builder.WriteString("]  ")
			if device.Healthy {
				builder.WriteString("在线")
			} else {
				builder.WriteString("离线/异常")
			}
			if device.VoWiFiActive {
				builder.WriteString(" · VoWiFi")
			} else if device.NetworkConnected {
				builder.WriteString(" · 蜂窝数据")
			}
			builder.WriteString("\r\n")
			builder.WriteString("    ")
			builder.WriteString(firstDesktopNonEmpty(device.Operator, "运营商未知"))
			if device.NetworkMode != "" {
				builder.WriteString(" · ")
				builder.WriteString(device.NetworkMode)
			}
			if device.Interface != "" {
				builder.WriteString(" · ")
				builder.WriteString(device.Interface)
			}
			builder.WriteString("\r\n")
		}
	}
	if !lastRefresh.IsZero() {
		builder.WriteString("\r\n最后刷新：")
		builder.WriteString(lastRefresh.Local().Format("2006-01-02 15:04:05"))
	}
	setDesktopText(app.dataEdit, builder.String())
}

func firstDesktopNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (app *desktopApp) scheduleServiceRefresh() {
	app.runAsync(func() error {
		service := queryDesktopService(desktopServiceName())
		app.mu.Lock()
		app.service = service
		app.lastError = ""
		app.mu.Unlock()
		return nil
	})
}

func (app *desktopApp) scheduleLoginOrRefresh() {
	app.mu.Lock()
	authenticated := app.authenticated
	app.mu.Unlock()
	if authenticated {
		app.scheduleDataRefresh()
		return
	}
	username := strings.TrimSpace(getDesktopText(app.userEdit))
	password := getDesktopText(app.passwordEdit)
	baseURL := strings.TrimSpace(getDesktopText(app.urlEdit))
	setDesktopText(app.passwordEdit, "")
	if username == "" {
		app.setError("请输入管理员账号。")
		return
	}
	if password == "" {
		app.setError("请输入管理员密码。")
		return
	}
	app.runAsync(func() error {
		api, err := newDesktopAPI(baseURL)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := api.login(ctx, username, password); err != nil {
			return err
		}
		// Do not retain the password after the HTTP request has completed.  It is
		// never included in a status string, log record, process argument, or
		// environment snapshot.
		password = ""
		snapshot, err := api.snapshot(ctx)
		if err != nil {
			return err
		}
		app.mu.Lock()
		app.api = api
		app.authenticated = true
		app.username = username
		app.snapshot = snapshot
		app.lastRefresh = time.Now()
		app.notice = "登录成功，原生桌面控制中心已连接。"
		app.lastError = ""
		app.mu.Unlock()
		return nil
	})
}

func (app *desktopApp) scheduleDataRefresh() {
	app.runAsync(func() error {
		app.mu.Lock()
		api := app.api
		app.mu.Unlock()
		if api == nil {
			return errors.New("尚未连接到桌面服务")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snapshot, err := api.snapshot(ctx)
		if err != nil {
			return err
		}
		app.mu.Lock()
		app.snapshot = snapshot
		app.lastRefresh = time.Now()
		app.notice = "状态已刷新。"
		app.lastError = ""
		app.mu.Unlock()
		return nil
	})
}

func (app *desktopApp) scheduleLogout() {
	app.runAsync(func() error {
		app.mu.Lock()
		api := app.api
		app.mu.Unlock()
		if api != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = api.logout(ctx)
			cancel()
		}
		app.mu.Lock()
		app.authenticated = false
		app.username = ""
		app.snapshot = desktopSnapshot{}
		app.notice = "已退出登录。"
		app.lastError = ""
		app.mu.Unlock()
		return nil
	})
}

func (app *desktopApp) scheduleServiceStart() {
	app.runAsync(func() error {
		if err := startDesktopService(desktopServiceName()); err != nil {
			return err
		}
		service := queryDesktopService(desktopServiceName())
		app.mu.Lock()
		app.service = service
		app.notice = "Halo 服务已启动。"
		app.lastError = ""
		app.mu.Unlock()
		return nil
	})
}

func (app *desktopApp) runAsync(task func() error) {
	app.mu.Lock()
	if app.busy || app.closing {
		app.mu.Unlock()
		return
	}
	app.busy = true
	app.lastError = ""
	app.notice = "正在处理，请稍候……"
	app.mu.Unlock()
	app.render()
	app.tasks.Add(1)
	go func() {
		defer app.tasks.Done()
		err := task()
		app.mu.Lock()
		app.busy = false
		if err != nil {
			app.lastError = err.Error()
			app.notice = "操作未完成。"
		}
		closing := app.closing
		hwnd := app.hwnd
		app.mu.Unlock()
		if !closing && hwnd != 0 {
			procPostMessageW.Call(hwnd, wmAppUpdate, 0, 0)
		}
	}()
}

func (app *desktopApp) setError(message string) {
	app.mu.Lock()
	app.lastError = message
	app.notice = "操作未完成。"
	app.mu.Unlock()
	app.render()
}

func desktopWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	app := activeDesktopApp
	if app == nil {
		result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return result
	}
	switch message {
	case wmCreate:
		app.hwnd = hwnd
		if err := app.createControls(hwnd); err != nil {
			app.setError(err.Error())
			procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}
		return 0
	case wmSize:
		app.layout()
		return 0
	case wmCommand:
		if uint16(wParam>>16) == bnClicked {
			switch uintptr(uint16(wParam)) {
			case controlService:
				app.scheduleServiceStart()
			case controlRefresh:
				app.scheduleServiceRefresh()
				app.mu.Lock()
				authenticated := app.authenticated
				app.mu.Unlock()
				if authenticated {
					app.scheduleDataRefresh()
				}
			case controlLogin:
				app.scheduleLoginOrRefresh()
			case controlLogout:
				app.scheduleLogout()
			case controlWeb:
				app.openWeb()
			}
		}
		return 0
	case wmTimer:
		if wParam == desktopTimerID {
			app.scheduleServiceRefresh()
			app.mu.Lock()
			authenticated := app.authenticated
			app.mu.Unlock()
			if authenticated {
				app.scheduleDataRefresh()
			}
		}
		return 0
	case wmAppUpdate:
		app.render()
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (app *desktopApp) openWeb() {
	app.mu.Lock()
	api := app.api
	if api == nil {
		api = &desktopAPI{baseURL: ""}
	}
	baseURL := api.baseURL
	app.mu.Unlock()
	if baseURL == "" {
		baseURL, _ = desktopBaseURLFromEnvironment(getenvDesktop)
	}
	if baseURL == "" {
		app.setError("没有可用的服务地址。")
		return
	}
	urlPtr := mustDesktopUTF16(strings.TrimRight(baseURL, "/") + "/")
	verbPtr := mustDesktopUTF16("open")
	result, _, callErr := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verbPtr)), uintptr(unsafe.Pointer(urlPtr)), 0, 0, showNormal)
	if result <= 32 {
		app.setError(desktopWin32Error("打开 Web 控制台", callErr).Error())
	}
}

func mustDesktopUTF16(value string) *uint16 {
	pointer, err := windows.UTF16PtrFromString(value)
	if err != nil {
		// All fixed labels are short and do not contain NUL.  For environment
		// supplied URLs, replacing NUL is safer than panicking in the callback.
		clean := strings.ReplaceAll(value, "\x00", "")
		pointer, _ = windows.UTF16PtrFromString(clean)
	}
	return pointer
}

func desktopWin32Error(operation string, callErr error) error {
	if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
		callErr = windows.GetLastError()
	}
	if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
		callErr = errors.New("unknown Windows error")
	}
	return fmt.Errorf("%s失败: %w", operation, callErr)
}

func setDesktopText(handle uintptr, value string) {
	if handle == 0 {
		return
	}
	text := mustDesktopUTF16(value)
	procSetWindowTextW.Call(handle, uintptr(unsafe.Pointer(text)))
}

func setDesktopEnabled(handle uintptr, enabled bool) {
	if handle == 0 {
		return
	}
	value := uintptr(swDisable)
	if enabled {
		value = swEnable
	}
	procEnableWindow.Call(handle, value)
}

func getDesktopText(handle uintptr) string {
	if handle == 0 {
		return ""
	}
	length, _, _ := procGetWindowTextLen.Call(handle)
	if length == 0 {
		return ""
	}
	buffer := make([]uint16, int(length)+1)
	procGetWindowTextW.Call(handle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return windows.UTF16ToString(buffer)
}
