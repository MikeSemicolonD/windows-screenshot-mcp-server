package screenshot

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/screenshot-mcp-server/pkg/types"
	"golang.org/x/sys/windows"
)

// Advanced Windows API functions for hidden window capture
var (
	// Additional DWM functions
	dwmRegisterThumbnail          = dwmapi.NewProc("DwmRegisterThumbnail")
	dwmUnregisterThumbnail        = dwmapi.NewProc("DwmUnregisterThumbnail")
	dwmUpdateThumbnailProperties  = dwmapi.NewProc("DwmUpdateThumbnailProperties")
	dwmQueryThumbnailSourceSize   = dwmapi.NewProc("DwmQueryThumbnailSourceSize")
	
	// Shell functions for system tray
	shell32                       = windows.NewLazyDLL("shell32.dll")
	shell_NotifyIconGetRect       = shell32.NewProc("Shell_NotifyIconGetRect")

	// Kernel32 for process/thread snapshot and handle management
	kernel32                      = windows.NewLazyDLL("kernel32.dll")
	closeHandle                   = kernel32.NewProc("CloseHandle")
	
	// Additional User32 functions
	sendMessage                   = user32.NewProc("SendMessageW")
	postMessage                   = user32.NewProc("PostMessageW")
	enumChildWindows              = user32.NewProc("EnumChildWindows")
	enumThreadWindows             = user32.NewProc("EnumThreadWindows")
	// getWindowThreadProcessId is declared in engine.go

	// Process and thread functions
	createToolhelp32Snapshot      = kernel32.NewProc("CreateToolhelp32Snapshot")
	process32First               = kernel32.NewProc("Process32FirstW")
	process32Next                = kernel32.NewProc("Process32NextW")
	thread32First                = kernel32.NewProc("Thread32First")
	thread32Next                 = kernel32.NewProc("Thread32Next")
)

// Windows API constants for advanced features
const (
	// DWM Thumbnail flags
	DWM_TNP_RECTDESTINATION = 0x00000001
	DWM_TNP_RECTSOURCE      = 0x00000002
	DWM_TNP_OPACITY         = 0x00000004
	DWM_TNP_VISIBLE         = 0x00000008
	DWM_TNP_SOURCECLIENTAREAONLY = 0x00000010
	
	// Window messages
	WM_PRINT          = 0x0317
	WM_PRINTCLIENT    = 0x0318
	PRF_CHECKVISIBLE  = 0x00000001
	PRF_NONCLIENT     = 0x00000002
	PRF_CLIENT        = 0x00000004
	PRF_ERASEBKGND    = 0x00000008
	PRF_CHILDREN      = 0x00000010
	PRF_OWNED         = 0x00000020
	
	// Cloaking constants
	DWMWA_CLOAKED = 14
	DWM_CLOAKED_APP = 0x0000001
	DWM_CLOAKED_SHELL = 0x0000002  
	DWM_CLOAKED_INHERITED = 0x0000004
	
	// Toolhelp32 constants
	TH32CS_SNAPPROCESS = 0x00000002
	TH32CS_SNAPTHREAD  = 0x00000004

	// ShowWindow command
	SW_MINIMIZE = 6
	
	// System tray constants
	NIM_ADD    = 0x00000000
	NIM_MODIFY = 0x00000001
	NIM_DELETE = 0x00000002
	NIM_SETFOCUS = 0x00000003
	NIM_SETVERSION = 0x00000004
)

// DWM Thumbnail structures
type DWM_THUMBNAIL_PROPERTIES struct {
	dwFlags               uint32
	rcDestination         RECT
	rcSource              RECT
	opacity               byte
	fVisible              int32  // BOOL
	fSourceClientAreaOnly int32  // BOOL
}

type SIZE struct {
	Width, Height int32
}

// Process and thread structures
type PROCESSENTRY32 struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [260]uint16
}

type THREADENTRY32 struct {
	dwSize             uint32
	cntUsage           uint32
	th32ThreadID       uint32
	th32OwnerProcessID uint32
	tpBasePri          int32
	tpDeltaPri         int32
	dwFlags            uint32
}

