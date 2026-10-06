//go:build windows

package main

// Native Windows shell: a real top-level window (own taskbar icon) hosting
// the chat UI in WebView2, plus a system-tray icon with Open/Quit and
// pop-up notifications. Closing the window hides it to the tray so messages
// and files keep arriving. Falls back to an Edge app window if the WebView2
// runtime is missing (very old Windows 10).

import (
	"encoding/binary"
	"strconv"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const nativeUI = true

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pPostQuitMessage               = user32.NewProc("PostQuitMessage")
	pPostMessageW                  = user32.NewProc("PostMessageW")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	pLoadCursorW                   = user32.NewProc("LoadCursorW")
	pCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                   = user32.NewProc("AppendMenuW")
	pTrackPopupMenu                = user32.NewProc("TrackPopupMenu")
	pDestroyMenu                   = user32.NewProc("DestroyMenu")
	pGetCursorPos                  = user32.NewProc("GetCursorPos")
	pFlashWindowEx                 = user32.NewProc("FlashWindowEx")
	pIsWindowVisible               = user32.NewProc("IsWindowVisible")
	pIsIconic                      = user32.NewProc("IsIconic")
	pGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pRegisterWindowMessageW        = user32.NewProc("RegisterWindowMessageW")
	pCreateIconFromResourceEx      = user32.NewProc("CreateIconFromResourceEx")
	pSetWindowTextW                = user32.NewProc("SetWindowTextW")
	pDestroyWindow                 = user32.NewProc("DestroyWindow")
	pShellNotifyIconW              = shell32.NewProc("Shell_NotifyIconW")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pDwmSetWindowAttribute         = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	pGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmDestroy       = 0x0002
	wmMove          = 0x0003
	wmSize          = 0x0005
	wmActivate      = 0x0006
	wmClose         = 0x0010
	wmNull          = 0x0000
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmUser          = 0x0400
	wmApp           = 0x8000
	wmTray          = wmApp + 1
	wmCall          = wmApp + 2
	ninBalloonClick = wmUser + 5

	swHide        = 0
	swShow        = 5
	swRestore     = 9
	sizeMinimized = 1

	wsOverlappedWindow = 0x00CF0000
	cwUseDefault       = 0x80000000

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nifMessage    = 0x1
	nifIcon       = 0x2
	nifTip        = 0x4
	nifInfo       = 0x10
	niifUser      = 0x4
	niifNoSound   = 0x10
	niifLargeIcon = 0x20

	menuOpen = 1
	menuQuit = 2
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type flashInfo struct {
	CbSize    uint32
	Hwnd      uintptr
	DwFlags   uint32
	UCount    uint32
	DwTimeout uint32
}

var (
	mainHwnd       uintptr
	webview        *edge.Chromium
	iconSmall      uintptr
	iconBig        uintptr
	taskbarCreated uintptr
	trayHinted     bool
	pendingChat    string // chat to open when a balloon is clicked

	callMu sync.Mutex
	calls  []func()

	visMu   sync.Mutex
	visible bool
)

// onUI runs fn on the window thread (WebView2 may only be used there).
func onUI(fn func()) {
	callMu.Lock()
	calls = append(calls, fn)
	callMu.Unlock()
	if mainHwnd != 0 {
		pPostMessageW.Call(mainHwnd, wmCall, 0, 0)
	}
}

func wstr(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// iconFromICO builds an HICON of the requested size from the embedded .ico
// (whose entries are PNGs), so no resource IDs are needed.
func iconFromICO(size int) uintptr {
	ico, err := webFS.ReadFile("web/icon.ico")
	if err != nil || len(ico) < 6 {
		return 0
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	best, bestW := -1, 0
	for i := 0; i < n; i++ {
		e := ico[6+16*i:]
		w := int(e[0])
		if w == 0 {
			w = 256
		}
		if best < 0 || (w >= size && (bestW < size || w < bestW)) || (bestW < size && w > bestW) {
			best, bestW = i, w
		}
	}
	e := ico[6+16*best:]
	ln := binary.LittleEndian.Uint32(e[8:])
	off := binary.LittleEndian.Uint32(e[12:])
	data := ico[off : off+ln]
	h, _, _ := pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
	return h
}

func setVisible(v bool) {
	visMu.Lock()
	changed := visible != v
	visible = v
	visMu.Unlock()
	if changed && webview != nil {
		webview.Eval("window.ocNative && ocNative.setVisible(" + strconv.FormatBool(v) + ")")
	}
}

func trayIcon(op uintptr, fill func(*notifyIconData)) {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = mainHwnd
	nid.UID = 1
	if fill != nil {
		fill(&nid)
	}
	pShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid)))
}

