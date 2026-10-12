//go:build windows

package desktop

// This file is the native Windows presentation layer.  It deliberately uses
// Win32 controls and GDI drawing instead of Electron, WebView2, or a browser
// surface.  The Go service remains the source of truth for hardware access;
// this process owns the Windows window and calls the authenticated local API.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nativeWMPaint     = 0x000F
	nativeWMLButtonUp = 0x0202
	nativeWMKeyDown   = 0x0100
	nativeWMAppUpdate = wmAppUpdate
	nativeWMClose     = wmClose
	nativeWMDestroy   = wmDestroy
	nativeWMSize      = wmSize
	nativeWMCreate    = wmCreate
	nativeWMCommand   = wmCommand
	nativeWMTimer     = wmTimer
	nativeWMSetFont   = wmSetFont
	nativeWMNCDestroy = 0x0082
	nativeWMCTLCOLOR  = 0x0133
	nativeWMCTLCOLOR2 = 0x0138
	nativeWMTray      = wmAppUpdate + 1
	nativeWMRButtonUp = 0x0205
	nativeWMDblClick  = 0x0203

	nativeSWHide        = 0
	nativeSWRestore     = 9
	nativeSizeMinimized = 1

	nativeNIMAdd       = 0x00000000
	nativeNIMDelete    = 0x00000002
	nativeNIFMessage   = 0x00000001
	nativeNIFIcon      = 0x00000002
	nativeNIFTip       = 0x00000004
	nativeMFString     = 0x00000000
	nativeMFSeparator  = 0x00000800
	nativeTPMRight     = 0x00000002
	nativeTPMReturnCmd = 0x00000100

	nativeIDLogin      uintptr = 2201
	nativeIDCommand    uintptr = 2202
	nativeIDCommandRun uintptr = 2203
	nativeIDDial       uintptr = 2204
	nativeIDDialRun    uintptr = 2205
	nativeIDSMSPhone   uintptr = 2206
	nativeIDSMSMessage uintptr = 2207
	nativeIDSMSSend    uintptr = 2208
	nativeIDLogout     uintptr = 2209
	nativeIDRefresh    uintptr = 2210
	nativeIDTrayOpen   uintptr = 2290
	nativeIDTrayExit   uintptr = 2291

	nativeSidebarWidth int32   = 248
	nativeHeaderHeight int32   = 82
	nativeTimerID      uintptr = 47

	nativeTransparent  = 1
	nativeDTLeft       = 0x00000000
	nativeDTCenter     = 0x00000001
	nativeDTRight      = 0x00000002
	nativeDTVCenter    = 0x00000004
	nativeDTWordBreak  = 0x00000010
	nativeDTSingleLine = 0x00000020
	nativeDTNoPrefix   = 0x00000800
	nativePSolid       = 0
	nativeNullPen      = 8
)

var (
	nativeBeginPaint       = user32.NewProc("BeginPaint")
	nativeEndPaint         = user32.NewProc("EndPaint")
	nativeInvalidateRect   = user32.NewProc("InvalidateRect")
	nativeSetBkMode        = gdi32.NewProc("SetBkMode")
	nativeSetTextColor     = gdi32.NewProc("SetTextColor")
	nativeSetBkColor       = gdi32.NewProc("SetBkColor")
	nativeCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	nativeCreatePen        = gdi32.NewProc("CreatePen")
	nativeSelectObject     = gdi32.NewProc("SelectObject")
	nativeDeleteObject     = gdi32.NewProc("DeleteObject")
	nativeRoundRect        = gdi32.NewProc("RoundRect")
	nativeDrawText         = user32.NewProc("DrawTextW")
	nativeCreateFont       = gdi32.NewProc("CreateFontW")
	nativeMoveToEx         = gdi32.NewProc("MoveToEx")
	nativeLineTo           = gdi32.NewProc("LineTo")
	nativeSetFocus         = user32.NewProc("SetFocus")
	nativeGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	nativeFindWindowW      = user32.NewProc("FindWindowW")
	nativeSetForeground    = user32.NewProc("SetForegroundWindow")
	nativeGetCursorPos     = user32.NewProc("GetCursorPos")
	nativeCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	nativeAppendMenuW      = user32.NewProc("AppendMenuW")
	nativeTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	nativeDestroyMenu      = user32.NewProc("DestroyMenu")
	nativeShellNotifyIcon  = shell32.NewProc("Shell_NotifyIconW")
	nativeCreateMutexW     = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW")
	nativeReleaseMutex     = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReleaseMutex")
	nativeCloseHandle      = windows.NewLazySystemDLL("kernel32.dll").NewProc("CloseHandle")
)

type nativePaintStruct struct {
	hdc        uintptr
	erase      int32
	rcPaint    desktopRect
	restore    int32
	incUpdate  int32
	rgbReserve [32]byte
}

type nativeNotifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type nativePage uint8

const (
	nativePageDashboard nativePage = iota
	nativePageDevices
	nativePagePhone
	nativePageSMS
	nativePageAT
	nativePageNetwork
	nativePageLogs
	nativePageSettings
)

type nativeDiscoveredDevice struct {
	ReaderName     string `json:"reader_name"`
	DeviceType     string `json:"device_type"`
	DiscoveryKey   string `json:"discovery_key"`
	ControlPath    string `json:"control_path"`
	NetInterface   string `json:"net_interface"`
	ATPort         string `json:"at_port"`
	IMEI           string `json:"imei"`
	Mode           string `json:"mode"`
	Configured     bool   `json:"configured"`
	Degraded       bool   `json:"degraded"`
	DiscoveryIssue string `json:"discovery_issue"`
}

type nativeLogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Caller  string `json:"caller"`
}

type nativeWorkspace struct {
	hwnd          uintptr
	instanceMutex uintptr
	trayIconAdded bool
	font          uintptr
	titleFont     uintptr
	sectionFont   uintptr
	smallFont     uintptr
	editBrush     uintptr
	ownedBackend  *exec.Cmd

	userEdit       uintptr
	passwordEdit   uintptr
	loginButton    uintptr
	commandEdit    uintptr
	commandButton  uintptr
	dialEdit       uintptr
	dialButton     uintptr
	smsPhoneEdit   uintptr
	smsMessageEdit uintptr
	smsButton      uintptr

	api *desktopAPI

	mu            sync.RWMutex
	closing       bool
	busy          bool
	authenticated bool
	username      string
	page          nativePage
	notice        string
	lastError     string
	output        string
	snapshot      desktopSnapshot
	discovered    []nativeDiscoveredDevice
	logs          []nativeLogEntry
	lastRefresh   time.Time
	tasks         sync.WaitGroup
}

var activeNativeWorkspace *nativeWorkspace

func runNativeWorkspace() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instanceMutex, alreadyRunning, err := acquireNativeInstance()
	if err != nil {
		return err
	}
	if alreadyRunning {
		return nil
	}
	app, err := newNativeWorkspace()
	if err != nil {
		releaseNativeInstance(instanceMutex)
		return err
	}
	app.instanceMutex = instanceMutex
	defer app.close()
	return app.messageLoop()
}

func acquireNativeInstance() (uintptr, bool, error) {
	name := mustDesktopUTF16("Local\\Halo.Windows.NativeWorkspace")
	handle, _, lastErr := nativeCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return 0, false, desktopWin32Error("创建 Halo 单实例锁", lastErr)
	}
	if errors.Is(lastErr, windows.ERROR_ALREADY_EXISTS) {
		nativeCloseHandle.Call(handle)
		className := mustDesktopUTF16("HaloNativeWorkspaceWindowClass")
		if hwnd, _, _ := nativeFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0); hwnd != 0 {
			procShowWindow.Call(hwnd, showNormal)
			nativeSetForeground.Call(hwnd)
		}
		return 0, true, nil
	}
	return handle, false, nil
}

func releaseNativeInstance(handle uintptr) {
	if handle == 0 {
		return
	}
	nativeReleaseMutex.Call(handle)
	nativeCloseHandle.Call(handle)
}