// EnumerateAllProcessWindows finds all windows belonging to a specific process
func (e *WindowsScreenshotEngine) EnumerateAllProcessWindows(pid uint32) ([]types.WindowInfo, error) {
	var windows []types.WindowInfo
	
	// Find all threads for this process
	threads, err := e.getProcessThreads(pid)
	if err != nil {
		return nil, fmt.Errorf("failed to get process threads: %w", err)
	}
	
	// Enumerate windows for each thread
	for _, threadID := range threads {
		threadWindows, err := e.enumerateThreadWindows(threadID)
		if err != nil {
			continue // Skip threads that fail
		}
		windows = append(windows, threadWindows...)
	}
	
	// Also try standard EnumWindows with PID filtering
	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		var windowPID uint32
		getWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
		
		if windowPID == pid {
			if info, err := e.getWindowInfo(hwnd); err == nil {
				windows = append(windows, *info)
			}
		}
		return 1 // Continue enumeration
	})
	
	enumWindows.Call(callback, 0)
	
	return e.deduplicateWindows(windows), nil
}

// FindSystemTrayApps discovers applications running in the system tray
func (e *WindowsScreenshotEngine) FindSystemTrayApps() ([]types.WindowInfo, error) {
	var trayApps []types.WindowInfo
	
	// Find the system tray window
	trayWnd, err := e.findWindow("Shell_TrayWnd", "")
	if err != nil {
		return nil, fmt.Errorf("failed to find system tray: %w", err)
	}
	
	// Find notification area
	notifyWnd, _ := e.findChildWindow(trayWnd, "TrayNotifyWnd", "")
	if notifyWnd != 0 {
		sysPager, _ := e.findChildWindow(notifyWnd, "SysPager", "")
		if sysPager != 0 {
			toolbarWnd, _ := e.findChildWindow(sysPager, "ToolbarWindow32", "")
			if toolbarWnd != 0 {
				// Get processes with tray icons
				trayProcesses := e.getTrayProcesses(toolbarWnd)
				
				for _, pid := range trayProcesses {
					processWindows, err := e.EnumerateAllProcessWindows(pid)
					if err == nil {
						trayApps = append(trayApps, processWindows...)
					}
				}
			}
		}
	}
	
	return trayApps, nil
}

// FindHiddenWindows discovers windows that are hidden but not minimized
func (e *WindowsScreenshotEngine) FindHiddenWindows() ([]types.WindowInfo, error) {
	var hiddenWindows []types.WindowInfo
	
	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		visible, _, _ := isWindowVisible.Call(hwnd)
		iconic, _, _ := isIconic.Call(hwnd)
		
		// Window exists but is not visible and not minimized
		if visible == 0 && iconic == 0 {
			if info, err := e.getWindowInfo(hwnd); err == nil {
				// Filter out system windows with no title
				if info.Title != "" || len(info.ClassName) > 0 {
					hiddenWindows = append(hiddenWindows, *info)
				}
			}
		}
		return 1 // Continue enumeration
	})
	
	enumWindows.Call(callback, 0)
	
	return hiddenWindows, nil
}

// FindCloakedWindows discovers windows that are cloaked by DWM (UWP apps, etc.)
func (e *WindowsScreenshotEngine) FindCloakedWindows() ([]types.WindowInfo, error) {
	var cloakedWindows []types.WindowInfo
	
	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		var cloaked uint32
		ret, _, _ := dwmGetWindowAttribute.Call(
			hwnd,
			DWMWA_CLOAKED,
			uintptr(unsafe.Pointer(&cloaked)),
			unsafe.Sizeof(cloaked),
		)
		
		// Window is cloaked by DWM
		if ret == 0 && cloaked != 0 {
			if info, err := e.getWindowInfo(hwnd); err == nil {
				info.State = "cloaked"
				cloakedWindows = append(cloakedWindows, *info)
			}
		}
		return 1 // Continue enumeration
	})
	
	enumWindows.Call(callback, 0)
	
	return cloakedWindows, nil
}

