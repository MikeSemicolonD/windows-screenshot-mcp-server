package chrome

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
	"github.com/screenshot-mcp-server/pkg/types"
	"golang.org/x/sys/windows"
)

var (
	// Windows API for Chrome process discovery
	user32                   = windows.NewLazyDLL("user32.dll")
	kernel32                 = windows.NewLazyDLL("kernel32.dll")
	enumWindows              = user32.NewProc("EnumWindows")
	getWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	getClassName             = user32.NewProc("GetClassNameW")
	openProcess              = kernel32.NewProc("OpenProcess")
	closeHandle              = kernel32.NewProc("CloseHandle")
	queryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
)

const (
	PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	MAX_PATH                          = 260
)

// ChromeManager implements Chrome DevTools Protocol integration
type ChromeManager struct {
	httpClient    *http.Client
	wsDialer      *websocket.Dialer
	defaultPort   int
	instanceCache map[uint32]*types.ChromeInstance
	timeout       time.Duration
}

// NewManager creates a new Chrome manager
func NewManager() *ChromeManager {
	return &ChromeManager{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		wsDialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
		},
		defaultPort:   9222,
		instanceCache: make(map[uint32]*types.ChromeInstance),
		timeout:       30 * time.Second,
	}
}

// DiscoverInstances discovers all running Chrome instances
func (cm *ChromeManager) DiscoverInstances() ([]types.ChromeInstance, error) {
	var instances []types.ChromeInstance
	
	// Find all Chrome processes
	chromePIDs, err := cm.findChromeProcesses()
	if err != nil {
		return nil, fmt.Errorf("failed to find Chrome processes: %w", err)
	}
	
	for _, pid := range chromePIDs {
		instance, err := cm.discoverInstance(pid)
		if err != nil {
			// Skip instances that can't be discovered (might not have debugging enabled)
			continue
		}
		instances = append(instances, *instance)
	}
	
	return instances, nil
}

// GetTabs retrieves all tabs for a Chrome instance
func (cm *ChromeManager) GetTabs(instance *types.ChromeInstance) ([]types.ChromeTab, error) {
	if instance == nil {
		return nil, fmt.Errorf("instance cannot be nil")
	}
	
	url := fmt.Sprintf("http://localhost:%d/json", instance.DebugPort)
	
	resp, err := cm.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Chrome DevTools: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Chrome DevTools returned status %d", resp.StatusCode)
	}
	
	var tabs []types.ChromeTab
	if err := json.NewDecoder(resp.Body).Decode(&tabs); err != nil {
		return nil, fmt.Errorf("failed to decode tabs response: %w", err)
	}
	
	// Filter only page tabs and mark active tab
	var filteredTabs []types.ChromeTab
	for _, tab := range tabs {
		if tab.Type == "page" {
			// Check if tab is active (you might need to implement additional logic)
			filteredTabs = append(filteredTabs, tab)
		}
	}
	
	return filteredTabs, nil
}

// CaptureTab captures a screenshot of a specific tab
func (cm *ChromeManager) CaptureTab(tab *types.ChromeTab, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	if tab == nil {
		return nil, fmt.Errorf("tab cannot be nil")
	}
	
	if tab.WebSocketURL == "" {
		return nil, fmt.Errorf("tab does not have WebSocket URL")
	}
	
	// Connect to tab's WebSocket
	ctx, cancel := context.WithTimeout(context.Background(), cm.timeout)
	defer cancel()
	
	conn, _, err := cm.wsDialer.DialContext(ctx, tab.WebSocketURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to tab WebSocket: %w", err)
	}
	defer conn.Close()
	
	// Set up response channel
	responses := make(chan map[string]interface{}, 10)
	errors := make(chan error, 1)
	
	// Start WebSocket message handler
	go cm.handleWebSocketMessages(conn, responses, errors)
	
	// Take screenshot using Chrome DevTools Protocol
	screenshotData, err := cm.takeScreenshot(conn, responses, options)
	if err != nil {
		return nil, fmt.Errorf("failed to take screenshot: %w", err)
	}
	
	// Decode base64 image data
	imageData, err := base64.StdEncoding.DecodeString(screenshotData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode screenshot data: %w", err)
	}
	
	// Convert to our screenshot buffer format
	buffer := &types.ScreenshotBuffer{
		Data:      imageData,
		Format:    "PNG", // Chrome always returns PNG
		Timestamp: time.Now(),
	}
	
	// Parse PNG header to get dimensions
	if len(imageData) > 24 {
		buffer.Width = int(uint32(imageData[16])<<24 | uint32(imageData[17])<<16 | uint32(imageData[18])<<8 | uint32(imageData[19]))
		buffer.Height = int(uint32(imageData[20])<<24 | uint32(imageData[21])<<16 | uint32(imageData[22])<<8 | uint32(imageData[23]))
	}
	
	return buffer, nil
}

