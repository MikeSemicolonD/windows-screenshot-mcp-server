package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
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

// Downscaling (max_width / scale) is the highest-leverage knob for agents: it
// cuts both encode time and payload size, so prefer it when you only need to
// see a window rather than read fine text at full resolution. The same two
// params below are added to every capture tool.
const (
	maxWidthDesc = "Downscale so the image is at most this many pixels wide, preserving aspect ratio (e.g. 1280). Smaller images encode faster and use far less payload. If you give no sizing at all (no max_width/scale/region) a default 1280px cap is applied to keep the round-trip fast; pass max_width: 0 to force full resolution."
	scaleDesc    = "Downscale factor between 0 and 1 (e.g. 0.5 = half width and height). If both scale and max_width are given, the smaller result wins. Never upscales."
	regionDesc   = "Optional sub-rectangle to keep, as [x, y, width, height] in pixels relative to the top-left of the capture. Cropped before any downscale, cutting the payload further. Omit to keep the whole capture."
)

func registerTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("capture_window_by_title",
		mcp.WithDescription("Capture a screenshot of a top-level window found by title. By default the title is matched case-insensitively as a substring (a \"lazy\" match): \"command\", \"prompt\", and \"Command Prompt\" all match a window titled \"Command Prompt\". When several windows match, the best target is captured (visible and non-minimized windows first, then an exact title match, then the shortest/closest title) and up to three other matches are listed in the result text so you can re-target (when more match, only the count is reported, so narrow the title or set exact=true to disambiguate). Two independent flags tighten the match: exact=true requires the title to equal the given string in full instead of just containing it; case_sensitive=true makes the comparison respect letter case. They combine freely (e.g. exact=true with case_sensitive=false is a full-title match ignoring case)."),
		mcp.WithString("title", mcp.Required(), mcp.Description("Window title to match. By default this is a case-insensitive substring (lazy match); see the exact and case_sensitive flags to tighten it.")),
		mcp.WithBoolean("exact", mcp.Description("Require the window title to equal the given string in full, instead of the default substring (contains) match. Default false.")),
		mcp.WithBoolean("case_sensitive", mcp.Description("Make the title comparison respect letter case. Default false (case-insensitive).")),
		mcp.WithString("format", mcp.Description("Image format: png, jpeg, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video)")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 90)")),
		mcp.WithBoolean("include_cursor", mcp.Description("Include the mouse cursor in the capture")),
		mcp.WithBoolean("gpu", mcp.Description("Use the GPU-accelerated Windows.Graphics.Capture path instead of PrintWindow/BitBlt. Reliably reproduces DirectComposition / hardware-rendered content. Requires Windows 10 1803+.")),
		mcp.WithNumber("max_width", mcp.Description(maxWidthDesc)),
		mcp.WithNumber("scale", mcp.Description(scaleDesc)),
		mcp.WithArray("region", mcp.Description(regionDesc), mcp.WithNumberItems()),
	), captureByTitle)

	s.AddTool(mcp.NewTool("capture_window_by_pid",
		mcp.WithDescription("Capture a screenshot of the main window of a process. If hidden=true, uses DWM thumbnail / PrintWindow fallbacks for invisible windows."),
		mcp.WithNumber("pid", mcp.Required(), mcp.Description("Process ID")),
		mcp.WithString("format", mcp.Description("png, jpeg, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video)")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
		mcp.WithBoolean("hidden", mcp.Description("Use the hidden-window capture path")),
		mcp.WithNumber("max_width", mcp.Description(maxWidthDesc)),
		mcp.WithNumber("scale", mcp.Description(scaleDesc)),
		mcp.WithArray("region", mcp.Description(regionDesc), mcp.WithNumberItems()),
	), captureByPID)

	s.AddTool(mcp.NewTool("capture_window_by_handle",
		mcp.WithDescription("Capture a screenshot of a window by its HWND."),
		mcp.WithNumber("handle", mcp.Required(), mcp.Description("Window handle (HWND) as integer")),
		mcp.WithString("format", mcp.Description("png, jpeg, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video)")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
		mcp.WithBoolean("gpu", mcp.Description("Use the GPU-accelerated Windows.Graphics.Capture path instead of PrintWindow/BitBlt. Reliably reproduces DirectComposition / hardware-rendered content. Requires Windows 10 1803+.")),
		mcp.WithNumber("max_width", mcp.Description(maxWidthDesc)),
		mcp.WithNumber("scale", mcp.Description(scaleDesc)),
		mcp.WithArray("region", mcp.Description(regionDesc), mcp.WithNumberItems()),
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
		mcp.WithBoolean("gpu", mcp.Description("Use the GPU-accelerated Windows.Graphics.Capture path. The burst reuses one capture session across all frames, so it is reasonably efficient; use it when you need DirectComposition / hardware-rendered content (Chromium, video, WinUI).")),
		mcp.WithNumber("max_width", mcp.Description(maxWidthDesc)),
		mcp.WithNumber("scale", mcp.Description(scaleDesc)),
		mcp.WithArray("region", mcp.Description(regionDesc), mcp.WithNumberItems()),
	), captureBurst)

	s.AddTool(mcp.NewTool("capture_window_by_class",
		mcp.WithDescription("Capture a screenshot of a window by its class name."),
		mcp.WithString("class_name", mcp.Required(), mcp.Description("Window class name")),
		mcp.WithString("format", mcp.Description("png, jpeg, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video)")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
		mcp.WithNumber("max_width", mcp.Description(maxWidthDesc)),
		mcp.WithNumber("scale", mcp.Description(scaleDesc)),
		mcp.WithArray("region", mcp.Description(regionDesc), mcp.WithNumberItems()),
	), captureByClass)

	s.AddTool(mcp.NewTool("capture_full_screen",
		mcp.WithDescription("Capture a screenshot of a single monitor in full. The monitor argument selects which display: 0 is the primary monitor (default), and higher indices are the remaining displays ordered left-to-right then top-to-bottom. An out-of-range index returns an error stating how many monitors are available."),
		mcp.WithNumber("monitor", mcp.Description("Zero-based monitor index: 0 = primary display (default), 1+ = additional displays ordered left-to-right then top-to-bottom")),
		mcp.WithString("format", mcp.Description("png, jpeg, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video)")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
		mcp.WithNumber("max_width", mcp.Description(maxWidthDesc)),
		mcp.WithNumber("scale", mcp.Description(scaleDesc)),
		mcp.WithArray("region", mcp.Description(regionDesc), mcp.WithNumberItems()),
	), captureFullScreen)

	s.AddTool(mcp.NewTool("list_chrome_tabs",
		mcp.WithDescription("List open tabs across all detected Chrome instances. Returns JSON array of {id,title,url,pid}."),
	), listChromeTabs)

	s.AddTool(mcp.NewTool("capture_chrome_tab",
		mcp.WithDescription("Capture a screenshot of a specific Chrome tab by tab ID (from list_chrome_tabs)."),
		mcp.WithString("tab_id", mcp.Required(), mcp.Description("Chrome tab ID")),
		mcp.WithString("format", mcp.Description("png, jpeg, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video)")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100")),
		mcp.WithNumber("max_width", mcp.Description(maxWidthDesc)),
		mcp.WithNumber("scale", mcp.Description(scaleDesc)),
		mcp.WithArray("region", mcp.Description(regionDesc), mcp.WithNumberItems()),
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

// outputOpts bundles the post-capture encoding choices shared by every capture
// tool: format/quality, an optional crop region, and optional downscaling.
type outputOpts struct {
	format   types.ImageFormat
	quality  int
	region   *types.Rectangle // nil = whole capture
	maxWidth int              // 0 = no width cap
	scale    float64          // 0 = no scale factor
}

// defaultMaxWidth caps the width of a capture when the caller specifies no
// sizing at all. Full-resolution images dominate the round-trip latency for an
// agent (transport + image tokens), and 1280px is plenty to read most UIs.
// Callers opt back into full resolution with max_width: 0.
const defaultMaxWidth = 1280

// outputFromArgs reads the shared output arguments. When format is omitted it
// resolves to FormatAuto, letting the encoder pick PNG (flat UI) or JPEG
// (photographic/video) by sampling the image. defaultFormat overrides that
// fallback (burst passes JPEG to keep its multi-image payload small).
//
// If the caller gives no sizing (no max_width, scale, or region), a default
// max_width cap is applied so payloads stay small by default; max_width: 0
// explicitly requests full resolution.
func outputFromArgs(args map[string]any, defaultFormat types.ImageFormat) outputOpts {
	o := outputOpts{
		quality: int(argInt64(args, "quality", 90)),
		region:  regionFromArgs(args),
	}
	// Only a scale in the documented (0,1) range counts as sizing. The encoder
	// ignores out-of-range values, so accepting e.g. scale: 2 here would both
	// skip the resize and suppress the default cap below — yielding an
	// unintended full-resolution payload. Treat anything else as unset.
	if v, ok := args["scale"].(float64); ok && v > 0 && v < 1 {
		o.scale = v
	}
	_, hasMaxWidth := args["max_width"]
	switch {
	case hasMaxWidth:
		o.maxWidth = int(argInt64(args, "max_width", 0)) // honor explicitly, 0 = full res
	case o.scale == 0 && o.region == nil:
		o.maxWidth = defaultMaxWidth // no sizing given → sane default cap
	}
	switch strings.ToLower(argString(args, "format", "")) {
	case "jpeg", "jpg":
		o.format = types.FormatJPEG
	case "png":
		o.format = types.FormatPNG
	default:
		o.format = defaultFormat
	}
	return o
}

// regionFromArgs parses the optional region crop, given as [x, y, width, height]
// in capture pixels. Returns nil when absent or malformed.
func regionFromArgs(args map[string]any) *types.Rectangle {
	raw, ok := args["region"].([]any)
	if !ok || len(raw) < 4 {
		return nil
	}
	toInt := func(v any) int {
		if f, ok := v.(float64); ok {
			return int(f)
		}
		return 0
	}
	w, h := toInt(raw[2]), toInt(raw[3])
	if w <= 0 || h <= 0 {
		return nil
	}
	return &types.Rectangle{X: toInt(raw[0]), Y: toInt(raw[1]), Width: w, Height: h}
}

// mimeFor maps a concrete image format to its MIME type.
func mimeFor(format types.ImageFormat) string {
	if format == types.FormatJPEG {
		return "image/jpeg"
	}
	return "image/png"
}

// imageResult encodes a buffer (optionally cropped/downscaled, format auto-picked)
// and returns it as an MCP image + summary text.
func imageResult(buffer *types.ScreenshotBuffer, label string, out outputOpts) (*mcp.CallToolResult, error) {
	encoded, w, h, chosen, err := imgProc.EncodeScaled(buffer, out.format, out.quality, out.region, out.maxWidth, out.scale)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("encode failed", err), nil
	}
	mime := mimeFor(chosen)
	b64 := base64.StdEncoding.EncodeToString(encoded)

	dims := fmt.Sprintf("%dx%d", w, h)
	if w != buffer.Width || h != buffer.Height {
		dims = fmt.Sprintf("%dx%d (from %dx%d capture)", w, h, buffer.Width, buffer.Height)
	}
	summary := fmt.Sprintf("Captured %s — %s, %d bytes (%s)", label, dims, len(encoded), mime)

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

const (
	// captureTimeout bounds a single capture. PrintWindow / BitBlt are dispatched
	// to the target window's UI thread and block indefinitely if that thread is
	// hung or the window is mid-teardown; the watchdog turns that freeze into a
	// recoverable error.
	captureTimeout = 10 * time.Second
	// burstFrameTimeout bounds each frame of a burst (shorter, so a stuck window
	// is noticed quickly and the burst can abort rather than hang per frame).
	burstFrameTimeout = 6 * time.Second
	// burstAbortAfter stops a burst once this many consecutive frames fail, so a
	// window that closes mid-burst yields one quick error instead of N timeouts.
	burstAbortAfter = 3
	// maxInFlightCaptures bounds how many blocking capture syscalls may run at
	// once. A capture that overruns its deadline is abandoned in its goroutine,
	// but the underlying syscall cannot be cancelled, so that goroutine stays
	// parked until (if ever) the syscall returns. This cap is therefore also the
	// ceiling on how many such stuck goroutines can accumulate: a slot is held
	// until the syscall actually returns, and once all slots are taken new
	// captures fail fast instead of piling up unbounded.
	maxInFlightCaptures = 8
)

// captureSlots is the concurrency limiter behind maxInFlightCaptures. A token is
// held for the full lifetime of the blocking capture goroutine (released only
// when the syscall returns), not merely until captureWithTimeout returns.
var captureSlots = make(chan struct{}, maxInFlightCaptures)

// captureWithTimeout runs a blocking capture under a deadline. If the capture
// overruns, it returns a timeout error and abandons the work in its goroutine —
// the underlying syscall cannot be cancelled, but the tool stays responsive
// instead of freezing. A panic in the capture is recovered into an error so a
// bad handle can never crash the server. The channel is buffered so the
// abandoned goroutine can still send (and exit) once the syscall returns.
//
// A maxInFlightCaptures semaphore bounds how many such (possibly stuck)
// goroutines can exist at once; when it is saturated the call fails fast rather
// than launching another goroutine that might never return.
func captureWithTimeout(d time.Duration, fn func() (*types.ScreenshotBuffer, error)) (*types.ScreenshotBuffer, error) {
	select {
	case captureSlots <- struct{}{}:
		// slot acquired; released by the goroutine below once fn actually returns
	default:
		return nil, fmt.Errorf("too many captures in flight (%d) — a target window is likely hung; retry shortly or restart the server", maxInFlightCaptures)
	}

	type result struct {
		buf *types.ScreenshotBuffer
		err error
	}
	ch := make(chan result, 1)
	go func() {
		// Release the slot only when the (uncancellable) capture actually
		// returns, so a goroutine still parked after a timeout keeps holding its
		// slot — that is what makes the cap bound total stuck goroutines.
		defer func() { <-captureSlots }()
		defer func() {
			if r := recover(); r != nil {
				ch <- result{nil, fmt.Errorf("capture panicked: %v", r)}
			}
		}()
		buf, err := fn()
		ch <- result{buf, err}
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.buf, r.err
	case <-timer.C:
		return nil, fmt.Errorf("capture timed out after %s — the target window is unresponsive or closing", d)
	}
}

// guardCapture wraps a single capture with the standard watchdog timeout.
func guardCapture(fn func() (*types.ScreenshotBuffer, error)) (*types.ScreenshotBuffer, error) {
	return captureWithTimeout(captureTimeout, fn)
}

// captureErr turns a capture failure into an MCP error, distinguishing the
// common case where the window was closed mid-capture (so the message is
// actionable) from other failures. handle may be 0 when it is not known.
func captureErr(handle uintptr, err error) *mcp.CallToolResult {
	if handle != 0 && !engine.WindowExists(handle) {
		return mcp.NewToolResultError(fmt.Sprintf("the target window (hwnd 0x%x) was closed during the capture", handle))
	}
	return mcp.NewToolResultErrorFromErr("capture failed", err)
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
	out := outputFromArgs(args, types.FormatAuto)

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
		buf, err = guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureGPU(target.Handle, opts) })
	} else {
		buf, err = guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureByHandle(target.Handle, opts) })
	}
	if err != nil {
		return captureErr(target.Handle, err), nil
	}

	label := fmt.Sprintf("window %q (%s for %q)", target.Title, mode, title)
	if useGPU {
		label += " [GPU]"
	}
	if others := len(matches) - 1; others > 0 {
		// Listing every match by title clutters the chat once there are many, so
		// only enumerate them when there are a handful; beyond that report just the
		// count and how to disambiguate, omitting the names.
		const maxList = 3
		if others <= maxList {
			named := make([]string, 0, others)
			for _, m := range matches[1:] {
				named = append(named, fmt.Sprintf("%q [hwnd 0x%x]", m.Title, m.Handle))
			}
			label = fmt.Sprintf("%s — %d other window(s) also matched: %s",
				label, others, strings.Join(named, ", "))
		} else {
			label = fmt.Sprintf("%s — %d other windows also matched (narrow the title or set exact=true to disambiguate)",
				label, others)
		}
	}
	return imageResult(buf, label, out)
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
		buf, err = guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureHiddenByPID(pid, opts) })
	} else {
		buf, err = guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureByPID(pid, opts) })
	}
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	out := outputFromArgs(args, types.FormatAuto)
	return imageResult(buf, fmt.Sprintf("pid %d", pid), out)
}

