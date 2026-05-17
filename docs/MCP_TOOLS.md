# MCP Tools Reference

The `windows-screenshot` MCP server (`cmd/mcp`, built as `screenshot-mcp.exe`) speaks the
Model Context Protocol over stdio and exposes the ten tools below. It is the complete,
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

- **`format`** — `png` (default) or `jpeg`. `jpg` is accepted as an alias for `jpeg`;
  any other value falls back to `png`.
- **`quality`** — JPEG quality, `1`–`100`, default `90`. Ignored for PNG.
- Capture tools return two pieces of content: a one-line text summary
  (`Captured <label> — <W>x<H>, <bytes> bytes (<mime>)`) followed by the image itself.
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
| `format` | string | no | `png` | `png` or `jpeg`. |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `include_cursor` | boolean | no | `false` | Include the mouse cursor in the capture. |

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
| `format` | string | no | `png` | `png` or `jpeg`. |
| `quality` | number | no | `90` | JPEG quality 1–100. |
| `hidden` | boolean | no | `false` | Use the hidden-window capture path (DWM thumbnail / `PrintWindow`) for invisible windows. |

```json
{ "name": "capture_window_by_pid", "arguments": { "pid": 12345, "hidden": true } }
```

### `capture_window_by_handle`

Capture a window by its `HWND`.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `handle` | number | yes | — | Window handle (HWND) as an integer. |
| `format` | string | no | `png` | `png` or `jpeg`. |
| `quality` | number | no | `90` | JPEG quality 1–100. |

HWNDs are returned by the discovery tools below. They are valid only for the lifetime
of the window — re-enumerate rather than caching them.

### `capture_window_by_class`

Capture a window by its window-class name.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `class_name` | string | yes | — | Window class name (e.g. `Notepad`, `Chrome_WidgetWin_1`). |
| `format` | string | no | `png` | `png` or `jpeg`. |
| `quality` | number | no | `90` | JPEG quality 1–100. |

### `capture_full_screen`

Capture one monitor in full.

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `monitor` | number | no | `0` | Zero-based monitor index. `0` = primary; `1+` = additional displays ordered left-to-right then top-to-bottom. |
| `format` | string | no | `png` | `png` or `jpeg`. |
| `quality` | number | no | `90` | JPEG quality 1–100. |

An out-of-range `monitor` index returns an error stating how many monitors are available.

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
| `format` | string | no | `png` | `png` or `jpeg`. |
| `quality` | number | no | `90` | JPEG quality 1–100. |

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