// ExecuteScript executes JavaScript in a tab
func (cm *ChromeManager) ExecuteScript(tab *types.ChromeTab, script string) (interface{}, error) {
	if tab == nil {
		return nil, fmt.Errorf("tab cannot be nil")
	}
	
	if tab.WebSocketURL == "" {
		return nil, fmt.Errorf("tab does not have WebSocket URL")
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), cm.timeout)
	defer cancel()
	
	conn, _, err := cm.wsDialer.DialContext(ctx, tab.WebSocketURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to tab WebSocket: %w", err)
	}
	defer conn.Close()
	
	// Set up response channel
	responses := make(chan map[string]interface{}, 10)
	errors := make(chan error, 1)
	
	// Start WebSocket message handler
	go cm.handleWebSocketMessages(conn, responses, errors)
	
	// Execute script
	return cm.executeScript(conn, responses, script)
}

// findChromeProcessesContext accumulates Chrome PIDs across an EnumWindows pass.
// See findChromeProcessesCallback for why the callback is package-level.
type findChromeProcessesContext struct {
	manager *ChromeManager
	pids    []uint32
}

// findChromeProcessesCallback is created exactly once. syscall.NewCallback draws
// from a small, process-wide pool that is never freed, so a fresh per-call
// callback would eventually exhaust it and panic; state is passed via lParam
// instead.
var findChromeProcessesCallback = syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
	ctx := (*findChromeProcessesContext)(unsafe.Pointer(lParam))
	var pid uint32
	getWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

	// Check if window class is Chrome
	classBuf := make([]uint16, 256)
	getClassName.Call(hwnd, uintptr(unsafe.Pointer(&classBuf[0])), 256)
	className := syscall.UTF16ToString(classBuf)

	// Chrome window classes
	if strings.Contains(className, "Chrome_WidgetWin") {
		// Check if this PID is already in our list
		found := false
		for _, existingPID := range ctx.pids {
			if existingPID == pid {
				found = true
				break
			}
		}
		if !found {
			// Verify it's actually Chrome by checking process name
			if ctx.manager.isChromePID(pid) {
				ctx.pids = append(ctx.pids, pid)
			}
		}
	}

	return 1 // Continue enumeration
})

// findChromeProcesses finds all Chrome process IDs
func (cm *ChromeManager) findChromeProcesses() ([]uint32, error) {
	ctx := &findChromeProcessesContext{manager: cm}
	enumWindows.Call(findChromeProcessesCallback, uintptr(unsafe.Pointer(ctx)))

	if len(ctx.pids) == 0 {
		return nil, fmt.Errorf("no Chrome processes found")
	}

	return ctx.pids, nil
}

// isChromePID verifies if a PID belongs to Chrome
func (cm *ChromeManager) isChromePID(pid uint32) bool {
	handle, _, _ := openProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return false
	}
	defer closeHandle.Call(handle)
	
	var pathBuf [MAX_PATH]uint16
	var size uint32 = MAX_PATH
	
	ret, _, _ := queryFullProcessImageName.Call(handle, 0, uintptr(unsafe.Pointer(&pathBuf[0])), uintptr(unsafe.Pointer(&size)))
	if ret == 0 {
		return false
	}
	
	processPath := syscall.UTF16ToString(pathBuf[:size])
	return strings.Contains(strings.ToLower(processPath), "chrome.exe")
}