func newNativeWorkspace() (*nativeWorkspace, error) {
	baseURL, err := desktopBaseURLFromEnvironment(getenvDesktop)
	if err != nil {
		return nil, err
	}
	backend, err := startNativeBackendIfNeeded(baseURL)
	if err != nil {
		return nil, err
	}
	api, err := newDesktopAPI(baseURL)
	if err != nil {
		stopNativeBackend(backend)
		return nil, err
	}

	app := &nativeWorkspace{
		api:          api,
		page:         nativePageDashboard,
		notice:       "请登录 Halo 管理员账号。设备数据只通过本机 Go 核心引擎读取。",
		ownedBackend: backend,
	}
	app.font, _, _ = procGetStockObject.Call(defaultGUIFont)
	// CreateFontW is called through a small helper so the native window can use
	// a readable Segoe UI hierarchy without depending on a UI toolkit.
	app.titleFont = nativeCreateFontHandle(-27, 700)
	app.sectionFont = nativeCreateFontHandle(-17, 700)
	app.smallFont = nativeCreateFontHandle(-13, 400)
	app.editBrush, _, _ = nativeCreateSolidBrush.Call(uintptr(nativeRGB(30, 36, 45)))

	className := mustDesktopUTF16("HaloNativeWorkspaceWindowClass")
	hinstance, _, callErr := procGetModuleHandleW.Call(0)
	if hinstance == 0 {
		app.close()
		return nil, desktopWin32Error("获取应用实例", callErr)
	}
	activeNativeWorkspace = app
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	class := desktopWndClassEx{
		cbSize:        uint32(unsafe.Sizeof(desktopWndClassEx{})),
		style:         0x0003,
		lpfnWndProc:   syscall.NewCallback(nativeWorkspaceWndProc),
		hInstance:     hinstance,
		hIcon:         icon,
		hCursor:       cursor,
		hbrBackground: 0,
		lpszClassName: className,
		hIconSm:       icon,
	}
	registered, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if registered == 0 && !errors.Is(registerErr, windows.ERROR_CLASS_ALREADY_EXISTS) {
		app.close()
		activeNativeWorkspace = nil
		return nil, desktopWin32Error("注册原生窗口类", registerErr)
	}

	title := mustDesktopUTF16("Halo · Windows 原生设备工作区")
	windowX, windowY, windowWidth, windowHeight := nativeInitialWindowBounds()
	hwnd, _, createErr := procCreateWindowExW.Call(
		wsExAppWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsClipChildren,
		uintptr(windowX),
		uintptr(windowY),
		uintptr(windowWidth),
		uintptr(windowHeight),
		0,
		0,
		hinstance,
		0,
	)
	if hwnd == 0 {
		app.close()
		activeNativeWorkspace = nil
		return nil, desktopWin32Error("创建原生窗口", createErr)
	}
	app.hwnd = hwnd
	app.applyWindowStyle()
	app.addNativeTrayIcon()
	procSetTimer.Call(hwnd, nativeTimerID, 5000, 0)
	procShowWindow.Call(hwnd, showNormal)
	procUpdateWindow.Call(hwnd)
	return app, nil
}

// nativeInitialWindowBounds keeps the first launch visible and centered. The
// Win32 CW_USEDEFAULT sentinel can place a GUI process started from a shell or
// service context at (-32000,-32000), which makes a healthy client look like it
// failed to start. The dimensions are deliberately conservative so the window
// also fits on smaller Windows displays.
func nativeInitialWindowBounds() (x, y, width, height int32) {
	width, height = 1360, 860
	screenWidth, _, _ := nativeGetSystemMetrics.Call(0)
	screenHeight, _, _ := nativeGetSystemMetrics.Call(1)
	if screenWidth > 0 {
		if int32(screenWidth) < width+80 {
			width = int32(screenWidth) - 80
		}
	}
	if screenHeight > 0 {
		if int32(screenHeight) < height+80 {
			height = int32(screenHeight) - 80
		}
	}
	if width < 980 {
		width = 980
	}
	if height < 640 {
		height = 640
	}
	if screenWidth > 0 {
		x = (int32(screenWidth) - width) / 2
	}
	if screenHeight > 0 {
		y = (int32(screenHeight) - height) / 2
	}
	if x < 20 {
		x = 20
	}
	if y < 20 {
		y = 20
	}
	return x, y, width, height
}

func (app *nativeWorkspace) close() {
	app.mu.Lock()
	app.closing = true
	hwnd := app.hwnd
	app.mu.Unlock()
	if hwnd != 0 {
		procKillTimer.Call(hwnd, nativeTimerID)
	}
	app.removeNativeTrayIcon()
	app.tasks.Wait()
	stopNativeBackend(app.ownedBackend)
	if app.editBrush != 0 {
		nativeDeleteObject.Call(app.editBrush)
	}
	for _, object := range []uintptr{app.titleFont, app.sectionFont, app.smallFont} {
		if object != 0 {
			nativeDeleteObject.Call(object)
		}
	}
	if activeNativeWorkspace == app {
		activeNativeWorkspace = nil
	}
	releaseNativeInstance(app.instanceMutex)
	app.instanceMutex = 0
}

func (app *nativeWorkspace) applyWindowStyle() {
	cornerPreference := uint32(2)
	procDwmSetAttribute.Call(app.hwnd, 33, uintptr(unsafe.Pointer(&cornerPreference)), unsafe.Sizeof(cornerPreference))
}

func (app *nativeWorkspace) addNativeTrayIcon() {
	if app.hwnd == 0 || app.trayIconAdded {
		return
	}
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	data := nativeNotifyIconData{
		cbSize:           uint32(unsafe.Sizeof(nativeNotifyIconData{})),
		hWnd:             app.hwnd,
		uID:              1,
		uFlags:           nativeNIFMessage | nativeNIFIcon | nativeNIFTip,
		uCallbackMessage: nativeWMTray,
		hIcon:            icon,
	}
	copy(data.szTip[:], utf16.Encode([]rune("Halo 原生客户端")))
	result, _, _ := nativeShellNotifyIcon.Call(nativeNIMAdd, uintptr(unsafe.Pointer(&data)))
	app.trayIconAdded = result != 0
}

func (app *nativeWorkspace) removeNativeTrayIcon() {
	if app.hwnd == 0 || !app.trayIconAdded {
		return
	}
	data := nativeNotifyIconData{
		cbSize: uint32(unsafe.Sizeof(nativeNotifyIconData{})),
		hWnd:   app.hwnd,
		uID:    1,
	}
	nativeShellNotifyIcon.Call(nativeNIMDelete, uintptr(unsafe.Pointer(&data)))
	app.trayIconAdded = false
}

func (app *nativeWorkspace) restoreFromTray() {
	if app.hwnd == 0 {
		return
	}
	procShowWindow.Call(app.hwnd, showNormal)
	nativeSetForeground.Call(app.hwnd)
}

func (app *nativeWorkspace) showTrayMenu() {
	if app.hwnd == 0 {
		return
	}
	menu, _, _ := nativeCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	openText := mustDesktopUTF16("打开 Halo")
	exitText := mustDesktopUTF16("退出 Halo")
	nativeAppendMenuW.Call(menu, nativeMFString, nativeIDTrayOpen, uintptr(unsafe.Pointer(openText)))
	nativeAppendMenuW.Call(menu, nativeMFSeparator, 0, 0)
	nativeAppendMenuW.Call(menu, nativeMFString, nativeIDTrayExit, uintptr(unsafe.Pointer(exitText)))
	var point desktopPoint
	nativeGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	nativeSetForeground.Call(app.hwnd)
	command, _, _ := nativeTrackPopupMenu.Call(menu, nativeTPMRight|nativeTPMReturnCmd, uintptr(point.x), uintptr(point.y), 0, app.hwnd, 0)
	nativeDestroyMenu.Call(menu)
	switch command {
	case nativeIDTrayOpen:
		app.restoreFromTray()
	case nativeIDTrayExit:
		procDestroyWindow.Call(app.hwnd)
	}
}