// CaptureHiddenByPID captures a screenshot of any window from a process, including hidden ones
func (e *WindowsScreenshotEngine) CaptureHiddenByPID(pid uint32, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	if options == nil {
		options = types.DefaultCaptureOptions()
	}
	
	// Force hidden window support
	options.AllowHidden = true
	options.AllowMinimized = true
	options.AllowCloaked = true
	options.DetectTrayApps = true
	
	// Find all windows for the process
	windows, err := e.EnumerateAllProcessWindows(pid)
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate process windows: %w", err)
	}
	
	if len(windows) == 0 {
		return nil, fmt.Errorf("no windows found for PID %d", pid)
	}
	
	// Try to capture the best window (prefer main windows)
	for _, window := range windows {
		if window.Title != "" && window.Rect.Width > 100 && window.Rect.Height > 100 {
			buffer, err := e.CaptureWithFallbacks(window.Handle, options)
			if err == nil {
				return buffer, nil
			}
		}
	}
	
	// Fallback to any window
	return e.CaptureWithFallbacks(windows[0].Handle, options)
}

// CaptureTrayApp captures a screenshot of a system tray application
func (e *WindowsScreenshotEngine) CaptureTrayApp(processName string, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	if options == nil {
		options = types.DefaultCaptureOptions()
	}
	
	// Find the process ID
	pid, err := e.findProcessByName(processName)
	if err != nil {
		return nil, fmt.Errorf("failed to find process %s: %w", processName, err)
	}
	
	return e.CaptureHiddenByPID(pid, options)
}

// CaptureWithFallbacks uses multiple capture methods with intelligent fallback
func (e *WindowsScreenshotEngine) CaptureWithFallbacks(handle uintptr, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	if options == nil {
		options = types.DefaultCaptureOptions()
	}
	
	windowInfo, err := e.getWindowInfo(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get window info: %w", err)
	}
	
	// Determine capture methods to try
	methods := e.selectCaptureMethods(windowInfo, options)
	
	var lastErr error
	for i, method := range methods {
		buffer, err := e.captureWithMethod(handle, windowInfo, method, options)
		if err == nil {
			return buffer, nil
		}
		lastErr = err
		
		// Add delay between attempts
		if i < len(methods)-1 {
			time.Sleep(time.Millisecond * 100)
		}
	}
	
	return nil, fmt.Errorf("all capture methods failed, last error: %w", lastErr)
}

// selectCaptureMethods intelligently selects the best capture methods for a window
func (e *WindowsScreenshotEngine) selectCaptureMethods(windowInfo *types.WindowInfo, options *types.CaptureOptions) []types.CaptureMethod {
	methods := make([]types.CaptureMethod, 0, 6)
	
	// If user specified a preferred method, try it first
	if options.PreferredMethod != types.CaptureAuto {
		methods = append(methods, options.PreferredMethod)
	}
	
	// Add fallback methods based on window state
	switch windowInfo.State {
	case "visible":
		methods = append(methods, types.CaptureBitBlt, types.CapturePrintWindow, types.CaptureDWMThumbnail)
	case "minimized":
		methods = append(methods, types.CaptureDWMThumbnail, types.CapturePrintWindow, types.CaptureWMPrint, types.CaptureStealthRestore)
	case "hidden", "cloaked":
		methods = append(methods, types.CaptureDWMThumbnail, types.CaptureWMPrint, types.CapturePrintWindow)
	default:
		methods = append(methods, types.CaptureDWMThumbnail, types.CapturePrintWindow, types.CaptureWMPrint, types.CaptureBitBlt)
	}
	
	// Add user-specified fallback methods
	if len(options.FallbackMethods) > 0 {
		methods = append(methods, options.FallbackMethods...)
	}
	
	// Remove duplicates
	return e.deduplicateMethods(methods)
}

