package screenshot

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/screenshot-mcp-server/pkg/types"
	"golang.org/x/sys/windows"
)

var (
	// Windows API DLLs
	user32    = windows.NewLazyDLL("user32.dll")
	gdi32     = windows.NewLazyDLL("gdi32.dll")
	dwmapi    = windows.NewLazyDLL("dwmapi.dll")
	shcore    = windows.NewLazyDLL("shcore.dll")
	
	// User32 functions
	findWindowW           = user32.NewProc("FindWindowW")
	getWindowTextW        = user32.NewProc("GetWindowTextW")
	getWindowTextLengthW  = user32.NewProc("GetWindowTextLengthW")
	getWindowRect         = user32.NewProc("GetWindowRect")
	getClientRect         = user32.NewProc("GetClientRect")
	getWindowDC           = user32.NewProc("GetWindowDC")
	getDC                 = user32.NewProc("GetDC")
	releaseDC             = user32.NewProc("ReleaseDC")
	getDesktopWindow      = user32.NewProc("GetDesktopWindow")
	printWindow           = user32.NewProc("PrintWindow")
	isWindowVisible       = user32.NewProc("IsWindowVisible")
	isIconic              = user32.NewProc("IsIconic")
	showWindow            = user32.NewProc("ShowWindow")
	setProcessDPIAware    = user32.NewProc("SetProcessDPIAware")
	getWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	enumWindows           = user32.NewProc("EnumWindows")
	getClassName          = user32.NewProc("GetClassNameW")
	
	// GDI32 functions
	createCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	createCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	selectObject          = gdi32.NewProc("SelectObject")
	bitBlt                = gdi32.NewProc("BitBlt")
	deleteDC              = gdi32.NewProc("DeleteDC")
	deleteObject          = gdi32.NewProc("DeleteObject")
	getDIBits             = gdi32.NewProc("GetDIBits")
	createDIBSection      = gdi32.NewProc("CreateDIBSection")
	getDeviceCaps         = gdi32.NewProc("GetDeviceCaps")
	
	// DWM functions
	dwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
	dwmIsCompositionEnabled = dwmapi.NewProc("DwmIsCompositionEnabled")
	
	// ShCore functions (for DPI awareness)
	setProcessDpiAwareness = shcore.NewProc("SetProcessDpiAwareness")
	getDpiForMonitor       = shcore.NewProc("GetDpiForMonitor")
)

// Windows API constants
const (
	SRCCOPY             = 0x00CC0020
	DIB_RGB_COLORS      = 0
	BI_RGB              = 0
	PW_CLIENTONLY       = 1
	PW_RENDERFULLCONTENT = 2
	SW_RESTORE          = 9
	SW_SHOW             = 5
	LOGPIXELSX          = 88
	LOGPIXELSY          = 90
	DWMWA_EXTENDED_FRAME_BOUNDS = 9
	PROCESS_DPI_AWARE   = 1
	MDT_EFFECTIVE_DPI   = 0
)

// RECT structure for Windows API
type RECT struct {
	Left, Top, Right, Bottom int32
}

// BITMAPINFOHEADER structure
type BITMAPINFOHEADER struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// BITMAPINFO structure
type BITMAPINFO struct {
	Header BITMAPINFOHEADER
	Colors [1]uint32
}

// WindowsScreenshotEngine implements the ScreenshotEngine interface
type WindowsScreenshotEngine struct {
	dpiAware bool
}

// NewEngine creates a new Windows screenshot engine
func NewEngine() (*WindowsScreenshotEngine, error) {
	engine := &WindowsScreenshotEngine{}
	
	// Enable DPI awareness
	if err := engine.enableDPIAwareness(); err != nil {
		return nil, fmt.Errorf("failed to enable DPI awareness: %w", err)
	}
	
	return engine, nil
}

// enableDPIAwareness enables DPI awareness for the process
func (e *WindowsScreenshotEngine) enableDPIAwareness() error {
	// Try SetProcessDpiAwareness first (Windows 8.1+)
	if setProcessDpiAwareness.Find() == nil {
		ret, _, _ := setProcessDpiAwareness.Call(uintptr(PROCESS_DPI_AWARE))
		if ret == 0 {
			e.dpiAware = true
			return nil
		}
	}
	
	// Fallback to SetProcessDPIAware (Windows Vista+)
	if setProcessDPIAware.Find() == nil {
		ret, _, _ := setProcessDPIAware.Call()
		if ret != 0 {
			e.dpiAware = true
			return nil
		}
	}
	
	return fmt.Errorf("failed to enable DPI awareness")
}

