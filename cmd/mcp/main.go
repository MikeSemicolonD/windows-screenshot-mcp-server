package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

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
		mcp.WithDescription("Capture a screenshot of a top-level window found by title. By default the title is matched case-insensitively as a substring (a \"lazy\" match): \"command\", \"prompt\", and \"Command Prompt\" all match a window titled \"Command Prompt\". When several windows match, the best target is captured (visible and non-minimized windows first, then an exact title match, then the shortest/closest title) and any other matches are listed in the result text so you can re-target. Two independent flags tighten the match: exact=true requires the title to equal the given string in full instead of just containing it; case_sensitive=true makes the comparison respect letter case. They combine freely (e.g. exact=true with case_sensitive=false is a full-title match ignoring case)."),
		mcp.WithString("title", mcp.Required(), mcp.Description("Window title to match. By default this is a case-insensitive substring (lazy match); see the exact and case_sensitive flags to tighten it.")),
		mcp.WithBoolean("exact", mcp.Description("Require the window title to equal the given string in full, instead of the default substring (contains) match. Default false.")),
		mcp.WithBoolean("case_sensitive", mcp.Description("Make the title comparison respect letter case. Default false (case-insensitive).")),
		mcp.WithString("format", mcp.Description("Image format: png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 90)")),
		mcp.WithBoolean("include_cursor", mcp.Description("Include the mouse cursor in the capture")),
		mcp.WithBoolean("gpu", mcp.Description("Use the GPU-accelerated Windows.Graphics.Capture path instead of PrintWindow/BitBlt. Reliably reproduces DirectComposition / hardware-rendered content. Requires Windows 10 1803+.")),
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
		mcp.WithBoolean("gpu", mcp.Description("Use the GPU-accelerated Windows.Graphics.Capture path instead of PrintWindow/BitBlt. Reliably reproduces DirectComposition / hardware-rendered content. Requires Windows 10 1803+.")),
	), captureByHandle)

	s.AddTool(mcp.NewTool("capture_burst",
		mcp.WithDescription("Capture a rapid series of screenshots of one window over time, returning every frame as a separate image. Useful for watching a window change (animations, progress, flicker). Target the window with either title (case-insensitive substring, same matching as capture_window_by_title) or handle (HWND). Throttling: interval_ms is the spacing between frames (minimum 100ms is enforced so the target is not hammered) and count is how many frames to take. Total capture time is interval_ms*count and is capped at 30 seconds — if the request exceeds that, count is reduced and the result text notes the adjustment. Defaults to JPEG to keep the multi-image payload small; pass format=png for lossless frames."),
		mcp.WithString("title", mcp.Description("Window title to match (case-insensitive substring). Provide this or handle.")),
		mcp.WithNumber("handle", mcp.Description("Window handle (HWND) as integer. Provide this or title.")),
		mcp.WithNumber("count", mcp.Description("Number of frames to capture (default 5). Combined with interval_ms it is capped so total time stays within 30s.")),
		mcp.WithNumber("interval_ms", mcp.Description("Milliseconds between frames (default 500, minimum 100). Acts as the throttle so captures are not taken too fast.")),
		mcp.WithString("format", mcp.Description("png or jpeg (default jpeg for burst, to keep the payload small)")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 80)")),
		mcp.WithBoolean("include_cursor", mcp.Description("Include the mouse cursor in each frame")),
		mcp.WithBoolean("gpu", mcp.Description("Use the GPU-accelerated Windows.Graphics.Capture path. Note: this re-initializes per frame and is slower for bursts; prefer leaving it off unless you need DirectComposition content.")),
	), captureBurst)

	s.AddTool(mcp.NewTool("capture_window_by_class",
		mcp.WithDescription("Capture a screenshot of a window by its class name."),
		mcp.WithString("class_name", mcp.Required(), mcp.Description("Window class name")),
		mcp.WithString("format", mcp.Description("png (default) or jpeg")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
	), captureByClass)

	s.AddTool(mcp.NewTool("capture_full_screen",
		mcp.WithDescription("Capture a screenshot of a single monitor in full. The monitor argument selects which display: 0 is the primary monitor (default), and higher indices are the remaining displays ordered left-to-right then top-to-bottom. An out-of-range index returns an error stating how many monitors are available."),
		mcp.WithNumber("monitor", mcp.Description("Zero-based monitor index: 0 = primary display (default), 1+ = additional displays ordered left-to-right then top-to-bottom")),
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

// titleMatchDesc describes a title match mode in plain words, e.g.
// "case-insensitive substring match" or "case-sensitive exact match".
func titleMatchDesc(caseSensitive, exact bool) string {
	cs := "case-insensitive"
	if caseSensitive {
		cs = "case-sensitive"
	}
	kind := "substring"
	if exact {
		kind = "exact"
	}
	return cs + " " + kind + " match"
}

func captureByTitle(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	title := argString(args, "title", "")
	if title == "" {
		return mcp.NewToolResultError("title is required"), nil
	}
	opts := defaultOptions()
	opts.IncludeCursor = argBool(args, "include_cursor", false)
	format, quality := captureFormat(args)

	exact := argBool(args, "exact", false)
	caseSensitive := argBool(args, "case_sensitive", false)
	mode := titleMatchDesc(caseSensitive, exact)

	matches, err := engine.FindWindowsByTitle(title, caseSensitive, exact)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("window search failed", err), nil
	}
	if len(matches) == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("no top-level window title matched %q (%s)", title, mode)), nil
	}

	target := matches[0]
	useGPU := argBool(args, "gpu", false)
	var buf *types.ScreenshotBuffer
	if useGPU {
		buf, err = engine.CaptureGPU(target.Handle, opts)
	} else {
		buf, err = engine.CaptureByHandle(target.Handle, opts)
	}
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}

	label := fmt.Sprintf("window %q (%s for %q)", target.Title, mode, title)
	if useGPU {
		label += " [GPU]"
	}
	if len(matches) > 1 {
		const maxList = 5
		others := make([]string, 0, maxList)
		for _, m := range matches[1:] {
			if len(others) == maxList {
				break
			}
			others = append(others, fmt.Sprintf("%q [hwnd 0x%x]", m.Title, m.Handle))
		}
		more := ""
		if len(matches)-1 > len(others) {
			more = fmt.Sprintf(" (+%d more)", len(matches)-1-len(others))
		}
		label = fmt.Sprintf("%s — %d other window(s) also matched: %s%s",
			label, len(matches)-1, strings.Join(others, ", "), more)
	}
	return imageResult(buf, label, format, quality)
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
	var buf *types.ScreenshotBuffer
	var err error
	label := fmt.Sprintf("hwnd 0x%x", handle)
	if argBool(args, "gpu", false) {
		buf, err = engine.CaptureGPU(handle, defaultOptions())
		label += " [GPU]"
	} else {
		buf, err = engine.CaptureByHandle(handle, defaultOptions())
	}
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	format, quality := captureFormat(args)
	return imageResult(buf, label, format, quality)
}