// captureWithMethod captures using a specific method
func (e *WindowsScreenshotEngine) captureWithMethod(handle uintptr, windowInfo *types.WindowInfo, method types.CaptureMethod, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	switch method {
	case types.CaptureBitBlt:
		return e.captureVisibleWindow(handle, windowInfo, options)
	case types.CapturePrintWindow:
		return e.tryPrintWindow(handle, windowInfo, options)
	case types.CaptureDWMThumbnail:
		return e.captureDWMThumbnail(handle, windowInfo, options)
	case types.CaptureWMPrint:
		return e.captureWMPrint(handle, windowInfo, options)
	case types.CaptureStealthRestore:
		return e.captureStealthRestore(handle, windowInfo, options)
	default:
		return nil, fmt.Errorf("unsupported capture method: %s", method)
	}
}

// captureDWMThumbnail uses the DWM Thumbnail API to capture any window
func (e *WindowsScreenshotEngine) captureDWMThumbnail(handle uintptr, windowInfo *types.WindowInfo, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	// Get desktop window as destination
	desktopHandle, _, _ := getDesktopWindow.Call()
	if desktopHandle == 0 {
		return nil, fmt.Errorf("failed to get desktop window")
	}
	
	// Register thumbnail
	var thumbnail uintptr
	ret, _, _ := dwmRegisterThumbnail.Call(desktopHandle, handle, uintptr(unsafe.Pointer(&thumbnail)))
	if ret != 0 {
		return nil, fmt.Errorf("DwmRegisterThumbnail failed: %x", ret)
	}
	defer dwmUnregisterThumbnail.Call(thumbnail)
	
	// Get source size
	var sourceSize SIZE
	ret, _, _ = dwmQueryThumbnailSourceSize.Call(thumbnail, uintptr(unsafe.Pointer(&sourceSize)))
	if ret != 0 {
		return nil, fmt.Errorf("DwmQueryThumbnailSourceSize failed: %x", ret)
	}
	
	// Create off-screen bitmap for thumbnail
	screenDC, _, _ := getDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("failed to get screen DC")
	}
	defer releaseDC.Call(0, screenDC)
	
	memDC, _, _ := createCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("failed to create compatible DC")
	}
	defer deleteDC.Call(memDC)
	
	// Create DIB section
	width := int(sourceSize.Width)
	height := int(sourceSize.Height)
	
	var bmi BITMAPINFO
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(width)
	bmi.Header.Height = -int32(height) // Top-down DIB
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = BI_RGB
	
	var pBits uintptr
	bitmap, _, _ := createDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&pBits)), 0, 0)
	if bitmap == 0 {
		return nil, fmt.Errorf("failed to create DIB section")
	}
	defer deleteObject.Call(bitmap)
	
	oldBitmap, _, _ := selectObject.Call(memDC, bitmap)
	defer selectObject.Call(memDC, oldBitmap)
	
	// Setup thumbnail properties
	var props DWM_THUMBNAIL_PROPERTIES
	props.dwFlags = DWM_TNP_RECTDESTINATION | DWM_TNP_RECTSOURCE | DWM_TNP_VISIBLE
	props.rcDestination = RECT{0, 0, int32(width), int32(height)}
	props.rcSource = RECT{0, 0, int32(sourceSize.Width), int32(sourceSize.Height)}
	props.fVisible = 1
	
	// Update thumbnail
	ret, _, _ = dwmUpdateThumbnailProperties.Call(thumbnail, uintptr(unsafe.Pointer(&props)))
	if ret != 0 {
		return nil, fmt.Errorf("DwmUpdateThumbnailProperties failed: %x", ret)
	}
	
	// Give DWM time to render
	time.Sleep(time.Millisecond * 100)
	
	// Copy pixel data
	pixelCount := width * height * 4
	pixelData := make([]byte, pixelCount)
	
	if pBits != 0 {
		copy(pixelData, (*[1 << 30]byte)(unsafe.Pointer(pBits))[:pixelCount:pixelCount])
	}
	
	// Create screenshot buffer
	buffer := &types.ScreenshotBuffer{
		Data:       pixelData,
		Width:      width,
		Height:     height,
		Stride:     width * 4,
		Format:     "BGRA32",
		DPI:        96,
		Timestamp:  time.Now(),
		SourceRect: types.Rectangle{X: 0, Y: 0, Width: width, Height: height},
		WindowInfo: *windowInfo,
	}
	
	return buffer, nil
}