func captureByHandle(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	handle := uintptr(argInt64(args, "handle", 0))
	if handle == 0 {
		return mcp.NewToolResultError("handle is required"), nil
	}
	if !engine.WindowExists(handle) {
		return mcp.NewToolResultError(fmt.Sprintf("no live window with handle 0x%x (it may have been closed)", handle)), nil
	}
	opts := defaultOptions()
	var buf *types.ScreenshotBuffer
	var err error
	label := fmt.Sprintf("hwnd 0x%x", handle)
	if argBool(args, "gpu", false) {
		buf, err = guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureGPU(handle, opts) })
		label += " [GPU]"
	} else {
		buf, err = guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureByHandle(handle, opts) })
	}
	if err != nil {
		return captureErr(handle, err), nil
	}
	out := outputFromArgs(args, types.FormatAuto)
	return imageResult(buf, label, out)
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
	if !engine.WindowExists(handle) {
		return mcp.NewToolResultError(fmt.Sprintf("no live window for %s (it may have been closed)", targetLabel)), nil
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

	// Burst defaults to JPEG to keep the multi-image payload manageable, and keeps
	// the format concrete (no auto-pick) so every frame encodes identically.
	out := outputFromArgs(args, types.FormatJPEG)
	out.quality = int(argInt64(args, "quality", 80))
	mime := mimeFor(out.format)

	opts := defaultOptions()
	opts.IncludeCursor = argBool(args, "include_cursor", false)
	useGPU := argBool(args, "gpu", false)

	// For a GPU burst, build one capture session and reuse it across frames —
	// CaptureGPU would otherwise rebuild the whole Direct3D pipeline per frame.
	// The session locks this goroutine's OS thread until Close, which is fine:
	// every Capture() call below runs on this same goroutine.
	var gpuSess types.GPUCaptureSession
	if useGPU {
		s, err := engine.NewGPUSession(handle, opts)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("gpu session failed", err), nil
		}
		gpuSess = s
		defer gpuSess.Close()
	}

	// Capture is paced sequentially on this goroutine to keep the cadence
	// accurate, but each frame's encode + base64 (the expensive part) is handed
	// to a worker so it overlaps the wait before the next capture. Every frame
	// owns its own buffer, so the in-place BGRA conversion is safe in parallel.
	type frame struct {
		b64  string
		size int
		w, h int
	}
	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		encoded      = make(map[int]frame, count)
		failures     []string
		origW, origH int
	)
	addFailure := func(s string) {
		mu.Lock()
		failures = append(failures, s)
		mu.Unlock()
	}

	var (
		consecutiveFails int
		windowClosed     bool
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
					addFailure("cancelled before all frames were captured")
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
			// The GPU session is bound to this goroutine's locked OS thread, so it
			// is captured inline; its frame wait is already internally bounded.
			buf, err = gpuSess.Capture()
		} else {
			buf, err = captureWithTimeout(burstFrameTimeout, func() (*types.ScreenshotBuffer, error) {
				return engine.CaptureByHandle(handle, opts)
			})
		}
		if err != nil {
			// If the window has gone, stop immediately with a clear reason rather
			// than timing out on every remaining frame.
			if !engine.WindowExists(handle) {
				windowClosed = true
				addFailure(fmt.Sprintf("frame %d: target window was closed", i+1))
				goto done
			}
			addFailure(fmt.Sprintf("frame %d: %v", i+1, err))
			consecutiveFails++
			if consecutiveFails >= burstAbortAfter {
				addFailure(fmt.Sprintf("aborted after %d consecutive failures (window unresponsive)", consecutiveFails))
				goto done
			}
			continue
		}
		consecutiveFails = 0
		if origW == 0 {
			origW, origH = buf.Width, buf.Height
		}

		wg.Add(1)
		go func(idx int, b *types.ScreenshotBuffer) {
			defer wg.Done()
			enc, w, h, _, err := imgProc.EncodeScaled(b, out.format, out.quality, out.region, out.maxWidth, out.scale)
			if err != nil {
				addFailure(fmt.Sprintf("frame %d encode: %v", idx+1, err))
				return
			}
			f := frame{b64: base64.StdEncoding.EncodeToString(enc), size: len(enc), w: w, h: h}
			mu.Lock()
			encoded[idx] = f
			mu.Unlock()
		}(i, buf)
	}
