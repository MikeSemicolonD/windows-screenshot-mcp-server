package window

import (
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/screenshot-mcp-server/pkg/types"
	"golang.org/x/sys/windows"
)

var (
	// Windows API DLLs
	user32   = windows.NewLazyDLL("user32.dll")
	kernel32 = windows.NewLazyDLL("kernel32.dll")
	dwmapi   = windows.NewLazyDLL("dwmapi.dll")

	// User32 functions
	enumWindows              = user32.NewProc("EnumWindows")
	getWindowTextW           = user32.NewProc("GetWindowTextW")
	getWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	getClassName             = user32.NewProc("GetClassNameW")
	getWindowRect            = user32.NewProc("GetWindowRect")
	getClientRect            = user32.NewProc("GetClientRect")
	getWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	isWindowVisible          = user32.NewProc("IsWindowVisible")
	isIconic                 = user32.NewProc("IsIconic")
	isZoomed                 = user32.NewProc("IsZoomed")
	showWindow               = user32.NewProc("ShowWindow")
	setWindowPos             = user32.NewProc("SetWindowPos")
	setForegroundWindow      = user32.NewProc("SetForegroundWindow")
	bringWindowToTop         = user32.NewProc("BringWindowToTop")
	moveWindow               = user32.NewProc("MoveWindow")
	getWindowPlacement       = user32.NewProc("GetWindowPlacement")
	setWindowPlacement       = user32.NewProc("SetWindowPlacement")
	getWindow                = user32.NewProc("GetWindow")
	getTopWindow             = user32.NewProc("GetTopWindow")
	findWindow               = user32.NewProc("FindWindowW")
	getWindowLong            = user32.NewProc("GetWindowLongPtrW")
	setWindowLong            = user32.NewProc("SetWindowLongPtrW")

	// Kernel32 functions
	openProcess                   = kernel32.NewProc("OpenProcess")
	closeHandle                   = kernel32.NewProc("CloseHandle")
	queryFullProcessImageName     = kernel32.NewProc("QueryFullProcessImageNameW")
	getProcessTimes               = kernel32.NewProc("GetProcessTimes")

	// DWM functions
	dwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

// Windows API constants
const (
	// ShowWindow constants
	SW_HIDE            = 0
	SW_SHOWNORMAL      = 1
	SW_SHOWMINIMIZED   = 2
	SW_SHOWMAXIMIZED   = 3
	SW_MAXIMIZE        = 3
	SW_SHOWNOACTIVATE  = 4
	SW_SHOW            = 5
	SW_MINIMIZE        = 6
	SW_SHOWMINNOACTIVE = 7
	SW_SHOWNA          = 8
	SW_RESTORE         = 9

	// SetWindowPos constants
	SWP_NOSIZE      = 0x0001
	SWP_NOMOVE      = 0x0002
	SWP_NOZORDER    = 0x0004
	SWP_NOREDRAW    = 0x0008
	SWP_NOACTIVATE  = 0x0010
	SWP_FRAMECHANGE = 0x0020
	SWP_SHOWWINDOW  = 0x0040
	SWP_HIDEWINDOW  = 0x0080

	// GetWindow constants
	GW_HWNDFIRST = 0
	GW_HWNDLAST  = 1
	GW_HWNDNEXT  = 2
	GW_HWNDPREV  = 3
	GW_OWNER     = 4
	GW_CHILD     = 5

	// Window attributes
	GWL_EXSTYLE = -20
	GWL_STYLE   = -16

	// Extended window styles
	WS_EX_TOPMOST     = 0x00000008
	WS_EX_TOOLWINDOW  = 0x00000080
	WS_EX_APPWINDOW   = 0x00040000
	WS_EX_NOACTIVATE  = 0x08000000

	// Window styles
	WS_OVERLAPPED  = 0x00000000
	WS_POPUP       = 0x80000000
	WS_CHILD       = 0x40000000
	WS_MINIMIZE    = 0x20000000
	WS_VISIBLE     = 0x10000000
	WS_DISABLED    = 0x08000000
	WS_CLIPSIBLINGS = 0x04000000
	WS_CLIPCHILDREN = 0x02000000
	WS_MAXIMIZE    = 0x01000000
	WS_CAPTION     = 0x00C00000
	WS_BORDER      = 0x00800000
	WS_DLGFRAME    = 0x00400000

	// Process access rights
	PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	PROCESS_QUERY_INFORMATION         = 0x0400

	// DWM attributes
	DWMWA_EXTENDED_FRAME_BOUNDS = 9
	DWMWA_CLOAKED              = 14

	// Maximum path length
	MAX_PATH = 260
)

// RECT structure for Windows API
type RECT struct {
	Left, Top, Right, Bottom int32
}

// WINDOWPLACEMENT structure
type WINDOWPLACEMENT struct {
	Length           uint32
	Flags            uint32
	ShowCmd          uint32
	PtMinPosition    POINT
	PtMaxPosition    POINT
	RcNormalPosition RECT
}

// POINT structure
type POINT struct {
	X, Y int32
}

// WindowsManager implements comprehensive window management
type WindowsManager struct {
	cache       map[uintptr]*types.WindowInfo
	cacheExpiry time.Duration
	lastUpdate  time.Time
}

// NewManager creates a new Windows manager
func NewManager() *WindowsManager {
	return &WindowsManager{
		cache:       make(map[uintptr]*types.WindowInfo),
		cacheExpiry: 5 * time.Second, // Cache window info for 5 seconds
	}
}

// EnumerateWindows lists all windows with optional filtering
func (wm *WindowsManager) EnumerateWindows(filter *types.WindowFilter) ([]types.WindowInfo, error) {
	var windows []types.WindowInfo
	var zOrder int

	// Callback function for EnumWindows
	callback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		windowInfo, err := wm.getWindowInfoDetailed(hwnd, zOrder)
		if err != nil {
			return 1 // Continue enumeration
		}

		// Apply filters
		if filter != nil {
			if !wm.matchesFilter(windowInfo, filter) {
				zOrder++
				return 1 // Continue enumeration
			}
		}

		windows = append(windows, *windowInfo)
		zOrder++
		return 1 // Continue enumeration
	})

	// Enumerate all top-level windows
	ret, _, _ := enumWindows.Call(callback, 0)
	if ret == 0 {
		return nil, fmt.Errorf("EnumWindows failed")
	}

	// Sort by z-order if requested
	sort.Slice(windows, func(i, j int) bool {
		return windows[i].ZOrder < windows[j].ZOrder
	})

	return windows, nil
}