// captureWMPrint uses WM_PRINT message to force window rendering
func (e *WindowsScreenshotEngine) captureWMPrint(handle uintptr, windowInfo *types.WindowInfo, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
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
	
	oldBitmap, _, _ := selectObject.Call(memDC, bitmap)
	defer selectObject.Call(memDC, oldBitmap)
	
	// Send WM_PRINT message
	flags := uintptr(PRF_CLIENT | PRF_NONCLIENT | PRF_CHILDREN | PRF_OWNED)
	if options.IncludeFrame {
		flags |= PRF_NONCLIENT
	}
	
	ret, _, _ := sendMessage.Call(handle, WM_PRINT, memDC, flags)
	if ret == 0 {
		return nil, fmt.Errorf("WM_PRINT failed")
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
		DPI:        96,
		Timestamp:  time.Now(),
		SourceRect: rect,
		WindowInfo: *windowInfo,
	}
	
	return buffer, nil
}

// captureStealthRestore temporarily restores a minimized window without activating it
func (e *WindowsScreenshotEngine) captureStealthRestore(handle uintptr, windowInfo *types.WindowInfo, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	isMinimized := e.isWindowMinimized(handle)
	if !isMinimized {
		return e.captureVisibleWindow(handle, windowInfo, options)
	}
	
	// Store original window placement
	placement, err := e.getWindowPlacement(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get window placement: %w", err)
	}
	
	// Restore window without activating
	ret, _, _ := showWindow.Call(handle, SW_SHOWNOACTIVATE)
	if ret == 0 {
		return nil, fmt.Errorf("failed to restore window")
	}
	
	// Wait for window to become visible
	if options.WaitForVisible > 0 {
		time.Sleep(options.WaitForVisible)
	} else {
		time.Sleep(time.Millisecond * 500)
	}
	
	// Capture the now-visible window
	buffer, err := e.captureVisibleWindow(handle, windowInfo, options)
	
	// Restore original state
	if placement != nil {
		e.setWindowPlacement(handle, placement)
	} else {
		showWindow.Call(handle, SW_MINIMIZE)
	}
	
	return buffer, err
}

// Helper functions

func (e *WindowsScreenshotEngine) getProcessThreads(pid uint32) ([]uint32, error) {
	var threads []uint32
	
	snapshot, _, _ := createToolhelp32Snapshot.Call(TH32CS_SNAPTHREAD, 0)
	if snapshot == ^uintptr(0) {
		return nil, fmt.Errorf("failed to create snapshot")
	}
	defer closeHandle.Call(snapshot)
	
	var te THREADENTRY32
	te.dwSize = uint32(unsafe.Sizeof(te))
	
	ret, _, _ := thread32First.Call(snapshot, uintptr(unsafe.Pointer(&te)))
	if ret == 0 {
		return threads, nil
	}
	
	for {
		if te.th32OwnerProcessID == pid {
			threads = append(threads, te.th32ThreadID)
		}
		
		ret, _, _ := thread32Next.Call(snapshot, uintptr(unsafe.Pointer(&te)))
		if ret == 0 {
			break
		}
	}
	
	return threads, nil
}

func (e *WindowsScreenshotEngine) enumerateThreadWindows(threadID uint32) ([]types.WindowInfo, error) {
	var windows []types.WindowInfo
	
	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		if info, err := e.getWindowInfo(hwnd); err == nil {
			windows = append(windows, *info)
		}
		return 1 // Continue enumeration
	})
	
	enumThreadWindows.Call(uintptr(threadID), callback, 0)
	
	return windows, nil
}