done:
	wg.Wait()

	// Assemble frames in capture order.
	contents := make([]mcp.Content, 0, len(encoded)+1)
	var totalBytes, finalW, finalH int
	for i := 0; i < count; i++ {
		f, ok := encoded[i]
		if !ok {
			continue
		}
		contents = append(contents, mcp.NewImageContent(f.b64, mime))
		totalBytes += f.size
		finalW, finalH = f.w, f.h
	}
	captured := len(contents)

	if captured == 0 {
		msg := "burst captured no frames"
		if windowClosed {
			msg = "burst captured no frames — the target window was closed before any frame was captured"
		}
		if len(failures) > 0 {
			msg += ": " + strings.Join(failures, "; ")
		}
		return mcp.NewToolResultError(msg), nil
	}

	dims := fmt.Sprintf("%dx%d", finalW, finalH)
	if finalW != origW || finalH != origH {
		dims = fmt.Sprintf("%dx%d (from %dx%d capture)", finalW, finalH, origW, origH)
	}
	summary := fmt.Sprintf("Burst of %s — %d frame(s)%s at %s over %s, ~%dms apart, %d bytes total (%s)",
		targetLabel, captured, noteAdjusted, dims, time.Since(start).Round(time.Millisecond),
		interval.Milliseconds(), totalBytes, mime)
	if windowClosed {
		summary += fmt.Sprintf(" — stopped early: the target window was closed after %d frame(s)", captured)
	}
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
	buf, err := guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureByClassName(class, defaultOptions()) })
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	out := outputFromArgs(args, types.FormatAuto)
	return imageResult(buf, fmt.Sprintf("class %q", class), out)
}

func captureFullScreen(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	monitor := int(argInt64(args, "monitor", 0))
	buf, err := guardCapture(func() (*types.ScreenshotBuffer, error) { return engine.CaptureFullScreen(monitor, defaultOptions()) })
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	out := outputFromArgs(args, types.FormatAuto)
	return imageResult(buf, fmt.Sprintf("monitor %d", monitor), out)
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

	buf, err := captureWithTimeout(captureTimeout, func() (*types.ScreenshotBuffer, error) {
		return chromeMgr.CaptureTab(target, defaultOptions())
	})
	if err != nil {
		return mcp.NewToolResultErrorFromErr("capture failed", err), nil
	}
	out := outputFromArgs(args, types.FormatAuto)
	return imageResult(buf, fmt.Sprintf("chrome tab %q", target.Title), out)
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