const (
	burstMaxTotal    = 30 * time.Second
	burstMinInterval = 100 * time.Millisecond
)

func captureBurst(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	// Resolve the target window once up front so we don't re-enumerate windows
	// on every frame — that keeps the per-frame cost to just capture + encode.
	title := argString(args, "title", "")
	handle := uintptr(argInt64(args, "handle", 0))
	if title == "" && handle == 0 {
		return mcp.NewToolResultError("provide either title or handle"), nil
	}
	var targetLabel string
	if handle == 0 {
		matches, err := engine.FindWindowsByTitle(title, false, false)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("window search failed", err), nil
		}
		if len(matches) == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("no top-level window title matched %q", title)), nil
		}
		handle = matches[0].Handle
		targetLabel = fmt.Sprintf("window %q", matches[0].Title)
	} else {
		targetLabel = fmt.Sprintf("hwnd 0x%x", handle)
	}

	// Throttle and frame count, capped so total capture time stays within 30s.
	interval := time.Duration(argInt64(args, "interval_ms", 500)) * time.Millisecond
	if interval < burstMinInterval {
		interval = burstMinInterval
	}
	count := int(argInt64(args, "count", 5))
	if count < 1 {
		count = 1
	}
	var noteAdjusted string
	if maxCount := int(burstMaxTotal / interval); maxCount >= 1 && count > maxCount {
		noteAdjusted = fmt.Sprintf(" (reduced from %d to fit the 30s cap)", count)
		count = maxCount
	}

	// Burst defaults to JPEG q80 to keep the multi-image payload manageable.
	format := types.FormatJPEG
	mime := "image/jpeg"
	if strings.EqualFold(argString(args, "format", "jpeg"), "png") {
		format = types.FormatPNG
		mime = "image/png"
	}
	quality := int(argInt64(args, "quality", 80))

	opts := defaultOptions()
	opts.IncludeCursor = argBool(args, "include_cursor", false)
	useGPU := argBool(args, "gpu", false)

	contents := make([]mcp.Content, 0, count+1)
	var (
		captured   int
		totalBytes int
		failures   []string
	)
	start := time.Now()
	for i := 0; i < count; i++ {
		// Pace frames against an absolute schedule so capture/encode time does
		// not let the cadence drift. The first frame is taken immediately.
		if i > 0 {
			if d := time.Until(start.Add(time.Duration(i) * interval)); d > 0 {
				timer := time.NewTimer(d)
				select {
				case <-ctx.Done():
					timer.Stop()
					failures = append(failures, "cancelled before all frames were captured")
					goto done
				case <-timer.C:
				}
			}
		}

		var (
			buf *types.ScreenshotBuffer
			err error
		)
		if useGPU {
			buf, err = engine.CaptureGPU(handle, opts)
		} else {
			buf, err = engine.CaptureByHandle(handle, opts)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("frame %d: %v", i+1, err))
			continue
		}
		encoded, err := imgProc.Encode(buf, format, quality)
		if err != nil {
			failures = append(failures, fmt.Sprintf("frame %d encode: %v", i+1, err))
			continue
		}
		contents = append(contents, mcp.NewImageContent(base64.StdEncoding.EncodeToString(encoded), mime))
		captured++
		totalBytes += len(encoded)
	}
done:

	if captured == 0 {
		msg := "burst captured no frames"
		if len(failures) > 0 {
			msg += ": " + strings.Join(failures, "; ")
		}
		return mcp.NewToolResultError(msg), nil
	}

	summary := fmt.Sprintf("Burst of %s — %d frame(s)%s over %s, ~%dms apart, %d bytes total (%s)",
		targetLabel, captured, noteAdjusted, time.Since(start).Round(time.Millisecond),
		interval.Milliseconds(), totalBytes, mime)
	if len(failures) > 0 {
		summary += fmt.Sprintf(" — %d issue(s): %s", len(failures), strings.Join(failures, "; "))
	}

	return &mcp.CallToolResult{
		Content: append([]mcp.Content{mcp.NewTextContent(summary)}, contents...),
	}, nil
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