func addTray() {
	trayIcon(nimAdd, func(n *notifyIconData) {
		n.UFlags = nifMessage | nifIcon | nifTip
		n.UCallbackMessage = wmTray
		n.HIcon = iconSmall
		copyUTF16(n.SzTip[:], appName)
	})
}

func balloon(title, body string) {
	trayIcon(nimModify, func(n *notifyIconData) {
		n.UFlags = nifInfo
		copyUTF16(n.SzInfoTitle[:], title)
		copyUTF16(n.SzInfo[:], body)
		n.DwInfoFlags = niifUser | niifLargeIcon | niifNoSound // the page plays its own sound
		n.HBalloonIcon = iconBig
	})
}

func showMain() {
	if r, _, _ := pIsIconic.Call(mainHwnd); r != 0 {
		pShowWindow.Call(mainHwnd, swRestore)
	} else {
		pShowWindow.Call(mainHwnd, swShow)
	}
	pSetForegroundWindow.Call(mainHwnd)
	if webview != nil {
		webview.Focus()
	} else {
		openAppWindow(uiURL()) // no WebView2 runtime
	}
	setVisible(true)
}

func trayMenu() {
	menu, _, _ := pCreatePopupMenu.Call()
	pAppendMenuW.Call(menu, 0, menuOpen, uintptr(unsafe.Pointer(wstr("Open OfficeChat"))))
	pAppendMenuW.Call(menu, 0x800, 0, 0) // separator
	pAppendMenuW.Call(menu, 0, menuQuit, uintptr(unsafe.Pointer(wstr("Quit OfficeChat"))))
	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(mainHwnd)
	cmd, _, _ := pTrackPopupMenu.Call(menu, 0x100|0x2, uintptr(pt.X), uintptr(pt.Y), 0, mainHwnd, 0) // RETURNCMD|RIGHTBUTTON
	pPostMessageW.Call(mainHwnd, wmNull, 0, 0)
	pDestroyMenu.Call(menu)
	switch cmd {
	case menuOpen:
		showMain()
	case menuQuit:
		requestQuit()
	}
}

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmSize:
		if webview != nil {
			webview.Resize()
		}
		if wparam == sizeMinimized {
			setVisible(false)
		} else if r, _, _ := pIsWindowVisible.Call(hwnd); r != 0 {
			setVisible(true)
		}
		return 0
	case wmMove:
		if webview != nil {
			webview.NotifyParentWindowPositionChanged()
		}
	case wmActivate:
		if wparam&0xFFFF != 0 {
			setVisible(true)
			if webview != nil {
				webview.Focus()
			}
		}
	case wmClose:
		// Keep running in the tray, like Teams/Slack.
		pShowWindow.Call(hwnd, swHide)
		setVisible(false)
		if !trayHinted {
			trayHinted = true
			balloon(appName+" is still running", "You'll keep getting messages and files. Right-click the tray icon to quit.")
		}
		return 0
	case wmDestroy:
		trayIcon(nimDelete, nil)
		pPostQuitMessage.Call(0)
		return 0
	case wmTray:
		switch lparam & 0xFFFF {
		case wmLButtonUp, wmLButtonDblClk:
			showMain()
		case wmRButtonUp:
			trayMenu()
		case ninBalloonClick:
			showMain()
			if pendingChat != "" && webview != nil {
				webview.Eval("window.ocNative && ocNative.openChat(" + strconv.Quote(pendingChat) + ")")
			}
		}
		return 0
	case wmCall:
		callMu.Lock()
		list := calls
		calls = nil
		callMu.Unlock()
		for _, fn := range list {
			fn()
		}
		return 0
	}
	if taskbarCreated != 0 && msg == taskbarCreated {
		addTray() // Explorer restarted
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func runUI(url string, hidden bool) {
	pSetProcessDpiAwarenessContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4); ignored on old Windows
	inst, _, _ := pGetModuleHandleW.Call(0)
	iconSmall = iconFromICO(32)
	iconBig = iconFromICO(256)
	cursor, _, _ := pLoadCursorW.Call(0, 32512) // IDC_ARROW
	className := wstr("OfficeChatWindow")
	wc := wndClassEx{
		WndProc:    windows.NewCallback(wndProc),
		Instance:   inst,
		Icon:       iconBig,
		IconSm:     iconSmall,
		Cursor:     cursor,
		Background: 6, // COLOR_WINDOW+1 (white)
		ClassName:  className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	taskbarCreated, _, _ = pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(wstr("TaskbarCreated"))))

	dpi := uintptr(96)
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d != 0 {
			dpi = d
		}
	}
	w, h := 1180*dpi/96, 780*dpi/96
	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	x, y := uintptr(cwUseDefault), uintptr(cwUseDefault)
	if sw > w && sh > h {
		x, y = (sw-w)/2, (sh-h)/2
	}
	mainHwnd, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(wstr(appName))),
		wsOverlappedWindow, x, y, w, h, 0, 0, inst, 0)
	addTray()

	if _, err := webviewloader.GetInstalledVersion(); err == nil {
		wv := edge.NewChromium()
		wv.DataPath = dataDir + `\WebView2`
		wv.MessageCallback = handleNativeMessage
		wv.SetPermission(edge.CoreWebView2PermissionKindNotifications, edge.CoreWebView2PermissionStateDeny)
		wv.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)
		if wv.Embed(mainHwnd) {
			webview = wv
			if s, err := wv.GetSettings(); err == nil {
				_ = s.PutAreDevToolsEnabled(false)
				_ = s.PutIsStatusBarEnabled(false)
				_ = s.PutIsZoomControlEnabled(false)
			}
			wv.Resize()
			wv.Navigate(url)
		} else {
			logger.Printf("WebView2 failed to start; using Edge window instead")
		}
	} else {
		logger.Printf("WebView2 runtime not installed (%v); using Edge window instead", err)
	}

	if !hidden {
		showMain()
	}

	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func uiShow() { onUI(showMain) }