// discoverInstance discovers Chrome instance information for a PID
func (cm *ChromeManager) discoverInstance(pid uint32) (*types.ChromeInstance, error) {
	// Check cache first
	if cached, exists := cm.instanceCache[pid]; exists {
		return cached, nil
	}
	
	// Find debugging port for this Chrome instance
	debugPort, err := cm.findDebugPort(pid)
	if err != nil {
		return nil, fmt.Errorf("failed to find debug port for PID %d: %w", pid, err)
	}
	
	// Get Chrome version info
	versionInfo, err := cm.getVersionInfo(debugPort)
	if err != nil {
		return nil, fmt.Errorf("failed to get version info: %w", err)
	}
	
	instance := &types.ChromeInstance{
		PID:         pid,
		DebugPort:   debugPort,
		Version:     versionInfo.Browser,
		UserAgent:   versionInfo.UserAgent,
		ProfilePath: cm.getProfilePath(pid),
	}
	
	// Cache the instance
	cm.instanceCache[pid] = instance
	
	return instance, nil
}

// findDebugPort finds the debugging port for a Chrome process
func (cm *ChromeManager) findDebugPort(pid uint32) (int, error) {
	// Try to read Chrome command line to find --remote-debugging-port
	cmdLine, err := cm.getProcessCommandLine(pid)
	if err == nil {
		if port := cm.extractPortFromCommandLine(cmdLine); port > 0 {
			return port, nil
		}
	}
	
	// Try common ports
	commonPorts := []int{9222, 9223, 9224, 9225, 9226}
	for _, port := range commonPorts {
		if cm.isPortOpen(port) {
			// Verify this is the correct Chrome instance
			if cm.verifyChromePID(port, pid) {
				return port, nil
			}
		}
	}
	
	// Try dynamic port discovery by scanning range
	for port := 9222; port <= 9300; port++ {
		if cm.isPortOpen(port) && cm.verifyChromePID(port, pid) {
			return port, nil
		}
	}
	
	return 0, fmt.Errorf("could not find debug port for Chrome PID %d", pid)
}

// getProcessCommandLine gets the command line for a process (Windows-specific)
func (cm *ChromeManager) getProcessCommandLine(pid uint32) (string, error) {
	// This is a simplified approach. In a production system, you'd use WMI or 
	// read from /proc equivalent on Windows
	cmd := exec.Command("wmic", "process", "where", fmt.Sprintf("ProcessId=%d", pid), "get", "CommandLine", "/format:value")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "CommandLine=") {
			return strings.TrimPrefix(line, "CommandLine="), nil
		}
	}
	
	return "", fmt.Errorf("command line not found")
}

// extractPortFromCommandLine extracts debug port from Chrome command line
func (cm *ChromeManager) extractPortFromCommandLine(cmdLine string) int {
	re := regexp.MustCompile(`--remote-debugging-port=(\d+)`)
	matches := re.FindStringSubmatch(cmdLine)
	if len(matches) >= 2 {
		if port, err := strconv.Atoi(matches[1]); err == nil {
			return port
		}
	}
	return 0
}

// isPortOpen checks if a port is open locally
func (cm *ChromeManager) isPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// verifyChromePID verifies that the Chrome instance on a port matches the expected PID
func (cm *ChromeManager) verifyChromePID(port int, expectedPID uint32) bool {
	// This is simplified - you could make a request to /json/version and parse process info
	return true // For now, assume it matches
}

// getProfilePath gets the Chrome profile path for a process
func (cm *ChromeManager) getProfilePath(pid uint32) string {
	// This would require more complex logic to read Chrome's data directory
	// For now, return a placeholder
	return fmt.Sprintf("Profile for PID %d", pid)
}

// Version info structure for Chrome DevTools
type chromeVersionInfo struct {
	Browser   string `json:"Browser"`
	UserAgent string `json:"User-Agent"`
	V8Version string `json:"V8-Version"`
	WebKitVersion string `json:"WebKit-Version"`
}

// getVersionInfo gets Chrome version information
func (cm *ChromeManager) getVersionInfo(port int) (*chromeVersionInfo, error) {
	url := fmt.Sprintf("http://localhost:%d/json/version", port)
	
	resp, err := cm.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	var versionInfo chromeVersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&versionInfo); err != nil {
		return nil, err
	}
	
	return &versionInfo, nil
}