func (e *WindowsScreenshotEngine) findWindow(className, windowName string) (uintptr, error) {
	var classPtr, namePtr *uint16
	
	if className != "" {
		var err error
		classPtr, err = syscall.UTF16PtrFromString(className)
		if err != nil {
			return 0, err
		}
	}
	
	if windowName != "" {
		var err error
		namePtr, err = syscall.UTF16PtrFromString(windowName)
		if err != nil {
			return 0, err
		}
	}
	
	handle, _, _ := findWindowW.Call(
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(namePtr)),
	)
	
	if handle == 0 {
		return 0, fmt.Errorf("window not found")
	}
	
	return handle, nil
}

func (e *WindowsScreenshotEngine) findChildWindow(parent uintptr, className, windowName string) (uintptr, error) {
	var found uintptr
	
	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		if className != "" {
			classBuf := make([]uint16, 256)
			getClassName.Call(hwnd, uintptr(unsafe.Pointer(&classBuf[0])), 256)
			actualClass := syscall.UTF16ToString(classBuf)
			if actualClass != className {
				return 1 // Continue
			}
		}
		
		if windowName != "" {
			titleLen, _, _ := getWindowTextLengthW.Call(hwnd)
			if titleLen > 0 {
				titleBuf := make([]uint16, titleLen+1)
				getWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&titleBuf[0])), uintptr(len(titleBuf)))
				actualName := syscall.UTF16ToString(titleBuf)
				if actualName != windowName {
					return 1 // Continue
				}
			} else if windowName != "" {
				return 1 // Continue
			}
		}
		
		found = hwnd
		return 0 // Stop enumeration
	})
	
	enumChildWindows.Call(parent, callback, 0)
	
	if found == 0 {
		return 0, fmt.Errorf("child window not found")
	}
	
	return found, nil
}

func (e *WindowsScreenshotEngine) getTrayProcesses(toolbarWnd uintptr) []uint32 {
	var processes []uint32
	
	// This would require more complex implementation involving
	// toolbar button enumeration and process identification
	// For now, return empty slice as a placeholder
	
	return processes
}

func (e *WindowsScreenshotEngine) findProcessByName(name string) (uint32, error) {
	snapshot, _, _ := createToolhelp32Snapshot.Call(TH32CS_SNAPPROCESS, 0)
	if snapshot == ^uintptr(0) {
		return 0, fmt.Errorf("failed to create snapshot")
	}
	defer closeHandle.Call(snapshot)
	
	var pe PROCESSENTRY32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	
	ret, _, _ := process32First.Call(snapshot, uintptr(unsafe.Pointer(&pe)))
	if ret == 0 {
		return 0, fmt.Errorf("no processes found")
	}
	
	for {
		exeName := syscall.UTF16ToString(pe.szExeFile[:])
		if exeName == name {
			return pe.th32ProcessID, nil
		}
		
		ret, _, _ := process32Next.Call(snapshot, uintptr(unsafe.Pointer(&pe)))
		if ret == 0 {
			break
		}
	}
	
	return 0, fmt.Errorf("process not found: %s", name)
}

func (e *WindowsScreenshotEngine) getWindowPlacement(handle uintptr) (*windowPlacement, error) {
	// Implementation would use GetWindowPlacement
	return nil, nil
}

func (e *WindowsScreenshotEngine) setWindowPlacement(handle uintptr, placement *windowPlacement) error {
	// Implementation would use SetWindowPlacement
	return nil
}

func (e *WindowsScreenshotEngine) deduplicateWindows(windows []types.WindowInfo) []types.WindowInfo {
	seen := make(map[uintptr]bool)
	var result []types.WindowInfo
	
	for _, window := range windows {
		if !seen[window.Handle] {
			seen[window.Handle] = true
			result = append(result, window)
		}
	}
	
	return result
}

func (e *WindowsScreenshotEngine) deduplicateMethods(methods []types.CaptureMethod) []types.CaptureMethod {
	seen := make(map[types.CaptureMethod]bool)
	var result []types.CaptureMethod
	
	for _, method := range methods {
		if !seen[method] {
			seen[method] = true
			result = append(result, method)
		}
	}
	
	return result
}

type windowPlacement struct {
	// Placeholder for actual WINDOWPLACEMENT structure
}