//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// XNC Ocean v1.0.8 native Win32 host with an embedded Microsoft Edge WebView2.
// The app's existing local HTTP API remains unchanged; only the UI host changed.

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	pRegisterClassExW      = user32.NewProc("RegisterClassExW")
	pCreateWindowExW       = user32.NewProc("CreateWindowExW")
	pDefWindowProcW        = user32.NewProc("DefWindowProcW")
	pDestroyWindow         = user32.NewProc("DestroyWindow")
	pShowWindow            = user32.NewProc("ShowWindow")
	pUpdateWindow          = user32.NewProc("UpdateWindow")
	pGetMessageW           = user32.NewProc("GetMessageW")
	pTranslateMessage      = user32.NewProc("TranslateMessage")
	pDispatchMessageW      = user32.NewProc("DispatchMessageW")
	pPostQuitMessage       = user32.NewProc("PostQuitMessage")
	pGetClientRect         = user32.NewProc("GetClientRect")
	pLoadCursorW           = user32.NewProc("LoadCursorW")
	pSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	pMessageBoxW           = user32.NewProc("MessageBoxW")

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	pLoadLibraryExW   = kernel32.NewProc("LoadLibraryExW")
	pLoadLibraryW     = kernel32.NewProc("LoadLibraryW")
	pSetDllDirectoryW = kernel32.NewProc("SetDllDirectoryW")
	pGetProcAddress   = kernel32.NewProc("GetProcAddress")

	pCoInitializeEx = ole32.NewProc("CoInitializeEx")
	pCoUninitialize = ole32.NewProc("CoUninitialize")
)

const (
	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsOverlappedWindow = 0x00CF0000
	wsClipChildren     = 0x02000000

	cwUseDefault = 0x80000000
	swShow       = 5

	wmDestroy = 0x0002
	wmSize    = 0x0005
	wmClose   = 0x0010

	colorWindow = 5
	idcArrow    = 32512

	spiGetWorkArea = 0x0030

	coinitApartmentThreaded = 0x2

	loadLibrarySearchDllLoadDir  = 0x00000100
	loadLibrarySearchDefaultDirs = 0x00001000
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

type point struct{ X, Y int32 }

type msg struct {
	HWnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type webViewHost struct {
	hwnd       uintptr
	module     uintptr
	controller uintptr
	core       uintptr
	url        string
	initErr    error
	shown      bool
}

var activeHost *webViewHost

func hiddenProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{HideWindow: true} }

func setDPIAware() {
	p := user32.NewProc("SetProcessDpiAwarenessContext")
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	_, _, _ = p.Call(^uintptr(3))
}

func fatalDialog(text string) {
	title, _ := syscall.UTF16PtrFromString("XNC Ocean")
	message, _ := syscall.UTF16PtrFromString(text)
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
}

func runAppWindow(url string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := pCoInitializeEx.Call(0, coinitApartmentThreaded)
	// S_OK, S_FALSE, or RPC_E_CHANGED_MODE (already initialized differently).
	if int32(hr) < 0 && uint32(hr) != 0x80010106 {
		return fmt.Errorf("không khởi tạo được COM: 0x%08X", uint32(hr))
	}
	if uint32(hr) != 0x80010106 {
		defer pCoUninitialize.Call()
	}

	host := &webViewHost{url: url}
	activeHost = host
	defer func() { activeHost = nil }()

	if err := host.createWindow(); err != nil {
		return err
	}
	if err := host.startWebView2(); err != nil {
		pDestroyWindow.Call(host.hwnd)
		return err
	}

	var m msg
	for {
		r, _, e := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			return fmt.Errorf("lỗi vòng lặp cửa sổ: %v", e)
		}
		if r == 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return host.initErr
}

func (h *webViewHost) createWindow() error {
	instance, _, _ := pGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("XNCOceanWebView2Window")
	title, _ := syscall.UTF16PtrFromString("XNC - Khai báo tạm trú khách nước ngoài")
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		Style:         csHRedraw | csVRedraw,
		LpfnWndProc:   syscall.NewCallback(xncWindowProc),
		HInstance:     instance,
		HCursor:       cursor,
		HbrBackground: colorWindow + 1,
		LpszClassName: className,
	}
	atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		// ERROR_CLASS_ALREADY_EXISTS is harmless when reopening in the same process.
		if errno, ok := err.(syscall.Errno); !ok || errno != 1410 {
			return fmt.Errorf("không đăng ký được cửa sổ: %v", err)
		}
	}

	x, y, width, height := defaultWindowBounds()
	hwnd, _, createErr := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsClipChildren,
		uintptr(int32(x)), uintptr(int32(y)), uintptr(int32(width)), uintptr(int32(height)),
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("không tạo được cửa sổ: %v", createErr)
	}
	h.hwnd = hwnd
	return nil
}