// GetWindowInfo retrieves detailed information about a specific window
func (wm *WindowsManager) GetWindowInfo(handle uintptr) (*types.WindowInfo, error) {
	// Check cache first
	if time.Since(wm.lastUpdate) < wm.cacheExpiry {
		if cached, exists := wm.cache[handle]; exists {
			return cached, nil
		}
	}

	info, err := wm.getWindowInfoDetailed(handle, 0)
	if err != nil {
		return nil, err
	}

	// Update cache
	wm.cache[handle] = info
	wm.lastUpdate = time.Now()

	return info, nil
}

// SetWindowPos sets the window position and size
func (wm *WindowsManager) SetWindowPos(handle uintptr, rect types.Rectangle) error {
	ret, _, _ := setWindowPos.Call(
		handle,
		0, // hWndInsertAfter
		uintptr(rect.X),
		uintptr(rect.Y),
		uintptr(rect.Width),
		uintptr(rect.Height),
		SWP_NOZORDER|SWP_NOACTIVATE,
	)

	if ret == 0 {
		return fmt.Errorf("SetWindowPos failed")
	}

	return nil
}

// SetWindowVisible shows or hides a window
func (wm *WindowsManager) SetWindowVisible(handle uintptr, visible bool) error {
	var cmd uintptr
	if visible {
		cmd = SW_SHOW
	} else {
		cmd = SW_HIDE
	}

	ret, _, _ := showWindow.Call(handle, cmd)
	if ret == 0 && visible {
		// ShowWindow returns 0 if the window was previously hidden, which is not an error
	}

	return nil
}

// SetWindowState changes the window state (minimize, maximize, restore)
func (wm *WindowsManager) SetWindowState(handle uintptr, state string) error {
	var cmd uintptr
	switch strings.ToLower(state) {
	case "minimize", "minimized":
		cmd = SW_MINIMIZE
	case "maximize", "maximized":
		cmd = SW_MAXIMIZE
	case "restore", "normal":
		cmd = SW_RESTORE
	case "hide", "hidden":
		cmd = SW_HIDE
	case "show", "visible":
		cmd = SW_SHOW
	default:
		return fmt.Errorf("unsupported window state: %s", state)
	}

	ret, _, _ := showWindow.Call(handle, cmd)
	if ret == 0 && (state == "show" || state == "visible") {
		// ShowWindow returns 0 if the window was previously hidden
	}

	return nil
}