// handleWebSocketMessages handles WebSocket messages from Chrome DevTools
func (cm *ChromeManager) handleWebSocketMessages(conn *websocket.Conn, responses chan<- map[string]interface{}, errors chan<- error) {
	defer close(responses)
	
	for {
		var message map[string]interface{}
		if err := conn.ReadJSON(&message); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				errors <- err
			}
			return
		}
		
		select {
		case responses <- message:
		default:
			// Channel full, skip message
		}
	}
}

// takeScreenshot takes a screenshot using Chrome DevTools Protocol
func (cm *ChromeManager) takeScreenshot(conn *websocket.Conn, responses <-chan map[string]interface{}, options *types.CaptureOptions) (string, error) {
	// Prepare screenshot parameters
	params := map[string]interface{}{
		"format": "png",
		"fromSurface": false, // Allows capturing background tabs
	}
	
	if options != nil && options.Region != nil {
		params["clip"] = map[string]interface{}{
			"x":      options.Region.X,
			"y":      options.Region.Y,
			"width":  options.Region.Width,
			"height": options.Region.Height,
			"scale":  options.ScaleFactor,
		}
	}
	
	// Send screenshot command
	command := map[string]interface{}{
		"id":     1,
		"method": "Page.captureScreenshot",
		"params": params,
	}
	
	if err := conn.WriteJSON(command); err != nil {
		return "", fmt.Errorf("failed to send screenshot command: %w", err)
	}
	
	// Wait for response
	timeout := time.After(cm.timeout)
	for {
		select {
		case response, ok := <-responses:
			if !ok {
				return "", fmt.Errorf("connection closed while waiting for screenshot")
			}
			
			// Check if this is our screenshot response
			if id, exists := response["id"]; exists && id == 1 {
				if errorObj, exists := response["error"]; exists {
					return "", fmt.Errorf("Chrome DevTools error: %v", errorObj)
				}
				
				if result, exists := response["result"]; exists {
					if resultMap, ok := result.(map[string]interface{}); ok {
						if data, exists := resultMap["data"]; exists {
							if dataStr, ok := data.(string); ok {
								return dataStr, nil
							}
						}
					}
				}
				
				return "", fmt.Errorf("invalid screenshot response format")
			}
			
		case <-timeout:
			return "", fmt.Errorf("timeout waiting for screenshot response")
		}
	}
}

// executeScript executes JavaScript using Chrome DevTools Protocol
func (cm *ChromeManager) executeScript(conn *websocket.Conn, responses <-chan map[string]interface{}, script string) (interface{}, error) {
	// Send script execution command
	command := map[string]interface{}{
		"id":     2,
		"method": "Runtime.evaluate",
		"params": map[string]interface{}{
			"expression":    script,
			"returnByValue": true,
		},
	}
	
	if err := conn.WriteJSON(command); err != nil {
		return nil, fmt.Errorf("failed to send script command: %w", err)
	}
	
	// Wait for response
	timeout := time.After(cm.timeout)
	for {
		select {
		case response, ok := <-responses:
			if !ok {
				return nil, fmt.Errorf("connection closed while waiting for script result")
			}
			
			// Check if this is our script response
			if id, exists := response["id"]; exists && id == 2 {
				if errorObj, exists := response["error"]; exists {
					return nil, fmt.Errorf("Chrome DevTools error: %v", errorObj)
				}
				
				if result, exists := response["result"]; exists {
					if resultMap, ok := result.(map[string]interface{}); ok {
						if value, exists := resultMap["result"]; exists {
							if valueMap, ok := value.(map[string]interface{}); ok {
								if returnValue, exists := valueMap["value"]; exists {
									return returnValue, nil
								}
							}
						}
					}
				}
				
				return nil, fmt.Errorf("invalid script response format")
			}
			
		case <-timeout:
			return nil, fmt.Errorf("timeout waiting for script response")
		}
	}
}

// Close cleans up resources
func (cm *ChromeManager) Close() error {
	// Clear cache
	cm.instanceCache = make(map[uint32]*types.ChromeInstance)
	return nil
}

// Ensure we implement the interface
var _ types.ChromeManager = (*ChromeManager)(nil)