func defaultWindowBounds() (int, int, int, int) {
	r := winRect{Right: 1440, Bottom: 900}
	ok, _, _ := pSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if ok == 0 {
		r = winRect{Right: 1440, Bottom: 900}
	}
	workW := int(r.Right - r.Left)
	workH := int(r.Bottom - r.Top)
	width := int(float64(workW) * 0.94)
	height := int(float64(workH) * 0.90)
	if width < 1180 && workW >= 1180 {
		width = 1180
	}
	if height < 720 && workH >= 720 {
		height = 720
	}
	if width > workW {
		width = workW
	}
	if height > workH {
		height = workH
	}
	x := int(r.Left) + (workW-width)/2
	y := int(r.Top) + (workH-height)/2
	return x, y, width, height
}

func xncWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	h := activeHost
	switch message {
	case wmSize:
		if h != nil && h.hwnd == hwnd && h.controller != 0 {
			h.resizeController()
		}
		return 0
	case wmClose:
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if h != nil {
			h.closeController()
		}
		pPostQuitMessage.Call(0)
		return 0
	default:
		r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return r
	}
}

func (h *webViewHost) startWebView2() error {
	dll, err := findWebView2RuntimeDLL()
	if err != nil {
		return err
	}
	module, proc, err := loadRuntimeEntry(dll)
	if err != nil {
		return err
	}
	h.module = module

	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "XNC Ocean", "WebView2")
	if os.Getenv("LOCALAPPDATA") == "" {
		dataDir = filepath.Join(os.TempDir(), "XNC Ocean WebView2")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("không tạo được thư mục WebView2: %w", err)
	}
	dataDirW, _ := syscall.UTF16PtrFromString(dataDir)

	initCallbackVTables()
	envHandler := &callbackObject{vtable: &envCompletedVTable[0], refs: 1, host: h}
	keepEnvHandler = envHandler

	// Signature of EmbeddedBrowserWebView.dll!CreateWebViewEnvironmentWithOptionsInternal:
	// (checkRunningInstance, runtimeType, userDataFolder, environmentOptions, completedHandler)
	result, _, callErr := syscall.SyscallN(
		proc,
		1, // match the normal WebView2Loader running-instance behavior
		0, // installed/evergreen runtime
		uintptr(unsafe.Pointer(dataDirW)),
		0,
		uintptr(unsafe.Pointer(envHandler)),
	)
	if int32(result) < 0 {
		return fmt.Errorf("WebView2 từ chối khởi tạo: 0x%08X (%v)", uint32(result), callErr)
	}
	return nil
}

func (h *webViewHost) resizeController() {
	if h.controller == 0 || h.hwnd == 0 {
		return
	}
	var r winRect
	if ok, _, _ := pGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return
	}
	// ICoreWebView2Controller::put_Bounds is vtable slot 6 (IUnknown included).
	_ = comCall(h.controller, 6, uintptr(unsafe.Pointer(&r)))
}

func (h *webViewHost) closeController() {
	if h.controller != 0 {
		// ICoreWebView2Controller::Close is slot 24.
		_ = comCall(h.controller, 24)
		comRelease(h.controller)
		h.controller = 0
	}
	if h.core != 0 {
		comRelease(h.core)
		h.core = 0
	}
}

func (h *webViewHost) show() {
	if h.shown {
		return
	}
	h.shown = true
	pShowWindow.Call(h.hwnd, swShow)
	pUpdateWindow.Call(h.hwnd)
}