// BringToForeground brings a window to the foreground
func (wm *WindowsManager) BringToForeground(handle uintptr) error {
	// First, restore the window if it's minimized
	isMin, _, _ := isIconic.Call(handle)
	if isMin != 0 {
		showWindow.Call(handle, SW_RESTORE)
	}

	// Bring to top
	ret, _, _ := bringWindowToTop.Call(handle)
	if ret == 0 {
		return fmt.Errorf("BringWindowToTop failed")
	}

	// Set as foreground window
	ret, _, _ = setForegroundWindow.Call(handle)
	if ret == 0 {
		return fmt.Errorf("SetForegroundWindow failed")
	}

	return nil
}

// MoveWindow moves and resizes a window
func (wm *WindowsManager) MoveWindow(handle uintptr, x, y, width, height int, repaint bool) error {
	var repaintFlag uintptr
	if repaint {
		repaintFlag = 1
	}

	ret, _, _ := moveWindow.Call(handle, uintptr(x), uintptr(y), uintptr(width), uintptr(height), repaintFlag)
	if ret == 0 {
		return fmt.Errorf("MoveWindow failed")
	}

	return nil
}

// GetWindowPlacement gets the window placement information
func (wm *WindowsManager) GetWindowPlacement(handle uintptr) (*WindowPlacement, error) {
	var wp WINDOWPLACEMENT
	wp.Length = uint32(unsafe.Sizeof(wp))

	ret, _, _ := getWindowPlacement.Call(handle, uintptr(unsafe.Pointer(&wp)))
	if ret == 0 {
		return nil, fmt.Errorf("GetWindowPlacement failed")
	}

	placement := &WindowPlacement{
		ShowCmd: wp.ShowCmd,
		MinPosition: types.Point{
			X: int(wp.PtMinPosition.X),
			Y: int(wp.PtMinPosition.Y),
		},
		MaxPosition: types.Point{
			X: int(wp.PtMaxPosition.X),
			Y: int(wp.PtMaxPosition.Y),
		},
		NormalPosition: types.Rectangle{
			X:      int(wp.RcNormalPosition.Left),
			Y:      int(wp.RcNormalPosition.Top),
			Width:  int(wp.RcNormalPosition.Right - wp.RcNormalPosition.Left),
			Height: int(wp.RcNormalPosition.Bottom - wp.RcNormalPosition.Top),
		},
	}

	return placement, nil
}

// SetWindowPlacement sets the window placement
func (wm *WindowsManager) SetWindowPlacement(handle uintptr, placement *WindowPlacement) error {
	wp := WINDOWPLACEMENT{
		Length:  uint32(unsafe.Sizeof(WINDOWPLACEMENT{})),
		ShowCmd: placement.ShowCmd,
		PtMinPosition: POINT{
			X: int32(placement.MinPosition.X),
			Y: int32(placement.MinPosition.Y),
		},
		PtMaxPosition: POINT{
			X: int32(placement.MaxPosition.X),
			Y: int32(placement.MaxPosition.Y),
		},
		RcNormalPosition: RECT{
			Left:   int32(placement.NormalPosition.X),
			Top:    int32(placement.NormalPosition.Y),
			Right:  int32(placement.NormalPosition.X + placement.NormalPosition.Width),
			Bottom: int32(placement.NormalPosition.Y + placement.NormalPosition.Height),
		},
	}

	ret, _, _ := setWindowPlacement.Call(handle, uintptr(unsafe.Pointer(&wp)))
	if ret == 0 {
		return fmt.Errorf("SetWindowPlacement failed")
	}

	return nil
}

// FindWindow finds a window by class name and window name
func (wm *WindowsManager) FindWindow(className, windowName string) (uintptr, error) {
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

	handle, _, _ := findWindow.Call(
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(namePtr)),
	)

	if handle == 0 {
		return 0, fmt.Errorf("window not found")
	}

	return handle, nil
}

// GetWindowChildren returns all child windows
func (wm *WindowsManager) GetWindowChildren(parent uintptr) ([]types.WindowInfo, error) {
	var children []types.WindowInfo

	// Find first child
	child, _, _ := getWindow.Call(parent, GW_CHILD)
	if child == 0 {
		return children, nil // No children
	}

	// Enumerate all child windows
	for child != 0 {
		if info, err := wm.GetWindowInfo(child); err == nil {
			children = append(children, *info)
		}
		child, _, _ = getWindow.Call(child, GW_HWNDNEXT)
	}

	return children, nil
}

// IsWindowTopMost checks if a window is topmost
func (wm *WindowsManager) IsWindowTopMost(handle uintptr) bool {
	gwl := int32(GWL_EXSTYLE)
	exStyle, _, _ := getWindowLong.Call(handle, uintptr(gwl))
	return (exStyle & WS_EX_TOPMOST) != 0
}