func (app *nativeWorkspace) messageLoop() error {
	var message desktopMsg
	for {
		result, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if result == ^uintptr(0) {
			return desktopWin32Error("读取原生窗口消息", callErr)
		}
		if result == 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (app *nativeWorkspace) createControls(parent uintptr) error {
	create := func(class, text string, exStyle, style uint32, id uintptr) (uintptr, error) {
		classPtr := mustDesktopUTF16(class)
		textPtr := mustDesktopUTF16(text)
		handle, _, callErr := procCreateWindowExW.Call(
			uintptr(exStyle), uintptr(unsafe.Pointer(classPtr)), uintptr(unsafe.Pointer(textPtr)), uintptr(style),
			0, 0, 10, 10, parent, id, 0, 0,
		)
		if handle == 0 {
			return 0, desktopWin32Error("创建原生控件", callErr)
		}
		procSendMessageW.Call(handle, nativeWMSetFont, app.font, 1)
		return handle, nil
	}
	var err error
	if app.userEdit, err = create("EDIT", "", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft, nativeIDLogin+1); err != nil {
		return err
	}
	if app.passwordEdit, err = create("EDIT", "", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft|esPassword, nativeIDLogin+2); err != nil {
		return err
	}
	procSendMessageW.Call(app.passwordEdit, 0x00CC, uintptr('*'), 0)
	if app.loginButton, err = create("BUTTON", "登录 Halo", 0, wsChild|wsVisible|wsTabStop|bsDefButton, nativeIDLogin); err != nil {
		return err
	}
	if app.commandEdit, err = create("EDIT", "AT+CSQ", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft, nativeIDCommand); err != nil {
		return err
	}
	if app.commandButton, err = create("BUTTON", "执行", 0, wsChild|wsVisible|wsTabStop|bsPushButton, nativeIDCommandRun); err != nil {
		return err
	}
	if app.dialEdit, err = create("EDIT", "", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft, nativeIDDial); err != nil {
		return err
	}
	if app.dialButton, err = create("BUTTON", "拨号", 0, wsChild|wsVisible|wsTabStop|bsPushButton, nativeIDDialRun); err != nil {
		return err
	}
	if app.smsPhoneEdit, err = create("EDIT", "", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft, nativeIDSMSPhone); err != nil {
		return err
	}
	if app.smsMessageEdit, err = create("EDIT", "", wsExClientEdge, wsChild|wsVisible|wsTabStop|wsBorder|esLeft|esMultiline|esAutovscroll, nativeIDSMSMessage); err != nil {
		return err
	}
	if app.smsButton, err = create("BUTTON", "发送短信", 0, wsChild|wsVisible|wsTabStop|bsPushButton, nativeIDSMSSend); err != nil {
		return err
	}
	return nil
}

func (app *nativeWorkspace) layout() {
	if app.hwnd == 0 {
		return
	}
	var client desktopRect
	if result, _, _ := procGetClientRect.Call(app.hwnd, uintptr(unsafe.Pointer(&client))); result == 0 {
		return
	}
	width := client.right - client.left
	height := client.bottom - client.top
	if width < 1040 {
		width = 1040
	}
	if height < 680 {
		height = 680
	}
	move := func(handle uintptr, x, y, w, h int32, visible bool) {
		if handle == 0 {
			return
		}
		procMoveWindow.Call(handle, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
		if visible {
			procShowWindow.Call(handle, showNormal)
		} else {
			procShowWindow.Call(handle, nativeSWHide)
		}
	}
	app.mu.RLock()
	authenticated := app.authenticated
	page := app.page
	app.mu.RUnlock()
	if !authenticated {
		move(app.userEdit, width/2-205, 360, 410, 34, true)
		move(app.passwordEdit, width/2-205, 420, 410, 34, true)
		move(app.loginButton, width/2-205, 482, 150, 36, true)
		for _, handle := range []uintptr{app.commandEdit, app.commandButton, app.dialEdit, app.dialButton, app.smsPhoneEdit, app.smsMessageEdit, app.smsButton} {
			move(handle, 0, 0, 1, 1, false)
		}
		return
	}
	for _, handle := range []uintptr{app.userEdit, app.passwordEdit, app.loginButton} {
		move(handle, 0, 0, 1, 1, false)
	}
	move(app.commandEdit, nativeSidebarWidth+72, height-66, width-nativeSidebarWidth-260, 34, page == nativePageAT)
	move(app.commandButton, width-156, height-66, 100, 34, page == nativePageAT)
	move(app.dialEdit, nativeSidebarWidth+110, height-70, 300, 34, page == nativePagePhone)
	move(app.dialButton, nativeSidebarWidth+426, height-70, 100, 34, page == nativePagePhone)
	move(app.smsPhoneEdit, nativeSidebarWidth+92, height-122, 250, 34, page == nativePageSMS)
	move(app.smsMessageEdit, nativeSidebarWidth+92, height-78, width-nativeSidebarWidth-360, 34, page == nativePageSMS)
	move(app.smsButton, width-156, height-78, 100, 34, page == nativePageSMS)
}

func (app *nativeWorkspace) paint(hdc uintptr) {
	var rect desktopRect
	procGetClientRect.Call(app.hwnd, uintptr(unsafe.Pointer(&rect)))
	nativeFillRect(hdc, rect, nativeRGB(15, 18, 24))
	app.mu.RLock()
	authenticated := app.authenticated
	app.mu.RUnlock()
	if !authenticated {
		app.paintLogin(hdc, rect)
		return
	}
	app.paintWorkspace(hdc, rect)
}

func (app *nativeWorkspace) paintLogin(hdc uintptr, rect desktopRect) {
	nativeFillRect(hdc, rect, nativeRGB(13, 17, 23))
	nativeDrawTextValue(hdc, "HALO", desktopRect{120, 112, 1240, 160}, nativeRGB(255, 112, 79), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "Windows 原生设备工作区", desktopRect{120, 164, 1240, 204}, nativeRGB(241, 244, 248), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "这是 Win32 原生窗口，不会打开浏览器。设备操作由本机 Go 核心引擎执行。", desktopRect{120, 210, 1240, 244}, nativeRGB(150, 162, 177), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	card := desktopRect{120, 270, 760, 560}
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "连接到 Halo 核心", desktopRect{160, 300, 720, 340}, nativeRGB(245, 247, 250), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "管理员账号", desktopRect{160, 344, 400, 370}, nativeRGB(150, 162, 177), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "管理员密码", desktopRect{160, 405, 400, 431}, nativeRGB(150, 162, 177), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "本地服务 · 设备访问 · 数据存储", desktopRect{160, 548, 720, 580}, nativeRGB(101, 115, 132), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	app.mu.RLock()
	notice := app.notice
	errText := app.lastError
	app.mu.RUnlock()
	if errText != "" {
		nativeDrawTextValue(hdc, errText, desktopRect{820, 320, rect.right - 90, 400}, nativeRGB(255, 125, 103), app.smallFont, nativeDTLeft|nativeDTWordBreak|nativeDTNoPrefix)
	} else {
		nativeDrawTextValue(hdc, notice, desktopRect{820, 320, rect.right - 90, 400}, nativeRGB(173, 184, 198), app.smallFont, nativeDTLeft|nativeDTWordBreak|nativeDTNoPrefix)
	}
}

func (app *nativeWorkspace) paintWorkspace(hdc uintptr, rect desktopRect) {
	app.mu.RLock()
	page := app.page
	snapshot := app.snapshot
	discovered := append([]nativeDiscoveredDevice(nil), app.discovered...)
	logs := append([]nativeLogEntry(nil), app.logs...)
	notice := app.notice
	errText := app.lastError
	output := app.output
	username := app.username
	busy := app.busy
	app.mu.RUnlock()

	nativeFillRect(hdc, desktopRect{0, 0, nativeSidebarWidth, rect.bottom}, nativeRGB(20, 25, 33))
	nativeFillRect(hdc, desktopRect{nativeSidebarWidth, 0, rect.right, nativeHeaderHeight}, nativeRGB(17, 22, 29))
	nativeDrawLine(hdc, nativeSidebarWidth, 0, nativeSidebarWidth, rect.bottom, nativeRGB(45, 54, 66))
	nativeDrawLine(hdc, nativeSidebarWidth, nativeHeaderHeight-1, rect.right, nativeHeaderHeight-1, nativeRGB(45, 54, 66))

	nativeDrawTextValue(hdc, "HALO", desktopRect{28, 30, 220, 62}, nativeRGB(255, 112, 79), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "WINDOWS NATIVE", desktopRect{30, 64, 220, 86}, nativeRGB(112, 128, 147), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawSidebar(hdc, rect, page, username)

	title, subtitle := nativePageTitle(page)
	nativeDrawTextValue(hdc, title, desktopRect{nativeSidebarWidth + 32, 22, rect.right - 280, 52}, nativeRGB(243, 246, 249), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, subtitle, desktopRect{nativeSidebarWidth + 34, 53, rect.right - 280, 76}, nativeRGB(137, 151, 169), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawPill(hdc, desktopRect{rect.right - 226, 25, rect.right - 112, 55}, nativeRGB(21, 79, 67), nativeRGB(94, 226, 176), "● 已连接", app.smallFont)
	nativeDrawTextValue(hdc, "刷新", desktopRect{rect.right - 92, 27, rect.right - 32, 55}, nativeRGB(255, 142, 112), app.sectionFont, nativeDTRight|nativeDTSingleLine|nativeDTNoPrefix)

	switch page {
	case nativePageDashboard:
		app.paintDashboard(hdc, rect, snapshot)
	case nativePageDevices:
		app.paintDevices(hdc, rect, snapshot, discovered)
	case nativePagePhone:
		app.paintPhone(hdc, rect, snapshot, output)
	case nativePageSMS:
		app.paintSMS(hdc, rect, snapshot, output)
	case nativePageAT:
		app.paintAT(hdc, rect, snapshot, output)
	case nativePageNetwork:
		app.paintNetwork(hdc, rect, snapshot)
	case nativePageLogs:
		app.paintLogs(hdc, rect, logs)
	case nativePageSettings:
		app.paintSettings(hdc, rect, snapshot)
	}
	if notice != "" || errText != "" || busy {
		text := notice
		color := nativeRGB(151, 166, 184)
		if errText != "" {
			text = "错误：" + errText
			color = nativeRGB(255, 125, 103)
		}
		if busy {
			text = "正在处理，请稍候……"
			color = nativeRGB(255, 183, 96)
		}
		nativeDrawTextValue(hdc, text, desktopRect{nativeSidebarWidth + 36, rect.bottom - 34, rect.right - 230, rect.bottom - 10}, color, app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	}
}

func (app *nativeWorkspace) paintDashboard(hdc uintptr, rect desktopRect, snapshot desktopSnapshot) {
	x := nativeSidebarWidth + 32
	w := rect.right - x - 32
	y := nativeHeaderHeight + 30
	gap := int32(16)
	cardW := (w - gap*2) / 3
	nativeMetricCard(hdc, desktopRect{x, y, x + cardW, y + 118}, "核心引擎", "Halo Go", "Windows 原生服务进程", nativeRGB(255, 112, 79), app.sectionFont, app.smallFont)
	nativeMetricCard(hdc, desktopRect{x + cardW + gap, y, x + cardW*2 + gap, y + 118}, "设备在线", fmt.Sprintf("%d 台", len(snapshot.Devices)), "来自实时设备快照", nativeRGB(95, 220, 170), app.sectionFont, app.smallFont)
	nativeMetricCard(hdc, desktopRect{x + (cardW+gap)*2, y, rect.right - 32, y + 118}, "系统", firstDesktopNonEmpty(snapshot.Info.OS, "Windows"), firstDesktopNonEmpty(snapshot.Info.Architecture, "amd64"), nativeRGB(129, 164, 255), app.sectionFont, app.smallFont)

	main := desktopRect{x, y + 144, x + int32(float64(w)*0.62), rect.bottom - 62}
	side := desktopRect{main.right + gap, main.top, rect.right - 32, main.bottom}
	nativeDrawCard(hdc, main, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "设备状态", desktopRect{main.left + 24, main.top + 22, main.right - 24, main.top + 52}, nativeRGB(239, 243, 248), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "实时设备与网络连接概览", desktopRect{main.left + 24, main.top + 54, main.right - 24, main.top + 80}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	if len(snapshot.Devices) == 0 {
		nativeDrawTextValue(hdc, "尚未配置设备", desktopRect{main.left + 24, main.top + 122, main.right - 24, main.top + 160}, nativeRGB(154, 166, 180), app.sectionFont, nativeDTCenter|nativeDTSingleLine|nativeDTNoPrefix)
		nativeDrawTextValue(hdc, "进入“设备管理”扫描并添加 USB/COM 设备。", desktopRect{main.left + 24, main.top + 168, main.right - 24, main.top + 198}, nativeRGB(128, 143, 161), app.smallFont, nativeDTCenter|nativeDTSingleLine|nativeDTNoPrefix)
	} else {
		for index, device := range snapshot.Devices {
			if index >= 5 {
				break
			}
			nativeDeviceRow(hdc, main, int32(index), device, app.sectionFont, app.smallFont)
		}
	}
	nativeDrawCard(hdc, side, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "宿主机性能", desktopRect{side.left + 22, side.top + 22, side.right - 22, side.top + 52}, nativeRGB(239, 243, 248), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, fmt.Sprintf("CPU %.1f%%", snapshot.Host.Perf.CPUPercent), desktopRect{side.left + 22, side.top + 84, side.right - 22, side.top + 116}, nativeRGB(255, 181, 94), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, fmt.Sprintf("内存 %.1f%%", snapshot.Host.Perf.MemoryPercent), desktopRect{side.left + 22, side.top + 134, side.right - 22, side.top + 166}, nativeRGB(95, 220, 170), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, fmt.Sprintf("磁盘 %.1f%%", snapshot.Host.Perf.DiskPercent), desktopRect{side.left + 22, side.top + 184, side.right - 22, side.top + 216}, nativeRGB(129, 164, 255), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, firstDesktopNonEmpty(snapshot.Host.Host.CPUModel, "Windows 主机"), desktopRect{side.left + 22, side.bottom - 60, side.right - 22, side.bottom - 28}, nativeRGB(128, 143, 161), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
}

func (app *nativeWorkspace) paintDevices(hdc uintptr, rect desktopRect, snapshot desktopSnapshot, discovered []nativeDiscoveredDevice) {
	x := nativeSidebarWidth + 32
	w := rect.right - x - 32
	card := desktopRect{x, nativeHeaderHeight + 30, x + w, rect.bottom - 62}
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "设备管理", desktopRect{card.left + 24, card.top + 22, card.right - 220, card.top + 54}, nativeRGB(239, 243, 248), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "管理已配置设备并扫描当前 USB / COM 硬件", desktopRect{card.left + 26, card.top + 58, card.right - 220, card.top + 84}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawPill(hdc, desktopRect{card.right - 182, card.top + 25, card.right - 28, card.top + 59}, nativeRGB(62, 42, 36), nativeRGB(255, 142, 112), "重新扫描", app.smallFont)
	rowY := card.top + 116
	if len(snapshot.Devices) == 0 {
		nativeDrawTextValue(hdc, "没有已配置设备。点击“重新扫描”查看当前连接的硬件。", desktopRect{card.left + 28, rowY, card.right - 28, rowY + 36}, nativeRGB(159, 171, 185), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	} else {
		for index, device := range snapshot.Devices {
			nativeDeviceRow(hdc, card, int32(index), device, app.sectionFont, app.smallFont)
		}
	}
	if len(discovered) > 0 {
		bottom := card.top + 330
		nativeDrawLine(hdc, card.left+24, bottom, card.right-24, bottom, nativeRGB(55, 65, 79))
		nativeDrawTextValue(hdc, fmt.Sprintf("本次扫描发现 %d 个硬件入口", len(discovered)), desktopRect{card.left + 24, bottom + 18, card.right - 24, bottom + 48}, nativeRGB(95, 220, 170), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		for index, device := range discovered {
			if index >= 4 {
				break
			}
			text := firstDesktopNonEmpty(device.ReaderName, device.DeviceType, "未知设备") + " · " + firstDesktopNonEmpty(device.ATPort, device.ControlPath, "端口未知")
			if device.Configured {
				text += " · 已配置"
			}
			nativeDrawTextValue(hdc, text, desktopRect{card.left + 32, bottom + 62 + int32(index)*29, card.right - 32, bottom + 88 + int32(index)*29}, nativeRGB(173, 184, 198), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		}
	}
}

func (app *nativeWorkspace) paintPhone(hdc uintptr, rect desktopRect, snapshot desktopSnapshot, output string) {
	card := nativeContentCard(rect)
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "电话", desktopRect{card.left + 26, card.top + 24, card.right - 30, card.top + 58}, nativeRGB(239, 243, 248), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "通过已连接设备拨号；实际操作由 modem API 执行", desktopRect{card.left + 28, card.top + 62, card.right - 30, card.top + 88}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "号码", desktopRect{card.left + 28, rect.bottom - 110, card.left + 90, rect.bottom - 84}, nativeRGB(150, 162, 177), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	if len(snapshot.Devices) == 0 {
		nativeDrawTextValue(hdc, "没有可用设备，请先在设备管理中完成发现和配置。", desktopRect{card.left + 28, card.top + 146, card.right - 28, card.top + 190}, nativeRGB(255, 181, 94), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	} else {
		nativeDrawTextValue(hdc, "当前设备："+firstDesktopNonEmpty(snapshot.Devices[0].Name, snapshot.Devices[0].ID, "未命名设备"), desktopRect{card.left + 28, card.top + 140, card.right - 28, card.top + 174}, nativeRGB(95, 220, 170), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	}
	if output != "" {
		nativeDrawCard(hdc, desktopRect{card.left + 28, card.top + 220, card.right - 28, rect.bottom - 160}, nativeRGB(18, 23, 30), nativeRGB(46, 56, 68))
		nativeDrawTextValue(hdc, output, desktopRect{card.left + 48, card.top + 244, card.right - 48, rect.bottom - 180}, nativeRGB(182, 194, 208), app.smallFont, nativeDTLeft|nativeDTWordBreak|nativeDTNoPrefix)
	}
}

func (app *nativeWorkspace) paintSMS(hdc uintptr, rect desktopRect, snapshot desktopSnapshot, output string) {
	card := nativeContentCard(rect)
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "短信", desktopRect{card.left + 26, card.top + 24, card.right - 30, card.top + 58}, nativeRGB(239, 243, 248), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "直接从原生窗口发送短信，不经过外部浏览器", desktopRect{card.left + 28, card.top + 62, card.right - 30, card.top + 88}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "收件人", desktopRect{card.left + 28, rect.bottom - 164, card.left + 90, rect.bottom - 138}, nativeRGB(150, 162, 177), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "内容", desktopRect{card.left + 28, rect.bottom - 120, card.left + 90, rect.bottom - 94}, nativeRGB(150, 162, 177), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	if len(snapshot.Devices) == 0 {
		nativeDrawTextValue(hdc, "没有可用设备，请先在设备管理中完成发现和配置。", desktopRect{card.left + 28, card.top + 146, card.right - 28, card.top + 190}, nativeRGB(255, 181, 94), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	}
	if output != "" {
		nativeDrawCard(hdc, desktopRect{card.left + 28, card.top + 220, card.right - 28, rect.bottom - 220}, nativeRGB(18, 23, 30), nativeRGB(46, 56, 68))
		nativeDrawTextValue(hdc, output, desktopRect{card.left + 48, card.top + 244, card.right - 48, rect.bottom - 242}, nativeRGB(182, 194, 208), app.smallFont, nativeDTLeft|nativeDTWordBreak|nativeDTNoPrefix)
	}
}

func (app *nativeWorkspace) paintAT(hdc uintptr, rect desktopRect, snapshot desktopSnapshot, output string) {
	card := nativeContentCard(rect)
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "AT 命令终端", desktopRect{card.left + 26, card.top + 24, card.right - 30, card.top + 58}, nativeRGB(239, 243, 248), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "命令会通过当前设备的 AT 通道执行，危险操作仍由后端安全策略拦截", desktopRect{card.left + 28, card.top + 62, card.right - 30, card.top + 88}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "命令", desktopRect{card.left + 28, rect.bottom - 90, card.left + 90, rect.bottom - 62}, nativeRGB(150, 162, 177), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	if len(snapshot.Devices) == 0 {
		nativeDrawTextValue(hdc, "没有可用设备。", desktopRect{card.left + 28, card.top + 146, card.right - 28, card.top + 190}, nativeRGB(255, 181, 94), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	}
	if output == "" {
		output = "示例：AT+CSQ、ATI、AT+COPS?"
	}
	nativeDrawCard(hdc, desktopRect{card.left + 28, card.top + 126, card.right - 28, rect.bottom - 132}, nativeRGB(18, 23, 30), nativeRGB(46, 56, 68))
	nativeDrawTextValue(hdc, output, desktopRect{card.left + 48, card.top + 150, card.right - 48, rect.bottom - 152}, nativeRGB(182, 194, 208), app.smallFont, nativeDTLeft|nativeDTWordBreak|nativeDTNoPrefix)
}

func (app *nativeWorkspace) paintNetwork(hdc uintptr, rect desktopRect, snapshot desktopSnapshot) {
	card := nativeContentCard(rect)
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "网络与 VoWiFi", desktopRect{card.left + 26, card.top + 24, card.right - 30, card.top + 58}, nativeRGB(239, 243, 248), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "蜂窝数据、运营商、信号与 WiFi Calling 状态", desktopRect{card.left + 28, card.top + 62, card.right - 30, card.top + 88}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	if len(snapshot.Devices) == 0 {
		nativeDrawTextValue(hdc, "暂无设备网络状态。", desktopRect{card.left + 28, card.top + 146, card.right - 28, card.top + 190}, nativeRGB(159, 171, 185), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		return
	}
	for index, device := range snapshot.Devices {
		y := card.top + 128 + int32(index)*104
		if y+88 > card.bottom {
			break
		}
		nativeDrawCard(hdc, desktopRect{card.left + 24, y, card.right - 24, y + 84}, nativeRGB(30, 37, 47), nativeRGB(47, 58, 72))
		nativeDrawTextValue(hdc, firstDesktopNonEmpty(device.Name, device.ID, "未命名设备"), desktopRect{card.left + 44, y + 16, card.right - 260, y + 44}, nativeRGB(238, 242, 247), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		nativeDrawTextValue(hdc, firstDesktopNonEmpty(device.Operator, "运营商未知")+" · "+firstDesktopNonEmpty(device.NetworkMode, "网络未知"), desktopRect{card.left + 44, y + 48, card.right - 260, y + 72}, nativeRGB(145, 159, 176), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		status := "离线"
		color := nativeRGB(255, 125, 103)
		if device.Healthy || device.NetworkConnected {
			status = "在线"
			color = nativeRGB(95, 220, 170)
		}
		if device.VoWiFiActive {
			status = "VoWiFi"
			color = nativeRGB(129, 164, 255)
		}
		nativeDrawTextValue(hdc, status, desktopRect{card.right - 220, y + 25, card.right - 46, y + 57}, color, app.sectionFont, nativeDTRight|nativeDTSingleLine|nativeDTNoPrefix)
	}
}

func (app *nativeWorkspace) paintLogs(hdc uintptr, rect desktopRect, logs []nativeLogEntry) {
	card := nativeContentCard(rect)
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "实时日志", desktopRect{card.left + 26, card.top + 24, card.right - 30, card.top + 58}, nativeRGB(239, 243, 248), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "来自 Halo 核心引擎的最近事件", desktopRect{card.left + 28, card.top + 62, card.right - 30, card.top + 88}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	if len(logs) == 0 {
		nativeDrawTextValue(hdc, "暂无可显示的日志。点击右上角刷新。", desktopRect{card.left + 28, card.top + 142, card.right - 28, card.top + 180}, nativeRGB(159, 171, 185), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		return
	}
	for index, entry := range logs {
		if index >= 14 {
			break
		}
		y := card.top + 112 + int32(index)*34
		levelColor := nativeRGB(173, 184, 198)
		if strings.EqualFold(entry.Level, "error") {
			levelColor = nativeRGB(255, 125, 103)
		} else if strings.EqualFold(entry.Level, "warn") || strings.EqualFold(entry.Level, "warning") {
			levelColor = nativeRGB(255, 181, 94)
		}
		nativeDrawTextValue(hdc, strings.ToUpper(firstDesktopNonEmpty(entry.Level, "INFO")), desktopRect{card.left + 28, y, card.left + 100, y + 24}, levelColor, app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		nativeDrawTextValue(hdc, entry.Message, desktopRect{card.left + 112, y, card.right - 28, y + 24}, nativeRGB(190, 200, 211), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	}
}

func (app *nativeWorkspace) paintSettings(hdc uintptr, rect desktopRect, snapshot desktopSnapshot) {
	card := nativeContentCard(rect)
	nativeDrawCard(hdc, card, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, "系统设置", desktopRect{card.left + 26, card.top + 24, card.right - 30, card.top + 58}, nativeRGB(239, 243, 248), app.titleFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "原生窗口、Go 核心与设备访问状态", desktopRect{card.left + 28, card.top + 62, card.right - 30, card.top + 88}, nativeRGB(133, 148, 166), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	rows := [][2]string{
		{"界面", "Win32 原生窗口 + GDI 绘制"},
		{"核心引擎", "Go Windows amd64"},
		{"运行版本", firstDesktopNonEmpty(snapshot.Info.Version, "未知")},
		{"操作系统", firstDesktopNonEmpty(snapshot.Info.OS, "windows") + " / " + firstDesktopNonEmpty(snapshot.Info.Architecture, "amd64")},
		{"服务通信", "本机进程内 HTTP API，仅用于 UI 与核心引擎通信"},
		{"浏览器依赖", "无 Electron、无 WebView2、无外部浏览器"},
	}
	for index, row := range rows {
		y := card.top + 132 + int32(index)*58
		nativeDrawTextValue(hdc, row[0], desktopRect{card.left + 30, y, card.left + 180, y + 28}, nativeRGB(143, 157, 174), app.smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
		nativeDrawTextValue(hdc, row[1], desktopRect{card.left + 200, y, card.right - 32, y + 28}, nativeRGB(235, 239, 244), app.sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	}
}

func nativeContentCard(rect desktopRect) desktopRect {
	return desktopRect{nativeSidebarWidth + 32, nativeHeaderHeight + 30, rect.right - 32, rect.bottom - 62}
}

func nativePageTitle(page nativePage) (string, string) {
	switch page {
	case nativePageDevices:
		return "设备管理", "发现、配置和监控本机 USB / COM 设备"
	case nativePagePhone:
		return "电话", "通过当前设备拨号和查看通话状态"
	case nativePageSMS:
		return "短信", "使用本机 modem 发送和接收短信"
	case nativePageAT:
		return "AT 命令", "直接执行受安全策略保护的 modem 命令"
	case nativePageNetwork:
		return "网络", "蜂窝网络、VoWiFi 与连接状态"
	case nativePageLogs:
		return "实时日志", "诊断 Halo 核心引擎和设备操作"
	case nativePageSettings:
		return "系统设置", "查看原生客户端和核心引擎运行信息"
	default:
		return "设备监控", "实时查看 Halo 核心引擎和设备状态"
	}
}

func nativeDrawSidebar(hdc uintptr, rect desktopRect, active nativePage, username string) {
	items := []struct {
		page  nativePage
		label string
	}{
		{nativePageDashboard, "仪表盘"},
		{nativePageDevices, "设备管理"},
		{nativePagePhone, "电话"},
		{nativePageSMS, "短信"},
		{nativePageAT, "AT 命令"},
		{nativePageNetwork, "网络与 VoWiFi"},
		{nativePageLogs, "实时日志"},
		{nativePageSettings, "系统设置"},
	}
	for index, item := range items {
		y := int32(124 + index*45)
		if item.page == active {
			nativeDrawCard(hdc, desktopRect{18, y - 7, nativeSidebarWidth - 18, y + 31}, nativeRGB(57, 42, 39), nativeRGB(103, 65, 54))
			nativeFillRect(hdc, desktopRect{18, y - 7, 22, y + 31}, nativeRGB(255, 112, 79))
		}
		nativeDrawTextValue(hdc, item.label, desktopRect{38, y, nativeSidebarWidth - 25, y + 25}, nativeRGB(211, 220, 231), 0, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	}
	nativeDrawLine(hdc, 22, rect.bottom-104, nativeSidebarWidth-22, rect.bottom-104, nativeRGB(45, 54, 66))
	nativeDrawTextValue(hdc, "Halo 已就绪", desktopRect{28, rect.bottom - 82, nativeSidebarWidth - 26, rect.bottom - 57}, nativeRGB(95, 220, 170), 0, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, firstDesktopNonEmpty(username, "管理员"), desktopRect{28, rect.bottom - 53, nativeSidebarWidth - 26, rect.bottom - 30}, nativeRGB(139, 153, 171), 0, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, "退出登录", desktopRect{nativeSidebarWidth - 98, rect.bottom - 53, nativeSidebarWidth - 26, rect.bottom - 30}, nativeRGB(255, 142, 112), 0, nativeDTRight|nativeDTSingleLine|nativeDTNoPrefix)
}

func nativeMetricCard(hdc uintptr, rect desktopRect, label, value, detail string, accent uint32, sectionFont, smallFont uintptr) {
	nativeDrawCard(hdc, rect, nativeRGB(25, 31, 40), nativeRGB(55, 65, 79))
	nativeDrawTextValue(hdc, label, desktopRect{rect.left + 18, rect.top + 16, rect.right - 18, rect.top + 42}, nativeRGB(143, 157, 174), smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, value, desktopRect{rect.left + 18, rect.top + 45, rect.right - 18, rect.top + 79}, accent, sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, detail, desktopRect{rect.left + 18, rect.top + 84, rect.right - 18, rect.top + 105}, nativeRGB(128, 143, 161), smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
}

func nativeDeviceRow(hdc uintptr, parent desktopRect, index int32, device desktopDevice, sectionFont, smallFont uintptr) {
	y := parent.top + 105 + index*86
	if y+70 > parent.bottom-18 {
		return
	}
	nativeDrawCard(hdc, desktopRect{parent.left + 24, y, parent.right - 24, y + 70}, nativeRGB(30, 37, 47), nativeRGB(47, 58, 72))
	status := "离线"
	color := nativeRGB(255, 125, 103)
	if device.Healthy || device.NetworkConnected {
		status = "在线"
		color = nativeRGB(95, 220, 170)
	}
	nativeDrawTextValue(hdc, firstDesktopNonEmpty(device.Name, device.ID, "未命名设备"), desktopRect{parent.left + 42, y + 12, parent.right - 180, y + 39}, nativeRGB(239, 243, 248), sectionFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, firstDesktopNonEmpty(device.Operator, "运营商未知")+" · "+firstDesktopNonEmpty(device.NetworkMode, "网络未知")+" · "+firstDesktopNonEmpty(device.Interface, "接口未知"), desktopRect{parent.left + 42, y + 42, parent.right - 180, y + 63}, nativeRGB(145, 159, 176), smallFont, nativeDTLeft|nativeDTSingleLine|nativeDTNoPrefix)
	nativeDrawTextValue(hdc, status, desktopRect{parent.right - 150, y + 20, parent.right - 42, y + 48}, color, sectionFont, nativeDTRight|nativeDTSingleLine|nativeDTNoPrefix)
}

func nativeDrawCard(hdc uintptr, rect desktopRect, fill, border uint32) {
	brush, _, _ := nativeCreateSolidBrush.Call(uintptr(fill))
	pen, _, _ := nativeCreatePen.Call(nativePSolid, 1, uintptr(border))
	oldBrush, _, _ := nativeSelectObject.Call(hdc, brush)
	oldPen, _, _ := nativeSelectObject.Call(hdc, pen)
	nativeRoundRect.Call(hdc, uintptr(rect.left), uintptr(rect.top), uintptr(rect.right), uintptr(rect.bottom), 16, 16)
	nativeSelectObject.Call(hdc, oldBrush)
	nativeSelectObject.Call(hdc, oldPen)
	nativeDeleteObject.Call(brush)
	nativeDeleteObject.Call(pen)
}

func nativeDrawPill(hdc uintptr, rect desktopRect, fill, color uint32, text string, font uintptr) {
	nativeDrawCard(hdc, rect, fill, fill)
	nativeDrawTextValue(hdc, text, rect, color, font, nativeDTCenter|nativeDTVCenter|nativeDTSingleLine|nativeDTNoPrefix)
}

func nativeDrawLine(hdc uintptr, x1, y1, x2, y2 int32, color uint32) {
	pen, _, _ := nativeCreatePen.Call(nativePSolid, 1, uintptr(color))
	old, _, _ := nativeSelectObject.Call(hdc, pen)
	nativeMoveToEx.Call(hdc, uintptr(x1), uintptr(y1), 0)
	nativeLineTo.Call(hdc, uintptr(x2), uintptr(y2))
	nativeSelectObject.Call(hdc, old)
	nativeDeleteObject.Call(pen)
}

func nativeFillRect(hdc uintptr, rect desktopRect, color uint32) {
	brush, _, _ := nativeCreateSolidBrush.Call(uintptr(color))
	procFillRect := user32.NewProc("FillRect")
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rect)), brush)
	nativeDeleteObject.Call(brush)
}

func nativeDrawTextValue(hdc uintptr, value string, rect desktopRect, color uint32, font uintptr, flags uint32) {
	if strings.TrimSpace(value) == "" {
		return
	}
	text := mustDesktopUTF16(value)
	nativeSetBkMode.Call(hdc, nativeTransparent)
	nativeSetTextColor.Call(hdc, uintptr(color))
	old := uintptr(0)
	if font != 0 {
		old, _, _ = nativeSelectObject.Call(hdc, font)
	}
	nativeDrawText.Call(hdc, uintptr(unsafe.Pointer(text)), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&rect)), uintptr(flags))
	if old != 0 {
		nativeSelectObject.Call(hdc, old)
	}
}

func nativeCreateFontHandle(height, weight int32) uintptr {
	name := mustDesktopUTF16("Segoe UI")
	font, _, _ := nativeCreateFont.Call(
		uintptr(height), 0, 0, 0, uintptr(weight), 0, 0, 0,
		uintptr(1), 0, 0, 5, uintptr(0x20), uintptr(unsafe.Pointer(name)),
	)
	return font
}

func nativeRGB(red, green, blue byte) uint32 {
	return uint32(red) | uint32(green)<<8 | uint32(blue)<<16
}

func (app *nativeWorkspace) onClick(x, y int32) {
	app.mu.RLock()
	authenticated := app.authenticated
	page := app.page
	app.mu.RUnlock()
	if !authenticated {
		return
	}
	if x < nativeSidebarWidth && y >= 104 && y < 104+8*45 {
		index := int((y - 104) / 45)
		app.mu.Lock()
		app.page = nativePage(index)
		app.output = ""
		app.lastError = ""
		app.notice = ""
		app.mu.Unlock()
		app.layout()
		app.nativeInvalidate()
		app.schedulePageRefresh()
		return
	}
	if y < nativeHeaderHeight && x > 0 && x >= nativeSidebarWidth && x > (nativeSidebarWidth+640) {
		app.schedulePageRefresh()
		return
	}
	if page == nativePageDevices && x > 0 && y >= nativeHeaderHeight+30 && y <= nativeHeaderHeight+120 {
		app.schedulePageRefresh()
	}
	if y > 0 {
		var rect desktopRect
		procGetClientRect.Call(app.hwnd, uintptr(unsafe.Pointer(&rect)))
		if x >= nativeSidebarWidth-122 && y >= rect.bottom-74 {
			app.scheduleLogout()
		}
	}
}

func (app *nativeWorkspace) schedulePageRefresh() {
	app.runAsync(func() error {
		app.mu.RLock()
		api := app.api
		page := app.page
		app.mu.RUnlock()
		if api == nil {
			return errors.New("尚未登录 Halo 核心引擎")
		}
		ctx, cancel := contextWithDesktopTimeout()
		defer cancel()
		snapshot, err := api.snapshot(ctx)
		if err != nil {
			return err
		}
		var discovered []nativeDiscoveredDevice
		var logs []nativeLogEntry
		if page == nativePageDevices {
			raw, requestErr := api.request(ctx, http.MethodGet, "/api/devices/discovered", nil)
			if requestErr != nil {
				return requestErr
			}
			var envelope struct {
				Devices []nativeDiscoveredDevice `json:"devices"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return fmt.Errorf("解析设备扫描结果失败: %w", err)
			}
			discovered = envelope.Devices
		}
		if page == nativePageLogs {
			raw, requestErr := api.request(ctx, http.MethodGet, "/api/logs/history?lines=80", nil)
			if requestErr != nil {
				return requestErr
			}
			var envelope struct {
				Logs []nativeLogEntry `json:"logs"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return fmt.Errorf("解析日志失败: %w", err)
			}
			logs = envelope.Logs
		}
		app.mu.Lock()
		app.snapshot = snapshot
		app.discovered = discovered
		app.logs = logs
		app.lastRefresh = time.Now()
		app.notice = "状态已刷新。"
		app.lastError = ""
		app.mu.Unlock()
		return nil
	})
}

func (app *nativeWorkspace) scheduleLogin() {
	username := strings.TrimSpace(getDesktopText(app.userEdit))
	password := getDesktopText(app.passwordEdit)
	if username == "" {
		app.setError("请输入管理员账号。")
		return
	}
	if password == "" {
		app.setError("请输入管理员密码。")
		return
	}
	setDesktopText(app.passwordEdit, "")
	app.runAsync(func() error {
		api, err := newDesktopAPI(app.api.baseURL)
		if err != nil {
			return err
		}
		ctx, cancel := contextWithDesktopTimeout()
		defer cancel()
		if err := api.login(ctx, username, password); err != nil {
			password = ""
			return err
		}
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
		app.page = nativePageDashboard
		app.notice = "登录成功，原生桌面工作区已连接。"
		app.lastError = ""
		app.lastRefresh = time.Now()
		app.mu.Unlock()
		return nil
	})
}

func (app *nativeWorkspace) scheduleLogout() {
	app.runAsync(func() error {
		app.mu.RLock()
		api := app.api
		app.mu.RUnlock()
		if api != nil {
			ctx, cancel := contextWithDesktopTimeout()
			_ = api.logout(ctx)
			cancel()
		}
		app.mu.Lock()
		app.authenticated = false
		app.username = ""
		app.snapshot = desktopSnapshot{}
		app.discovered = nil
		app.logs = nil
		app.notice = "已退出登录。"
		app.lastError = ""
		app.output = ""
		app.mu.Unlock()
		return nil
	})
}

func (app *nativeWorkspace) scheduleAT() {
	command := strings.TrimSpace(getDesktopText(app.commandEdit))
	if command == "" {
		app.setError("请输入 AT 命令。")
		return
	}
	app.runAsync(func() error {
		deviceID, err := app.firstDeviceID()
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"cmd": command})
		ctx, cancel := contextWithDesktopTimeout()
		defer cancel()
		data, err := app.api.request(ctx, http.MethodPost, "/api/devices/"+url.PathEscape(deviceID)+"/actions/at", payload)
		if err != nil {
			return err
		}
		var result struct {
			Response string `json:"response"`
			Final    string `json:"final"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		app.mu.Lock()
		app.output = strings.TrimSpace(result.Response + "\n" + result.Final)
		app.notice = "AT 命令已执行。"
		app.mu.Unlock()
		return nil
	})
}

func (app *nativeWorkspace) scheduleDial() {
	number := strings.TrimSpace(getDesktopText(app.dialEdit))
	if number == "" {
		app.setError("请输入电话号码。")
		return
	}
	app.runAsync(func() error {
		deviceID, err := app.firstDeviceID()
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"number": number})
		ctx, cancel := contextWithDesktopTimeout()
		defer cancel()
		_, err = app.api.request(ctx, http.MethodPost, "/api/devices/"+url.PathEscape(deviceID)+"/actions/dial", payload)
		if err == nil {
			app.mu.Lock()
			app.notice = "拨号请求已提交。"
			app.output = "号码：" + number
			app.mu.Unlock()
		}
		return err
	})
}

func (app *nativeWorkspace) scheduleSMS() {
	phone := strings.TrimSpace(getDesktopText(app.smsPhoneEdit))
	message := strings.TrimSpace(getDesktopText(app.smsMessageEdit))
	if phone == "" || message == "" {
		app.setError("请输入收件人和短信内容。")
		return
	}
	app.runAsync(func() error {
		deviceID, err := app.firstDeviceID()
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"phone": phone, "message": message, "device_id": deviceID})
		ctx, cancel := contextWithDesktopTimeout()
		defer cancel()
		_, err = app.api.request(ctx, http.MethodPost, "/api/sms/send", payload)
		if err == nil {
			app.mu.Lock()
			app.notice = "短信发送请求已提交。"
			app.output = "收件人：" + phone + "\n内容：" + message
			app.mu.Unlock()
		}
		return err
	})
}

func (app *nativeWorkspace) firstDeviceID() (string, error) {
	app.mu.RLock()
	defer app.mu.RUnlock()
	if app.api == nil || len(app.snapshot.Devices) == 0 {
		return "", errors.New("没有可用的已配置设备")
	}
	deviceID := strings.TrimSpace(app.snapshot.Devices[0].ID)
	if deviceID == "" {
		return "", errors.New("当前设备缺少有效 ID")
	}
	return deviceID, nil
}

func (app *nativeWorkspace) runAsync(task func() error) {
	app.mu.Lock()
	if app.busy || app.closing {
		app.mu.Unlock()
		return
	}
	app.busy = true
	app.lastError = ""
	app.notice = "正在处理，请稍候……"
	app.mu.Unlock()
	app.nativeInvalidate()
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
			procPostMessageW.Call(hwnd, nativeWMAppUpdate, 0, 0)
		}
	}()
}

func (app *nativeWorkspace) setError(message string) {
	app.mu.Lock()
	app.lastError = message
	app.notice = "操作未完成。"
	app.mu.Unlock()
	app.nativeInvalidate()
}

func (app *nativeWorkspace) nativeInvalidate() {
	if app.hwnd != 0 {
		nativeInvalidateRect.Call(app.hwnd, 0, 1)
	}
}

func nativeWorkspaceWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	app := activeNativeWorkspace
	if app == nil {
		result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return result
	}
	switch message {
	case nativeWMCreate:
		app.hwnd = hwnd
		if err := app.createControls(hwnd); err != nil {
			app.setError(err.Error())
			procPostMessageW.Call(hwnd, nativeWMClose, 0, 0)
		}
		app.layout()
		return 0
	case nativeWMPaint:
		var paint nativePaintStruct
		hdc, _, _ := nativeBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		app.paint(hdc)
		nativeEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		return 0
	case nativeWMSize:
		if wParam == nativeSizeMinimized && app.trayIconAdded {
			procShowWindow.Call(hwnd, nativeSWHide)
			return 0
		}
		app.layout()
		app.nativeInvalidate()
		return 0
	case nativeWMCommand:
		if uint16(wParam>>16) == bnClicked {
			switch uintptr(uint16(wParam)) {
			case nativeIDLogin:
				app.scheduleLogin()
			case nativeIDCommandRun:
				app.scheduleAT()
			case nativeIDDialRun:
				app.scheduleDial()
			case nativeIDSMSSend:
				app.scheduleSMS()
			case nativeIDTrayOpen:
				app.restoreFromTray()
			case nativeIDTrayExit:
				procDestroyWindow.Call(hwnd)
			}
		}
		return 0
	case nativeWMTray:
		switch uint32(lParam) {
		case nativeWMRButtonUp:
			app.showTrayMenu()
		case nativeWMDblClick:
			app.restoreFromTray()
		}
		return 0
	case nativeWMLButtonUp:
		x := int32(int16(uint16(lParam)))
		y := int32(int16(uint16(lParam >> 16)))
		app.onClick(x, y)
		return 0
	case nativeWMKeyDown:
		if wParam == uintptr('K') || wParam == uintptr('k') {
			// Ctrl+K focuses the current native page's action field, matching the
			// desktop shortcut without opening a command palette in a browser.
			if (uint32(lParam)>>29)&1 == 1 {
				app.mu.RLock()
				page := app.page
				app.mu.RUnlock()
				switch page {
				case nativePageAT:
					nativeSetFocus.Call(app.commandEdit)
				case nativePagePhone:
					nativeSetFocus.Call(app.dialEdit)
				case nativePageSMS:
					nativeSetFocus.Call(app.smsMessageEdit)
				}
			}
		}
		return 0
	case nativeWMTimer:
		if wParam == nativeTimerID {
			app.mu.RLock()
			authenticated := app.authenticated
			app.mu.RUnlock()
			if authenticated {
				app.schedulePageRefresh()
			}
		}
		return 0
	case nativeWMAppUpdate:
		app.layout()
		app.nativeInvalidate()
		return 0
	case nativeWMCTLCOLOR:
		hdc := wParam
		nativeSetTextColor.Call(hdc, uintptr(nativeRGB(234, 239, 245)))
		nativeSetBkColor.Call(hdc, uintptr(nativeRGB(30, 36, 45)))
		return app.editBrush
	case nativeWMCTLCOLOR2:
		hdc := wParam
		nativeSetTextColor.Call(hdc, uintptr(nativeRGB(234, 239, 245)))
		nativeSetBkColor.Call(hdc, uintptr(nativeRGB(15, 18, 24)))
		return app.editBrush
	case nativeWMClose:
		app.removeNativeTrayIcon()
		procDestroyWindow.Call(hwnd)
		return 0
	case nativeWMDestroy:
		procKillTimer.Call(hwnd, nativeTimerID)
		procPostQuitMessage.Call(0)
		return 0
	case nativeWMNCDestroy:
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func contextWithDesktopTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func startNativeBackendIfNeeded(baseURL string) (*exec.Cmd, error) {
	if nativeHealthProbe(baseURL) == nil {
		return nil, nil
	}
	path, err := nativeBackendPath()
	if err != nil {
		return nil, err
	}
	command := exec.Command(path, "serve")
	command.Dir = filepath.Dir(path)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.Env = nativeBackendEnvironment(baseURL)
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("启动 Halo Windows 核心引擎失败: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if nativeHealthProbe(baseURL) == nil {
			return command, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	stopNativeBackend(command)
	return nil, fmt.Errorf("Halo 核心引擎在 30 秒内没有响应")
}

func nativeBackendPath() (string, error) {
	candidates := []string{}
	if value := strings.TrimSpace(os.Getenv("VOCAT_DESKTOP_BACKEND")); value != "" {
		candidates = append(candidates, value)
	}
	if executable, err := os.Executable(); err == nil {
		folder := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(folder, "halo-windows-amd64.exe"), filepath.Join(folder, "..", "dist", "halo-windows-amd64.exe"))
	}
	candidates = append(candidates, filepath.Join("dist", "halo-windows-amd64.exe"))
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return filepath.Abs(candidate)
		}
	}
	return "", errors.New("找不到 Halo Windows 核心引擎 halo-windows-amd64.exe；请把它放在原生客户端旁边")
}

func nativeBackendEnvironment(baseURL string) []string {
	env := os.Environ()
	parsed, err := url.Parse(baseURL)
	address := "127.0.0.1:7575"
	if err == nil && parsed.Host != "" {
		address = parsed.Host
	}
	env = append(env, "VOCAT_ADDR="+address)
	if strings.TrimSpace(os.Getenv("VOCAT_DATABASE_PATH")) == "" {
		if root, err := os.UserConfigDir(); err == nil {
			dataDir := filepath.Join(root, "Halo", "data")
			if os.MkdirAll(dataDir, 0700) == nil {
				env = append(env, "VOCAT_DATABASE_PATH="+filepath.Join(dataDir, "vocat.db"))
			}
		}
	}
	return env
}

func nativeHealthProbe(baseURL string) error {
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 700 * time.Millisecond}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned HTTP %d", response.StatusCode)
	}
	return nil
}

func stopNativeBackend(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
}