func uiQuit() { onUI(func() { pDestroyWindow.Call(mainHwnd) }) }

func uiVisible() bool {
	visMu.Lock()
	defer visMu.Unlock()
	return visible
}

func uiEval(js string) {
	onUI(func() {
		if webview != nil {
			webview.Eval(js)
		}
	})
}

func uiBadge(n int) {
	onUI(func() {
		title, tip := appName, appName
		if n > 0 {
			title = "(" + strconv.Itoa(n) + ") " + appName
			tip = appName + " – " + strconv.Itoa(n) + " unread"
		}
		pSetWindowTextW.Call(mainHwnd, uintptr(unsafe.Pointer(wstr(title))))
		trayIcon(nimModify, func(nid *notifyIconData) {
			nid.UFlags = nifTip
			copyUTF16(nid.SzTip[:], tip)
		})
	})
}

// uiTheme gives the window a dark or light title bar to match the page
// (Windows 10 20H1+ / Windows 11; older versions ignore it).
func uiTheme(mode string, dark bool) {
	onUI(func() {
		if pDwmSetWindowAttribute.Find() != nil {
			return
		}
		v := int32(0)
		if dark {
			v = 1
		}
		for _, attr := range []uintptr{20, 19} { // DWMWA_USE_IMMERSIVE_DARK_MODE (new, then pre-20H1 id)
			if r, _, _ := pDwmSetWindowAttribute.Call(mainHwnd, attr, uintptr(unsafe.Pointer(&v)), 4); r == 0 {
				break
			}
		}
		// Redraw the frame so the change shows immediately.
		pSetWindowPos.Call(mainHwnd, 0, 0, 0, 0, 0, 0x1|0x2|0x4|0x20) // NOSIZE|NOMOVE|NOZORDER|FRAMECHANGED
	})
}

func uiNotify(title, body, chat string) {
	onUI(func() {
		pendingChat = chat
		balloon(title, body)
		if fg, _, _ := pGetForegroundWindow.Call(); fg != mainHwnd {
			fi := flashInfo{Hwnd: mainHwnd, DwFlags: 0x2 | 0xC} // FLASHW_TRAY | FLASHW_TIMERNOFG
			fi.CbSize = uint32(unsafe.Sizeof(fi))
			pFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
		}
	})
}
