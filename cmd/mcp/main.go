package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/screenshot-mcp-server/internal/chrome"
	"github.com/screenshot-mcp-server/internal/screenshot"
	"github.com/screenshot-mcp-server/pkg/types"
)

var (
	engine    types.ScreenshotEngine
	chromeMgr types.ChromeManager
	imgProc   *screenshot.ImageProcessor
)

func main() {
	var err error
	engine, err = screenshot.NewEngine()
	if err != nil {
		log.Fatalf("failed to init screenshot engine: %v", err)
	}
	chromeMgr = chrome.NewManager()
	imgProc = screenshot.NewImageProcessor()

	s := server.NewMCPServer(
		"windows-screenshot",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	registerTools(s)

	// stderr-only logging — stdout is reserved for MCP framing.
	log.SetOutput(os.Stderr)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("mcp stdio server error: %v", err)
	}
}

func registerTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("capture_window_by_title",
		mcp.WithDescription("Capture a screenshot of a top-level window whose title contains the given substring (case-sensitive)."),
		mcp.WithString("title", mcp.Required(), mcp.Description("Window title or substring to match")),
		mcp.WithString("format", mcp.Description("Image format: png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 90)")),
		mcp.WithBoolean("include_cursor", mcp.Description("Include the mouse cursor in the capture")),
	), captureByTitle)

	s.AddTool(mcp.NewTool("capture_window_by_pid",
		mcp.WithDescription("Capture a screenshot of the main window of a process. If hidden=true, uses DWM thumbnail / PrintWindow fallbacks for invisible windows."),
		mcp.WithNumber("pid", mcp.Required(), mcp.Description("Process ID")),
		mcp.WithString("format", mcp.Description("png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
		mcp.WithBoolean("hidden", mcp.Description("Use the hidden-window capture path")),
	), captureByPID)

	s.AddTool(mcp.NewTool("capture_window_by_handle",
		mcp.WithDescription("Capture a screenshot of a window by its HWND."),
		mcp.WithNumber("handle", mcp.Required(), mcp.Description("Window handle (HWND) as integer")),
		mcp.WithString("format", mcp.Description("png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
	), captureByHandle)

	s.AddTool(mcp.NewTool("capture_window_by_class",
		mcp.WithDescription("Capture a screenshot of a window by its class name."),
		mcp.WithString("class_name", mcp.Required(), mcp.Description("Window class name")),
		mcp.WithString("format", mcp.Description("png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
	), captureByClass)

	s.AddTool(mcp.NewTool("capture_full_screen",
		mcp.WithDescription("Capture a full monitor."),
		mcp.WithNumber("monitor", mcp.Description("Monitor index (0 = primary, default 0)")),
		mcp.WithString("format", mcp.Description("png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
	), captureFullScreen)

	s.AddTool(mcp.NewTool("list_chrome_tabs",
		mcp.WithDescription("List open tabs across all detected Chrome instances. Returns JSON array of {id,title,url,pid}."),
	), listChromeTabs)

	s.AddTool(mcp.NewTool("capture_chrome_tab",
		mcp.WithDescription("Capture a screenshot of a specific Chrome tab by tab ID (from list_chrome_tabs)."),
		mcp.WithString("tab_id", mcp.Required(), mcp.Description("Chrome tab ID")),
		mcp.WithString("format", mcp.Description("png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
	), captureChromeTab)

	s.AddTool(mcp.NewTool("find_tray_apps",
		mcp.WithDescription("Enumerate system tray applications (visible and overflow). Returns JSON window info list."),
	), findTrayApps)

	s.AddTool(mcp.NewTool("find_hidden_windows",
		mcp.WithDescription("Enumerate hidden top-level windows. Returns JSON window info list."),
	), findHiddenWindows)

	s.AddTool(mcp.NewTool("enumerate_process_windows",
		mcp.WithDescription("List all windows owned by a process (including hidden/cloaked). Returns JSON window info list."),
		mcp.WithNumber("pid", mcp.Required(), mcp.Description("Process ID")),
	), enumerateProcessWindows)
}

// --- argument helpers ---

func argString(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

func argBool(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func argInt64(args map[string]any, key string, def int64) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
	}
	return def
}

// captureFormat resolves the requested format and quality from args.
func captureFormat(args map[string]any) (types.ImageFormat, int) {
	format := strings.ToLower(argString(args, "format", "png"))
	quality := int(argInt64(args, "quality", 90))
	switch format {
	case "jpeg", "jpg":
		return types.FormatJPEG, quality
	default:
		return types.FormatPNG, quality
	}
}

// imageResult encodes a buffer and returns it as MCP image + summary text.
func imageResult(buffer *types.ScreenshotBuffer, label string, format types.ImageFormat, quality int) (*mcp.CallToolResult, error) {
	encoded, err := imgProc.Encode(buffer, format, quality)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("encode failed", err), nil
	}
	mime := "image/png"
	if format == types.FormatJPEG {
		mime = "image/jpeg"
	}
	b64 := base64.StdEncoding.EncodeToString(encoded)

	summary := fmt.Sprintf("Captured %s — %dx%d, %d bytes (%s)", label, buffer.Width, buffer.Height, len(encoded), mime)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(summary),
			mcp.NewImageContent(b64, mime),
		},
	}, nil
}

func defaultOptions() *types.CaptureOptions {
	opts := types.DefaultCaptureOptions()
	opts.AllowMinimized = true
	return opts
}

// --- tool handlers ---

func captureByTitle(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	title := argString(args, "title", "")
	if title == "" {
		return mcp.NewToolResultError("title is required"), nil
	}
	opts := defaultOptions()
	opts.IncludeCursor = argBool(args, "include_cursor", false)

	buf, err := engine.CaptureByTitle(title, opts)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	format, quality := captureFormat(args)
	return imageResult(buf, fmt.Sprintf("window %q", title), format, quality)
}

func captureByPID(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	pid := uint32(argInt64(args, "pid", 0))
	if pid == 0 {
		return mcp.NewToolResultError("pid is required"), nil
	}
	opts := defaultOptions()

	var buf *types.ScreenshotBuffer
	var err error
	if argBool(args, "hidden", false) {
		buf, err = engine.CaptureHiddenByPID(pid, opts)
	} else {
		buf, err = engine.CaptureByPID(pid, opts)
	}
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	format, quality := captureFormat(args)
	return imageResult(buf, fmt.Sprintf("pid %d", pid), format, quality)
}

func captureByHandle(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	handle := uintptr(argInt64(args, "handle", 0))
	if handle == 0 {
		return mcp.NewToolResultError("handle is required"), nil
	}
	buf, err := engine.CaptureByHandle(handle, defaultOptions())
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	format, quality := captureFormat(args)
	return imageResult(buf, fmt.Sprintf("hwnd 0x%x", handle), format, quality)
}

func captureByClass(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	class := argString(args, "class_name", "")
	if class == "" {
		return mcp.NewToolResultError("class_name is required"), nil
	}
	buf, err := engine.CaptureByClassName(class, defaultOptions())
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	format, quality := captureFormat(args)
	return imageResult(buf, fmt.Sprintf("class %q", class), format, quality)
}

func captureFullScreen(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	monitor := int(argInt64(args, "monitor", 0))
	buf, err := engine.CaptureFullScreen(monitor, defaultOptions())
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	format, quality := captureFormat(args)
	return imageResult(buf, fmt.Sprintf("monitor %d", monitor), format, quality)
}

func listChromeTabs(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	instances, err := chromeMgr.DiscoverInstances()
	if err != nil {
		return mcp.NewToolResultErrorFromErr("discover chrome failed", err), nil
	}
	var allTabs []types.ChromeTab
	for i := range instances {
		tabs, err := chromeMgr.GetTabs(&instances[i])
		if err != nil {
			continue
		}
		allTabs = append(allTabs, tabs...)
	}
	out, err := json.MarshalIndent(map[string]any{
		"count": len(allTabs),
		"tabs":  allTabs,
	}, "", "  ")
	if err != nil {
		return mcp.NewToolResultErrorFromErr("marshal failed", err), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

func captureChromeTab(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	tabID := argString(args, "tab_id", "")
	if tabID == "" {
		return mcp.NewToolResultError("tab_id is required"), nil
	}

	instances, err := chromeMgr.DiscoverInstances()
	if err != nil {
		return mcp.NewToolResultErrorFromErr("discover chrome failed", err), nil
	}
	var target *types.ChromeTab
	for i := range instances {
		tabs, err := chromeMgr.GetTabs(&instances[i])
		if err != nil {
			continue
		}
		for j := range tabs {
			if tabs[j].ID == tabID {
				target = &tabs[j]
				break
			}
		}
		if target != nil {
			break
		}
	}
	if target == nil {
		return mcp.NewToolResultError(fmt.Sprintf("tab %q not found", tabID)), nil
	}

	buf, err := chromeMgr.CaptureTab(target, defaultOptions())
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	format, quality := captureFormat(args)
	return imageResult(buf, fmt.Sprintf("chrome tab %q", target.Title), format, quality)
}

func findTrayApps(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	wins, err := engine.FindSystemTrayApps()
	if err != nil {
		return mcp.NewToolResultErrorFromErr("enum tray failed", err), nil
	}
	return jsonResult(wins)
}

func findHiddenWindows(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	wins, err := engine.FindHiddenWindows()
	if err != nil {
		return mcp.NewToolResultErrorFromErr("enum hidden failed", err), nil
	}
	return jsonResult(wins)
}

func enumerateProcessWindows(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	pid := uint32(argInt64(args, "pid", 0))
	if pid == 0 {
		return mcp.NewToolResultError("pid is required"), nil
	}
	wins, err := engine.EnumerateAllProcessWindows(pid)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("enum windows failed", err), nil
	}
	return jsonResult(wins)
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultErrorFromErr("marshal failed", err), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}