func loadRuntimeEntry(dllPath string) (uintptr, uintptr, error) {
	pathW, _ := syscall.UTF16PtrFromString(dllPath)
	module, _, err := pLoadLibraryExW.Call(
		uintptr(unsafe.Pointer(pathW)), 0,
		loadLibrarySearchDllLoadDir|loadLibrarySearchDefaultDirs,
	)
	if module == 0 {
		dirW, _ := syscall.UTF16PtrFromString(filepath.Dir(dllPath))
		pSetDllDirectoryW.Call(uintptr(unsafe.Pointer(dirW)))
		module, _, err = pLoadLibraryW.Call(uintptr(unsafe.Pointer(pathW)))
		pSetDllDirectoryW.Call(0)
	}
	if module == 0 {
		return 0, 0, fmt.Errorf("không nạp được WebView2 Runtime: %v", err)
	}
	name := append([]byte("CreateWebViewEnvironmentWithOptionsInternal"), 0)
	proc, _, procErr := pGetProcAddress.Call(module, uintptr(unsafe.Pointer(&name[0])))
	if proc == 0 {
		return 0, 0, fmt.Errorf("WebView2 Runtime thiếu hàm khởi tạo: %v", procErr)
	}
	return module, proc, nil
}

func findWebView2RuntimeDLL() (string, error) {
	roots := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "Application"),
	}
	var matches []runtimeCandidate
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" || seen[strings.ToLower(root)] {
			continue
		}
		seen[strings.ToLower(root)] = true
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !looksLikeVersion(entry.Name()) {
				continue
			}
			base := filepath.Join(root, entry.Name())
			candidates := []string{
				filepath.Join(base, "EBWebView", "x64", "EmbeddedBrowserWebView.dll"),
				filepath.Join(base, "EBWebView", "EmbeddedBrowserWebView.dll"),
				filepath.Join(base, "EmbeddedBrowserWebView.dll"),
			}
			for _, candidate := range candidates {
				if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
					matches = append(matches, runtimeCandidate{version: entry.Name(), path: candidate})
					break
				}
			}
		}
	}
	if len(matches) == 0 {
		return "", errors.New("không tìm thấy Microsoft Edge WebView2 Runtime. Hãy cài WebView2 Runtime rồi mở lại XNC Ocean")
	}
	sort.Slice(matches, func(i, j int) bool { return compareVersions(matches[i].version, matches[j].version) > 0 })
	return matches[0].path, nil
}

type runtimeCandidate struct{ version, path string }

func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

