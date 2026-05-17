# Examples

End-to-end examples for the Windows Screenshot MCP Server. The project ships two
interfaces:

- **MCP server** (`cmd/mcp` → `screenshot-mcp.exe`) — stdio MCP, the complete and
  recommended interface. Full tool reference: [../docs/MCP_TOOLS.md](../docs/MCP_TOOLS.md).
- **REST + WebSocket server** (`cmd/server` → `screenshot-server.exe`) — HTTP API and
  live streaming, useful for browser/dashboard integrations.

Focused walkthroughs live alongside this file:

| Topic | Doc |
| --- | --- |
| Single window capture | [basics/single-window.md](basics/single-window.md) |
| Chrome tab capture | [chrome/chrome-tabs.md](chrome/chrome-tabs.md) |
| Hidden / tray windows | [hidden-and-tray/hidden-window.md](hidden-and-tray/hidden-window.md) |
| WebSocket streaming | [streaming/websocket-streaming.md](streaming/websocket-streaming.md) |
| Visual regression testing | [testing/visual-regression.md](testing/visual-regression.md) |

---

## Part 1 — MCP server

### Setup

```bash
go build -o screenshot-mcp.exe ./cmd/mcp
```

Register it with your MCP client (see `.mcp.json` in the repo root). The client
launches the binary; all interaction is JSON-RPC over stdio.

Tool calls below are shown as the `arguments` object an MCP client sends. Capture
tools return a one-line text summary plus the image; discovery tools return JSON text.

### 1. Capture a window by title

```json
{ "name": "capture_window_by_title", "arguments": { "title": "Notepad" } }
```

`title` is a case-insensitive substring by default — `"note"` matches `"Untitled - Notepad"`.
Tighten it when needed:

```json
{ "name": "capture_window_by_title",
  "arguments": { "title": "Command Prompt", "exact": true, "case_sensitive": true } }
```

If more than one window matches, the best candidate is captured and the rest are
listed (with HWNDs) in the summary text so you can re-target with `capture_window_by_handle`.

### 2. Capture by PID, handle, or class

```json
{ "name": "capture_window_by_pid",    "arguments": { "pid": 12345 } }
{ "name": "capture_window_by_handle", "arguments": { "handle": 4262890 } }
{ "name": "capture_window_by_class",  "arguments": { "class_name": "Chrome_WidgetWin_1" } }
```

### 3. Capture a full monitor

```json
{ "name": "capture_full_screen", "arguments": { "monitor": 0 } }
```

`monitor` is zero-based: `0` is the primary display, `1+` the others ordered
left-to-right then top-to-bottom. An out-of-range index reports how many exist.

### 4. Capture a hidden, minimized, or tray application

Discover the target, then capture it by handle:

```json
{ "name": "find_tray_apps",     "arguments": {} }
{ "name": "find_hidden_windows", "arguments": {} }
```

Each returns a JSON array of `WindowInfo`. Pick a `handle` and capture it:

```json
{ "name": "capture_window_by_handle", "arguments": { "handle": 1180498 } }
```

Or go straight through the process, using the hidden-capture path:

```json
{ "name": "enumerate_process_windows", "arguments": { "pid": 8820 } }
{ "name": "capture_window_by_pid",     "arguments": { "pid": 8820, "hidden": true } }
```

See [hidden-and-tray/hidden-window.md](hidden-and-tray/hidden-window.md) for a focused
walkthrough, and the "How hidden-window capture works" section of
[../docs/MCP_TOOLS.md](../docs/MCP_TOOLS.md) for the underlying mechanism.

### 5. Capture a Chrome tab

```json
{ "name": "list_chrome_tabs", "arguments": {} }
```

```json
{
  "count": 2,
  "tabs": [
    { "id": "8A1F...", "title": "Example Domain", "url": "https://example.com", "active": true },
    { "id": "B7C2...", "title": "GitHub",         "url": "https://github.com",  "active": false }
  ]
}
```

```json
{ "name": "capture_chrome_tab", "arguments": { "tab_id": "B7C2...", "format": "jpeg", "quality": 85 } }
```

Tab capture works for background tabs too, since it renders via Chrome DevTools.
More detail: [chrome/chrome-tabs.md](chrome/chrome-tabs.md).

---

## Part 2 — REST + WebSocket server

```bash
go build -o screenshot-server.exe ./cmd/server
screenshot-server.exe            # listens on :8080 (see config.yaml)
```

### Health check

```bash
curl http://localhost:8080/api/health
# {"status":"healthy","timestamp":"...","version":"1.0.0"}
```

### Take a screenshot

`GET /api/screenshot` (also `/v1/screenshot`). Query parameters:

| Param | Values | Notes |
| --- | --- | --- |
| `method` | `title`, `pid`, `handle`, `class` | Defaults to `title`. |
| `target` | string | Required — the title / PID / HWND / class name. |
| `format` | `png`, `jpeg` | Defaults to the server config. |
| `quality` | `1`–`100` | JPEG quality. |
| `cursor` | `true` | Include the mouse cursor. |

```bash
curl "http://localhost:8080/api/screenshot?method=title&target=Notepad"
curl "http://localhost:8080/api/screenshot?method=pid&target=12345&format=jpeg&quality=85"
```

The response is a JSON `ScreenshotResponse`:

```json
{
  "success": true,
  "data": "<base64>",
  "format": "BGRA32",
  "width": 1280,
  "height": 720,
  "size": 3686400,
  "timestamp": "...",
  "metadata": { "capture_method": "title", "processing_time": 0, "...": "..." }
}
```

> **Note:** `data` is base64-encoded **raw BGRA pixels** (`width × height × 4` bytes),
> not an encoded PNG/JPEG file — so `curl ... -o shot.png` does **not** produce a valid
> image. Decode the pixels yourself, or use the MCP server, which returns ready-to-use
> encoded images.

A `POST /v1/screenshot` variant accepts the same fields as a JSON body and additionally
supports a `region` object (`{x,y,width,height}`) for sub-rectangle capture.

### Chrome over REST

```bash
curl http://localhost:8080/v1/chrome/instances      # discovered Chrome processes
curl http://localhost:8080/v1/chrome/tabs           # all tabs across instances
curl -X POST http://localhost:8080/v1/chrome/tabs/<TAB_ID>/screenshot
```

### WebSocket streaming

Connect to `ws://localhost:8080/stream/{windowId}` for a live feed. Tune it with
query parameters:

```text
ws://localhost:8080/stream/123456?fps=15&quality=80&format=jpeg
```

`GET /v1/stream/status` reports active sessions and throughput. A ready-made browser
client (`websocket-viewer.html`) and a Go client (`streaming-client/`) are in this
directory — see [examples/README.md](README.md) and
[streaming/websocket-streaming.md](streaming/websocket-streaming.md).

### Known limitations

- `GET /v1/windows`, `GET /api/windows`, and `GET /v1/windows/:handle` are
  placeholders — window enumeration over REST is not yet implemented. Use the MCP
  server's `find_tray_apps` / `find_hidden_windows` / `enumerate_process_windows`
  tools instead.
- The REST screenshot endpoint returns raw BGRA pixels (see the note above).

For anything beyond live streaming or browser integration, prefer the MCP server.