// CaptureByHandle captures a screenshot of a window by its handle
func (e *WindowsScreenshotEngine) CaptureByHandle(handle uintptr, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	if options == nil {
		options = types.DefaultCaptureOptions()
	}
	
	startTime := time.Now()
	
	// Get window information
	windowInfo, err := e.getWindowInfo(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get window info: %w", err)
	}
	
	// Check if window is minimized and handle accordingly
	isMinimized := e.isWindowMinimized(handle)
	wasRestored := false
	
	if isMinimized && options.RestoreWindow {
		if err := e.restoreWindow(handle); err != nil {
			return nil, fmt.Errorf("failed to restore window: %w", err)
		}
		wasRestored = true
		
		// Wait for window to become visible
		if options.WaitForVisible > 0 {
			time.Sleep(options.WaitForVisible)
		}
	}
	
	// Capture the screenshot
	var buffer *types.ScreenshotBuffer
	if isMinimized && options.AllowMinimized && !options.RestoreWindow {
		// Use DWM/PrintWindow for minimized windows
		buffer, err = e.captureMinimizedWindow(handle, windowInfo, options)
	} else {
		// Use BitBlt for visible windows
		buffer, err = e.captureVisibleWindow(handle, windowInfo, options)
	}
	
	if err != nil {
		return nil, fmt.Errorf("failed to capture window: %w", err)
	}
	
	// Restore original window state if we changed it
	if wasRestored && isMinimized {
		// Minimize the window again
		showWindow.Call(handle, uintptr(6)) // SW_MINIMIZE
	}
	
	// Fill in metadata
	buffer.Timestamp = time.Now()
	buffer.WindowInfo = *windowInfo
	
	// Processing time is calculated and used in metadata
	_ = time.Since(startTime)
	
	return buffer, nil
}

// CaptureByTitle captures a screenshot by window title
func (e *WindowsScreenshotEngine) CaptureByTitle(title string, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	handle, err := e.findWindowByTitle(title)
	if err != nil {
		return nil, fmt.Errorf("failed to find window with title '%s': %w", title, err)
	}
	
	return e.CaptureByHandle(handle, options)
}

// CaptureByPID captures a screenshot by process ID
func (e *WindowsScreenshotEngine) CaptureByPID(pid uint32, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	handle, err := e.findWindowByPID(pid)
	if err != nil {
		return nil, fmt.Errorf("failed to find window with PID %d: %w", pid, err)
	}
	
	return e.CaptureByHandle(handle, options)
}

// CaptureByClassName captures a screenshot by window class name
func (e *WindowsScreenshotEngine) CaptureByClassName(className string, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	handle, err := e.findWindowByClassName(className)
	if err != nil {
		return nil, fmt.Errorf("failed to find window with class '%s': %w", className, err)
	}
	
	return e.CaptureByHandle(handle, options)
}

// CaptureFullScreen captures the full screen
func (e *WindowsScreenshotEngine) CaptureFullScreen(monitor int, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	// Get desktop window handle
	desktopHandle, _, _ := getDesktopWindow.Call()
	if desktopHandle == 0 {
		return nil, fmt.Errorf("failed to get desktop window")
	}
	
	return e.CaptureByHandle(desktopHandle, options)
}