func compareVersions(a, b string) int {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	n := len(aa)
	if len(bb) > n {
		n = len(bb)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(aa) {
			av, _ = strconv.Atoi(aa[i])
		}
		if i < len(bb) {
			bv, _ = strconv.Atoi(bb[i])
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// Minimal COM callbacks needed for WebView2 environment/controller creation.
type callbackObject struct {
	vtable *uintptr
	refs   uint32
	host   *webViewHost
}

var (
	envCompletedVTable        [4]uintptr
	controllerCompletedVTable [4]uintptr
	vtablesReady              atomic.Bool
	keepEnvHandler            *callbackObject
	keepControllerHandler     *callbackObject
)

func initCallbackVTables() {
	if vtablesReady.Swap(true) {
		return
	}
	envCompletedVTable = [4]uintptr{
		syscall.NewCallback(callbackQueryInterface),
		syscall.NewCallback(callbackAddRef),
		syscall.NewCallback(callbackRelease),
		syscall.NewCallback(environmentCompletedInvoke),
	}
	controllerCompletedVTable = [4]uintptr{
		syscall.NewCallback(callbackQueryInterface),
		syscall.NewCallback(callbackAddRef),
		syscall.NewCallback(callbackRelease),
		syscall.NewCallback(controllerCompletedInvoke),
	}
}

func callbackQueryInterface(this, _riid, object uintptr) uintptr {
	if object == 0 {
		return uintptr(uint32(0x80004003))
	} // E_POINTER
	*(*uintptr)(unsafe.Pointer(object)) = this
	callbackAddRef(this)
	return 0
}

func callbackAddRef(this uintptr) uintptr {
	obj := (*callbackObject)(unsafe.Pointer(this))
	return uintptr(atomic.AddUint32(&obj.refs, 1))
}

func callbackRelease(this uintptr) uintptr {
	obj := (*callbackObject)(unsafe.Pointer(this))
	for {
		old := atomic.LoadUint32(&obj.refs)
		if old == 0 {
			return 0
		}
		if atomic.CompareAndSwapUint32(&obj.refs, old, old-1) {
			return uintptr(old - 1)
		}
	}
}

func environmentCompletedInvoke(this uintptr, errorCode uintptr, environment uintptr) uintptr {
	obj := (*callbackObject)(unsafe.Pointer(this))
	h := obj.host
	if int32(errorCode) < 0 || environment == 0 {
		h.initErr = fmt.Errorf("không tạo được môi trường WebView2: 0x%08X", uint32(errorCode))
		fatalDialog(h.initErr.Error())
		pDestroyWindow.Call(h.hwnd)
		return 0
	}
	controllerHandler := &callbackObject{vtable: &controllerCompletedVTable[0], refs: 1, host: h}
	keepControllerHandler = controllerHandler
	// ICoreWebView2Environment::CreateCoreWebView2Controller is slot 3.
	result := comCall(environment, 3, h.hwnd, uintptr(unsafe.Pointer(controllerHandler)))
	if int32(result) < 0 {
		h.initErr = fmt.Errorf("không tạo được bộ điều khiển WebView2: 0x%08X", uint32(result))
		fatalDialog(h.initErr.Error())
		pDestroyWindow.Call(h.hwnd)
	}
	return 0
}

func controllerCompletedInvoke(this uintptr, errorCode uintptr, controller uintptr) uintptr {
	obj := (*callbackObject)(unsafe.Pointer(this))
	h := obj.host
	if int32(errorCode) < 0 || controller == 0 {
		h.initErr = fmt.Errorf("WebView2 không gắn được vào cửa sổ: 0x%08X", uint32(errorCode))
		fatalDialog(h.initErr.Error())
		pDestroyWindow.Call(h.hwnd)
		return 0
	}
	h.controller = controller
	comAddRef(controller)

	var core uintptr
	// ICoreWebView2Controller::get_CoreWebView2 is slot 25.
	result := comCall(controller, 25, uintptr(unsafe.Pointer(&core)))
	if int32(result) < 0 || core == 0 {
		h.initErr = fmt.Errorf("không lấy được giao diện WebView2: 0x%08X", uint32(result))
		fatalDialog(h.initErr.Error())
		pDestroyWindow.Call(h.hwnd)
		return 0
	}
	h.core = core
	h.resizeController()
	// ICoreWebView2Controller::put_IsVisible is slot 4.
	_ = comCall(controller, 4, 1)

	urlW, _ := syscall.UTF16PtrFromString(h.url)
	// ICoreWebView2::Navigate is slot 5.
	result = comCall(core, 5, uintptr(unsafe.Pointer(urlW)))
	if int32(result) < 0 {
		h.initErr = fmt.Errorf("WebView2 không mở được giao diện: 0x%08X", uint32(result))
		fatalDialog(h.initErr.Error())
		pDestroyWindow.Call(h.hwnd)
		return 0
	}
	h.show()
	return 0
}

func comMethod(object uintptr, slot int) uintptr {
	if object == 0 {
		return 0
	}
	vtable := *(*uintptr)(unsafe.Pointer(object))
	return *(*uintptr)(unsafe.Pointer(vtable + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
}

func comCall(object uintptr, slot int, args ...uintptr) uintptr {
	method := comMethod(object, slot)
	if method == 0 {
		return uintptr(uint32(0x80004003))
	}
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, object)
	callArgs = append(callArgs, args...)
	result, _, _ := syscall.SyscallN(method, callArgs...)
	return result
}

func comAddRef(object uintptr) {
	if object != 0 {
		_ = comCall(object, 1)
	}
}
func comRelease(object uintptr) {
	if object != 0 {
		_ = comCall(object, 2)
	}
}