// SetWindowTopMost sets or removes the topmost flag
func (wm *WindowsManager) SetWindowTopMost(handle uintptr, topmost bool) error {
	var hWndInsertAfter uintptr
	if topmost {
		hWndInsertAfter = ^uintptr(0) // HWND_TOPMOST
	} else {
		hWndInsertAfter = ^uintptr(1) // HWND_NOTOPMOST
	}

	ret, _, _ := setWindowPos.Call(
		handle,
		hWndInsertAfter,
		0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE,
	)

	if ret == 0 {
		return fmt.Errorf("SetWindowPos failed")
	}

	return nil
}

// Helper functions

func (wm *WindowsManager) getWindowInfoDetailed(handle uintptr, zOrder int) (*types.WindowInfo, error) {
	info := &types.WindowInfo{
		Handle: handle,
		ZOrder: zOrder,
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

	// Get window rectangles
	var rect RECT
	getWindowRect.Call(handle, uintptr(unsafe.Pointer(&rect)))
	info.Rect = types.Rectangle{
		X:      int(rect.Left),
		Y:      int(rect.Top),
		Width:  int(rect.Right - rect.Left),
		Height: int(rect.Bottom - rect.Top),
	}

	var clientRect RECT
	getClientRect.Call(handle, uintptr(unsafe.Pointer(&clientRect)))
	info.ClientRect = types.Rectangle{
		X:      0,
		Y:      0,
		Width:  int(clientRect.Right),
		Height: int(clientRect.Bottom),
	}

	// Get window state
	visible, _, _ := isWindowVisible.Call(handle)
	info.IsVisible = visible != 0

	minimized, _, _ := isIconic.Call(handle)
	maximized, _, _ := isZoomed.Call(handle)

	if minimized != 0 {
		info.State = "minimized"
	} else if maximized != 0 {
		info.State = "maximized"
	} else if info.IsVisible {
		info.State = "visible"
	} else {
		info.State = "hidden"
	}

	// Get additional window properties
	info.IsTopMost = wm.IsWindowTopMost(handle)
	
	return info, nil
}

func (wm *WindowsManager) matchesFilter(info *types.WindowInfo, filter *types.WindowFilter) bool {
	// Title filter
	if filter.TitleContains != "" {
		if !strings.Contains(strings.ToLower(info.Title), strings.ToLower(filter.TitleContains)) {
			return false
		}
	}

	// Class name filter
	if len(filter.ClassNames) > 0 {
		found := false
		for _, className := range filter.ClassNames {
			if strings.EqualFold(info.ClassName, className) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Process ID filter
	if len(filter.ProcessIDs) > 0 {
		found := false
		for _, pid := range filter.ProcessIDs {
			if info.ProcessID == pid {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Visible only filter
	if filter.VisibleOnly && !info.IsVisible {
		return false
	}

	// Size filters
	if filter.MinimumSize != nil {
		if info.Rect.Width < filter.MinimumSize.Width || info.Rect.Height < filter.MinimumSize.Height {
			return false
		}
	}

	if filter.MaximumSize != nil {
		if info.Rect.Width > filter.MaximumSize.Width || info.Rect.Height > filter.MaximumSize.Height {
			return false
		}
	}

	// Exclude system windows
	if filter.ExcludeSystem {
		if wm.isSystemWindow(info) {
			return false
		}
	}

	return true
}

func (wm *WindowsManager) isSystemWindow(info *types.WindowInfo) bool {
	// Common system window patterns
	systemClasses := []string{
		"Shell_TrayWnd",
		"DV2ControlHost",
		"MsgrIMEWindowClass",
		"SysShadow",
		"Button",
		"Progman",
		"WorkerW",
	}

	for _, sysClass := range systemClasses {
		if strings.EqualFold(info.ClassName, sysClass) {
			return true
		}
	}

	// Windows with no title and certain characteristics
	if info.Title == "" && (info.Rect.Width < 100 || info.Rect.Height < 100) {
		return true
	}

	return false
}

// WindowPlacement represents window placement information
type WindowPlacement struct {
	ShowCmd        uint32
	MinPosition    types.Point
	MaxPosition    types.Point
	NormalPosition types.Rectangle
}

// Additional types for extended window information
type WindowInfo struct {
	types.WindowInfo
	IsTopMost    bool
	Placement    *WindowPlacement
	Children     []types.WindowInfo
	Owner        uintptr
	ProcessName  string
	CreationTime time.Time
}

// Ensure WindowsManager implements the interface
var _ types.WindowManager = (*WindowsManager)(nil)