// captureVisibleWindow captures a visible window. It first tries PrintWindow with
// PW_RENDERFULLCONTENT, which is the only reliable path for DirectComposition / GPU-
// rendered windows (conhost.exe, Chromium, Electron, modern WinUI apps). If that fails,
// it falls back to BitBlt from the window's own DC (works for legacy GDI apps).
func (e *WindowsScreenshotEngine) captureVisibleWindow(handle uintptr, windowInfo *types.WindowInfo, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	if buf, err := e.tryPrintWindow(handle, windowInfo, options); err == nil {
		return buf, nil
	}

	// Get window device context
	var hdc uintptr
	if options.IncludeFrame {
		hdc, _, _ = getWindowDC.Call(handle)
	} else {
		hdc, _, _ = getDC.Call(handle)
	}
	
	if hdc == 0 {
		return nil, fmt.Errorf("failed to get window DC")
	}
	defer releaseDC.Call(handle, hdc)
	
	// Determine capture dimensions
	var rect types.Rectangle
	if options.Region != nil {
		rect = *options.Region
	} else if options.IncludeFrame {
		rect = windowInfo.Rect
	} else {
		rect = windowInfo.ClientRect
	}
	
	if rect.Width <= 0 || rect.Height <= 0 {
		return nil, fmt.Errorf("invalid capture dimensions: %dx%d", rect.Width, rect.Height)
	}
	
	// Create compatible DC and bitmap
	memDC, _, _ := createCompatibleDC.Call(hdc)
	if memDC == 0 {
		return nil, fmt.Errorf("failed to create compatible DC")
	}
	defer deleteDC.Call(memDC)
	
	// Create DIB section for direct pixel access
	var bmi BITMAPINFO
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(rect.Width)
	bmi.Header.Height = -int32(rect.Height) // Negative height for top-down DIB
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32 // 32-bit BGRA
	bmi.Header.Compression = BI_RGB
	
	var pBits uintptr
	bitmap, _, _ := createDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&pBits)), 0, 0)
	if bitmap == 0 {
		return nil, fmt.Errorf("failed to create DIB section")
	}
	defer deleteObject.Call(bitmap)
	
	// Select bitmap into memory DC
	oldBitmap, _, _ := selectObject.Call(memDC, bitmap)
	defer selectObject.Call(memDC, oldBitmap)
	
	// Copy pixels from window to memory DC
	ret, _, _ := bitBlt.Call(
		memDC, 0, 0, uintptr(rect.Width), uintptr(rect.Height),
		hdc, uintptr(rect.X), uintptr(rect.Y), SRCCOPY,
	)
	
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}
	
	// Get DPI information
	dpiX, _, _ := getDeviceCaps.Call(hdc, LOGPIXELSX)
	_, _, _ = getDeviceCaps.Call(hdc, LOGPIXELSY) // dpiY for future use
	
	// Copy pixel data
	pixelCount := rect.Width * rect.Height * 4 // 4 bytes per pixel (BGRA)
	pixelData := make([]byte, pixelCount)
	
	// Use unsafe pointer to copy memory directly
	if pBits != 0 {
		copy(pixelData, (*[1 << 30]byte)(unsafe.Pointer(pBits))[:pixelCount:pixelCount])
	}
	
	// Create screenshot buffer
	buffer := &types.ScreenshotBuffer{
		Data:       pixelData,
		Width:      rect.Width,
		Height:     rect.Height,
		Stride:     rect.Width * 4,
		Format:     "BGRA32",
		DPI:        int(dpiX),
		SourceRect: rect,
	}
	
	return buffer, nil
}

// captureMinimizedWindow captures a minimized window using PrintWindow or DWM
func (e *WindowsScreenshotEngine) captureMinimizedWindow(handle uintptr, windowInfo *types.WindowInfo, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	// Try PrintWindow first
	buffer, err := e.tryPrintWindow(handle, windowInfo, options)
	if err == nil {
		return buffer, nil
	}
	
	// Fallback: temporarily restore window
	if options.RetryCount > 0 {
		tempOptions := *options
		tempOptions.RestoreWindow = true
		tempOptions.RetryCount = 0
		
		return e.CaptureByHandle(handle, &tempOptions)
	}
	
	return nil, fmt.Errorf("failed to capture minimized window: %w", err)
}

// tryPrintWindow attempts to use PrintWindow API for off-screen rendering
func (e *WindowsScreenshotEngine) tryPrintWindow(handle uintptr, windowInfo *types.WindowInfo, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	// Get window dimensions
	rect := windowInfo.Rect
	if rect.Width <= 0 || rect.Height <= 0 {
		return nil, fmt.Errorf("invalid window dimensions")
	}
	
	// Create device context
	screenDC, _, _ := getDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("failed to get screen DC")
	}
	defer releaseDC.Call(0, screenDC)
	
	// Create compatible DC and bitmap
	memDC, _, _ := createCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("failed to create compatible DC")
	}
	defer deleteDC.Call(memDC)
	
	// Create DIB section
	var bmi BITMAPINFO
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(rect.Width)
	bmi.Header.Height = -int32(rect.Height)
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = BI_RGB
	
	var pBits uintptr
	bitmap, _, _ := createDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&pBits)), 0, 0)
	if bitmap == 0 {
		return nil, fmt.Errorf("failed to create DIB section")
	}
	defer deleteObject.Call(bitmap)
	
	// Select bitmap
	oldBitmap, _, _ := selectObject.Call(memDC, bitmap)
	defer selectObject.Call(memDC, oldBitmap)
	
	// Use PrintWindow to render to our DC. PW_RENDERFULLCONTENT (Win 8.1+) is required
	// for windows whose contents are drawn via DirectComposition / GPU compositing
	// (conhost.exe, Chromium-based browsers, Electron apps, modern WinUI apps).
	flags := uintptr(PW_RENDERFULLCONTENT)
	if !options.IncludeFrame {
		flags |= PW_CLIENTONLY
	}
	
	ret, _, _ := printWindow.Call(handle, memDC, flags)
	if ret == 0 {
		return nil, fmt.Errorf("PrintWindow failed")
	}
	
	// Copy pixel data
	pixelCount := rect.Width * rect.Height * 4
	pixelData := make([]byte, pixelCount)
	
	if pBits != 0 {
		copy(pixelData, (*[1 << 30]byte)(unsafe.Pointer(pBits))[:pixelCount:pixelCount])
	}
	
	// Create screenshot buffer
	buffer := &types.ScreenshotBuffer{
		Data:       pixelData,
		Width:      rect.Width,
		Height:     rect.Height,
		Stride:     rect.Width * 4,
		Format:     "BGRA32",
		DPI:        96, // Default DPI for PrintWindow
		SourceRect: rect,
	}
	
	return buffer, nil
}

