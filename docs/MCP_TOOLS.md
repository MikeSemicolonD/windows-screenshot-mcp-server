# MCP Tools Reference

The `windows-screenshot` MCP server (`cmd/mcp`, built as `screenshot-mcp.exe`) speaks the
Model Context Protocol over stdio and exposes the eleven tools below. It is the complete,
recommended interface to this project — every capability is available here.

## Configuration

Register the server with any MCP client. Example `.mcp.json`:

```json
{
  "mcpServers": {
    "windows-screenshot": {
      "command": "..\\windows-screenshot-mcp-server\\screenshot-mcp.exe",
      "type": "stdio"
    }
  }
}
```

Build the binary first: `go build -o screenshot-mcp.exe ./cmd/mcp`

## Common conventions

- **`format`** — `png`, `jpeg`, or omitted. `jpg` is an alias for `jpeg`. When omitted,
  the codec is auto-picked from the image content. See [Format selection](#format-selection).
- **`quality`** — JPEG quality, `1`–`100`, default `90`. Ignored for PNG.
- **`region`** — optional `[x, y, width, height]` crop, in capture pixels. See
  [Region capture](#region-capture).
- **`max_width`** / **`scale`** — optional downscale knobs accepted by every capture
  tool. See [Downscaling](#downscaling).
- Capture tools return two pieces of content: a one-line text summary
  (`Captured <label> — <W>x<H>, <bytes> bytes (<mime>)`, with `(from <W>x<H> capture)`
  appended when a region crop or downscale changed the dimensions) followed by the image.
- Discovery tools (`list_chrome_tabs`, `find_tray_apps`, `find_hidden_windows`,
  `enumerate_process_windows`) return pretty-printed JSON as text.
- Capture defaults allow minimized windows; hidden/cloaked windows are reached via
  DWM-thumbnail and `PrintWindow` fallbacks.
- A missing required argument, a window/tab that cannot be found, or a capture failure
  is returned as an MCP tool error (not a thrown exception).

---

## Capture tools

### `capture_window_by_title`

Capture a top-level window located by its title.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `title` | string | yes | — | Title to match. By default a case-insensitive substring ("lazy") match. |
| `exact` | boolean | no | `false` | Require the title to equal `title` in full instead of merely containing it. |
| `case_sensitive` | boolean | no | `false` | Make the comparison respect letter case. |
| `format` | string | no | auto | `png`, `jpeg`, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video). See [Format selection](#format-selection). |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `include_cursor` | boolean | no | `false` | Include the mouse cursor in the capture. |
| `gpu` | boolean | no | `false` | Use the GPU-accelerated Windows.Graphics.Capture path instead of `PrintWindow`/`BitBlt`. See [GPU-accelerated capture](#gpu-accelerated-capture). |
| `max_width` | number | no | — | Downscale to at most this width in pixels, preserving aspect ratio. See [Downscaling](#downscaling). |
| `scale` | number | no | — | Downscale factor between `0` and `1` (e.g. `0.5`). See [Downscaling](#downscaling). |
| `region` | array | no | — | Sub-rectangle `[x, y, width, height]` in capture pixels to keep. Cropped before any downscale. See [Region capture](#region-capture). |

`exact` and `case_sensitive` are independent and combine freely — e.g. `exact=true`
with `case_sensitive=false` is a full-title match that ignores case.

When several windows match, the best target is captured (visible and non-minimized
first, then an exact title match, then the shortest/closest title). Any other matches
are listed in the summary text with their HWNDs so you can re-target precisely.

```json
{ "name": "capture_window_by_title", "arguments": { "title": "Notepad" } }
{ "name": "capture_window_by_title",
  "arguments": { "title": "Command Prompt", "exact": true, "format": "jpeg", "quality": 80 } }
```

### `capture_window_by_pid`

Capture the main window of a process.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `pid` | number | yes | — | Process ID. |
| `format` | string | no | auto | `png`, `jpeg`, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video). See [Format selection](#format-selection). |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `hidden` | boolean | no | `false` | Use the hidden-window capture path (DWM thumbnail / `PrintWindow`) for invisible windows. |
| `max_width` | number | no | — | Downscale to at most this width in pixels, preserving aspect ratio. See [Downscaling](#downscaling). |
| `scale` | number | no | — | Downscale factor between `0` and `1` (e.g. `0.5`). See [Downscaling](#downscaling). |
| `region` | array | no | — | Sub-rectangle `[x, y, width, height]` in capture pixels to keep. Cropped before any downscale. See [Region capture](#region-capture). |

```json
{ "name": "capture_window_by_pid", "arguments": { "pid": 12345, "hidden": true } }
```

### `capture_window_by_handle`

Capture a window by its `HWND`.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `handle` | number | yes | — | Window handle (HWND) as an integer. |
| `format` | string | no | auto | `png`, `jpeg`, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video). See [Format selection](#format-selection). |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `gpu` | boolean | no | `false` | Use the GPU-accelerated Windows.Graphics.Capture path instead of `PrintWindow`/`BitBlt`. See [GPU-accelerated capture](#gpu-accelerated-capture). |
| `max_width` | number | no | — | Downscale to at most this width in pixels, preserving aspect ratio. See [Downscaling](#downscaling). |
| `scale` | number | no | — | Downscale factor between `0` and `1` (e.g. `0.5`). See [Downscaling](#downscaling). |
| `region` | array | no | — | Sub-rectangle `[x, y, width, height]` in capture pixels to keep. Cropped before any downscale. See [Region capture](#region-capture). |

HWNDs are returned by the discovery tools below. They are valid only for the lifetime
of the window — re-enumerate rather than caching them.

### `capture_window_by_class`

Capture a window by its window-class name.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `class_name` | string | yes | — | Window class name (e.g. `Notepad`, `Chrome_WidgetWin_1`). |
| `format` | string | no | auto | `png`, `jpeg`, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video). See [Format selection](#format-selection). |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `max_width` | number | no | — | Downscale to at most this width in pixels, preserving aspect ratio. See [Downscaling](#downscaling). |
| `scale` | number | no | — | Downscale factor between `0` and `1` (e.g. `0.5`). See [Downscaling](#downscaling). |
| `region` | array | no | — | Sub-rectangle `[x, y, width, height]` in capture pixels to keep. Cropped before any downscale. See [Region capture](#region-capture). |

### `capture_full_screen`

Capture one monitor in full.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `monitor` | number | no | `0` | Zero-based monitor index. `0` = primary; `1+` = additional displays ordered left-to-right then top-to-bottom. |
| `format` | string | no | auto | `png`, `jpeg`, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video). See [Format selection](#format-selection). |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `max_width` | number | no | — | Downscale to at most this width in pixels, preserving aspect ratio. See [Downscaling](#downscaling). |
| `scale` | number | no | — | Downscale factor between `0` and `1` (e.g. `0.5`). See [Downscaling](#downscaling). |
| `region` | array | no | — | Sub-rectangle `[x, y, width, height]` in capture pixels to keep. Cropped before any downscale. See [Region capture](#region-capture). |

An out-of-range `monitor` index returns an error stating how many monitors are available.

### `capture_burst`

Capture a rapid series of screenshots of **one** window over time, returning every
frame as a separate image. Useful for watching a window change — animations, progress
bars, flicker, or intermittent rendering.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `title` | string | one of `title`/`handle` | — | Title to match (case-insensitive substring, same as `capture_window_by_title`). |
| `handle` | number | one of `title`/`handle` | — | Window handle (HWND) as an integer. |
| `count` | number | no | `5` | Number of frames to capture. |
| `interval_ms` | number | no | `500` | Milliseconds between frames. **Minimum 100ms is enforced** as a throttle. |
| `format` | string | no | `jpeg` | `png` or `jpeg`. Burst defaults to `jpeg` (not `png`) to keep the multi-image payload small. |
| `quality` | number | no | `80` | JPEG quality 1–100. |
| `include_cursor` | boolean | no | `false` | Include the mouse cursor in each frame. |
| `gpu` | boolean | no | `false` | Use the GPU path. The burst reuses a single capture session across frames, so it stays efficient — enable it when you need DirectComposition / hardware-rendered content. |
| `max_width` | number | no | — | Downscale to at most this width in pixels, preserving aspect ratio. See [Downscaling](#downscaling). |
| `scale` | number | no | — | Downscale factor between `0` and `1` (e.g. `0.5`). See [Downscaling](#downscaling). |
| `region` | array | no | — | Sub-rectangle `[x, y, width, height]` in capture pixels to keep. Cropped before any downscale. See [Region capture](#region-capture). |

The window is resolved **once** up front, so each frame costs only a capture + encode.
Total capture time is `interval_ms × count` and is **capped at 30 seconds** — if the
request exceeds that, `count` is reduced to fit and the summary notes the adjustment.
Frames are paced against an absolute schedule so encode time does not drift the cadence,
and each frame's encode runs concurrently with the wait before the next capture. A
per-frame failure is reported in the summary rather than aborting the whole burst.

The result is a text summary followed by one image content block per captured frame, in
capture order.

```json
{ "name": "capture_burst", "arguments": { "title": "Calculator", "count": 4, "interval_ms": 250 } }
{ "name": "capture_burst",
  "arguments": { "handle": 4262890, "count": 10, "interval_ms": 200, "scale": 0.5 } }
```

> **Payload size:** many full-resolution frames in one response is heavy even as JPEG.
> The 30s cap and minimum interval bound it, but for high frame counts prefer a lower
> `quality`, fewer frames, or a `scale` / `max_width` downscale.

### GPU-accelerated capture

`capture_window_by_title` and `capture_window_by_handle` accept a `gpu` flag. When set,
the window is captured through the **Windows.Graphics.Capture** API instead of the
default `PrintWindow`/`BitBlt` path.

WGC captures the window as composited by the Desktop Window Manager on the GPU, so it
faithfully reproduces DirectComposition and hardware-rendered content (Chromium and
Electron apps, modern WinUI apps, hardware-accelerated video) that the GDI paths can
render incorrectly or as black. The summary text of a GPU capture is tagged `[GPU]`.

Notes and limitations:

- Requires **Windows 10 1803 or newer**.
- The target must be a normal top-level window. Special windows such as the desktop
  shell (`Program Manager`) are rejected by the OS and return a capture error.
- The cursor is excluded unless `include_cursor` is set (where the OS build supports
  the toggle).

### Format selection

When `format` is omitted, the server samples the captured image's colour diversity and
picks the codec automatically:

- **Flat / UI content** (large areas of repeated colour — editors, file managers,
  settings windows) → **PNG**, which is lossless and keeps text crisp.
- **Photographic / video content** (mostly unique colours — video players, photo
  galleries, 3D apps) → **JPEG**, which is dramatically smaller and faster to encode
  for that kind of image.

Pass `format` explicitly (`png` or `jpeg`) to override the choice. The decision is made
on the final image, so it accounts for any `region` crop — cropping a photographic
window down to a flat panel can flip the pick to PNG, and vice versa. `capture_burst`
does not auto-pick; it defaults to JPEG to keep its multi-image payload small.

### Region capture

Every capture tool accepts an optional `region`: an array `[x, y, width, height]` in
pixels relative to the top-left of the capture. Only that sub-rectangle is returned.

```json
{ "name": "capture_window_by_title", "arguments": { "title": "Photoshop", "region": [700, 350, 500, 360] } }
```

The crop is applied **before** any `scale` / `max_width` downscale, so the two compose:
`region` selects the area of interest and the downscale shrinks it further. The region
is clamped to the image bounds; a region entirely outside the capture is an error. This
is the way to send an agent just the panel of a window it needs instead of the whole
frame — strictly less payload than capturing and downscaling the entire window.

### Downscaling

Every capture tool (including `capture_burst`) accepts two optional downscale arguments:

- **`max_width`** — downscale so the image is at most this many pixels wide, preserving
  aspect ratio (e.g. `1280`). `0` or omitted means full resolution.
- **`scale`** — a downscale factor between `0` and `1` (e.g. `0.5` = half width and
  height).

If both are given, the smaller result wins, and the image is **never upscaled**. When a
downscale is applied, the summary text appends `(downscaled from <W>x<H>)`.

Downscaling cuts both the encode time and the payload size roughly with the square of
the scale factor, making it the **highest-leverage option for agents** that only need
to *see* a window rather than read fine text at full resolution — as a rough guide, a
half-scale JPEG is on the order of ~20× smaller than a full-resolution PNG of the same
window. Linear (bilinear) resampling is used, which keeps the resize cost small relative
to the encode it shrinks.

```json
{ "name": "capture_full_screen",       "arguments": { "monitor": 0, "max_width": 1280 } }
{ "name": "capture_window_by_title",   "arguments": { "title": "Notepad", "format": "jpeg", "scale": 0.5 } }
```

---

## Chrome tools

### `list_chrome_tabs`

List open tabs across every detected Chrome instance. Takes no arguments.

Returns JSON text:

```json
{
  "count": 2,
  "tabs": [
    { "id": "A1B2C3", "title": "Example", "url": "https://example.com", "type": "page", "active": true }
  ]
}
```

Use a tab `id` with `capture_chrome_tab`. Chrome must be discoverable via the
DevTools protocol (a normal running Chrome is detected automatically).

### `capture_chrome_tab`

Capture a specific Chrome tab.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `tab_id` | string | yes | — | Tab ID from `list_chrome_tabs`. |
| `format` | string | no | auto | `png`, `jpeg`, or omit to auto-pick (PNG for flat UI, JPEG for photographic/video). See [Format selection](#format-selection). |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `max_width` | number | no | — | Downscale to at most this width in pixels, preserving aspect ratio. See [Downscaling](#downscaling). |
| `scale` | number | no | — | Downscale factor between `0` and `1` (e.g. `0.5`). See [Downscaling](#downscaling). |
| `region` | array | no | — | Sub-rectangle `[x, y, width, height]` in capture pixels to keep. Cropped before any downscale. See [Region capture](#region-capture). |

This captures the rendered tab content via Chrome DevTools, so it works even for
background (non-foreground) tabs.

---

## Window discovery tools

These return a JSON array of `WindowInfo` objects:

| Field | Description |
| --- | --- |
| `handle` | Window handle (HWND). |
| `title` | Window title. |
| `class_name` | Window class name. |
| `process_id` / `thread_id` | Owning process / thread. |
| `rect` / `client_rect` | Window and client-area rectangles (`x`, `y`, `width`, `height`). |
| `state` | `visible`, `minimized`, `maximized`, or `hidden`. |
| `z_order` | Z-order position. |
| `is_visible` / `is_topmost` | Visibility and always-on-top flags. |
| `monitor` | Monitor index the window is on. |

### `find_tray_apps`

Enumerate system-tray applications (visible and overflow). Takes no arguments.

### `find_hidden_windows`

Enumerate hidden top-level windows. Takes no arguments.

### `enumerate_process_windows`

List every window owned by a process, including hidden and cloaked ones.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `pid` | number | yes | — | Process ID. |

---

## Typical workflows

**Capture a known app:** call `capture_window_by_title` with a partial title.

**Capture a backgrounded / tray / minimized app:**

1. `find_tray_apps` or `find_hidden_windows` (or `enumerate_process_windows` with a PID).
2. `capture_window_by_handle` with the `handle` from the result — or
   `capture_window_by_pid` with `hidden: true`.

**Capture a browser tab:** `list_chrome_tabs` → `capture_chrome_tab` with the tab `id`.

See [../examples/COMPREHENSIVE_EXAMPLES.md](../examples/COMPREHENSIVE_EXAMPLES.md) for
end-to-end examples including the REST server.

---

## How hidden-window capture works

Standard screenshot code uses `BitBlt` (or `GetDIBits`), which copies pixels straight
from the screen. That only works for a window that is currently visible and not
minimized — it cannot reach a minimized, hidden, or off-screen window. This server
instead picks from several capture methods and falls back between them.

| Method | How it works | Reaches |
| --- | --- | --- |
| DWM Thumbnail | Registers a Desktop Window Manager thumbnail of the target and renders it to an off-screen bitmap. | Any window state, including minimized and cloaked. |
| `PrintWindow` | Asks the window to paint itself into a device context. | Visible and most minimized windows. |
| `WM_PRINT` | Sends the window a paint message to force it to render. | Most classic Win32 apps. |
| Stealth restore | Briefly restores a minimized window without activating it, captures, then re-minimizes. | Minimized windows that refuse the methods above. |
| `BitBlt` | Direct screen copy — the fast path. | Visible windows only. |

**Fallback ladder.** The engine inspects the window's state and tries methods in the
order most likely to succeed, retrying on failure:

- Visible → `BitBlt` → `PrintWindow` → DWM Thumbnail
- Minimized → DWM Thumbnail → `PrintWindow` → stealth restore
- Hidden or cloaked → DWM Thumbnail → `WM_PRINT` → `PrintWindow`

**Why DWM thumbnails are the workhorse.** The Desktop Window Manager already keeps a
live thumbnail of every window for Alt-Tab, taskbar previews, and compositing effects.
By registering a thumbnail and rendering it off-screen, the engine reads those pixels
directly — so capture succeeds regardless of whether the window is on screen.

The `hidden: true` flag on `capture_window_by_pid` opts straight into this path; the
other capture tools use the fallback ladder automatically. Some windows still cannot
be captured — system/protected processes may require elevation, and a destroyed window
returns an error.