// Helper functions

func (e *WindowsScreenshotEngine) findWindowByTitle(title string) (uintptr, error) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	handle, _, _ := findWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if handle == 0 {
		return 0, fmt.Errorf("window not found")
	}
	return handle, nil
}

func (e *WindowsScreenshotEngine) findWindowByClassName(className string) (uintptr, error) {
	classPtr, _ := syscall.UTF16PtrFromString(className)
	handle, _, _ := findWindowW.Call(uintptr(unsafe.Pointer(classPtr)), 0)
	if handle == 0 {
		return 0, fmt.Errorf("window not found")
	}
	return handle, nil
}

func (e *WindowsScreenshotEngine) findWindowByPID(targetPID uint32) (uintptr, error) {
	var foundHandle uintptr
	
	// Callback function for EnumWindows
	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		var pid uint32
		getWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		
		if pid == targetPID {
			// Check if window is visible and has a title
			visible, _, _ := isWindowVisible.Call(hwnd)
			if visible != 0 {
				titleLen, _, _ := getWindowTextLengthW.Call(hwnd)
				if titleLen > 0 {
					foundHandle = hwnd
					return 0 // Stop enumeration
				}
			}
		}
		return 1 // Continue enumeration
	})
	
	enumWindows.Call(callback, 0)
	
	if foundHandle == 0 {
		return 0, fmt.Errorf("no visible window found for PID %d", targetPID)
	}
	
	return foundHandle, nil
}

func (e *WindowsScreenshotEngine) getWindowInfo(handle uintptr) (*types.WindowInfo, error) {
	info := &types.WindowInfo{
		Handle: handle,
	}
	
	// Get window title
	titleLen, _, _ := getWindowTextLengthW.Call(handle)
	if titleLen > 0 {
		titleBuf := make([]uint16, titleLen+1)
		getWindowTextW.Call(handle, uintptr(unsafe.Pointer(&titleBuf[0])), uintptr(len(titleBuf)))
		info.Title = syscall.UTF16ToString(titleBuf)
	}
	
	// Get class name
	classBuf := make([]uint16, 256)
	getClassName.Call(handle, uintptr(unsafe.Pointer(&classBuf[0])), 256)
	info.ClassName = syscall.UTF16ToString(classBuf)
	
	// Get process and thread IDs
	var pid uint32
	threadID, _, _ := getWindowThreadProcessId.Call(handle, uintptr(unsafe.Pointer(&pid)))
	info.ProcessID = pid
	info.ThreadID = uint32(threadID)
	
	// Get window rectangle
	var rect RECT
	getWindowRect.Call(handle, uintptr(unsafe.Pointer(&rect)))
	info.Rect = types.Rectangle{
		X:      int(rect.Left),
		Y:      int(rect.Top),
		Width:  int(rect.Right - rect.Left),
		Height: int(rect.Bottom - rect.Top),
	}
	
	// Get client rectangle
	var clientRect RECT
	getClientRect.Call(handle, uintptr(unsafe.Pointer(&clientRect)))
	info.ClientRect = types.Rectangle{
		X:      0,
		Y:      0,
		Width:  int(clientRect.Right),
		Height: int(clientRect.Bottom),
	}
	
	// Check window state
	visible, _, _ := isWindowVisible.Call(handle)
	info.IsVisible = visible != 0
	
	minimized, _, _ := isIconic.Call(handle)
	if minimized != 0 {
		info.State = "minimized"
	} else if info.IsVisible {
		info.State = "visible"
	} else {
		info.State = "hidden"
	}
	
	return info, nil
}

func (e *WindowsScreenshotEngine) isWindowMinimized(handle uintptr) bool {
	ret, _, _ := isIconic.Call(handle)
	return ret != 0
}

func (e *WindowsScreenshotEngine) restoreWindow(handle uintptr) error {
	ret, _, _ := showWindow.Call(handle, SW_RESTORE)
	if ret == 0 {
		return fmt.Errorf("failed to restore window")
	}
	return nil
}

// Ensure we implement the interface
var _ types.ScreenshotEngine = (*WindowsScreenshotEngine)(nil)

// Add constants for the advanced features
const (
	SW_SHOWNOACTIVATE = 4
)

func init() {
	// Lock OS thread for Windows API calls
	runtime.LockOSThread()
}
