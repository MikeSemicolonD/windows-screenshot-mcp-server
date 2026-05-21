# Windows Screenshot MCP Server

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![MCP Compatible](https://img.shields.io/badge/MCP-Compatible-green.svg)](https://github.com/modelcontextprotocol/specification)

**Professional Windows screenshot capture server with Model Context Protocol (MCP) integration, real-time WebSocket streaming, Chrome tab capture, and advanced window targeting.**

## Overview

A production-ready Go-based screenshot server that provides both REST API and MCP protocol support for capturing Windows application screenshots. Designed for automation, testing, and AI agent integration with advanced features like real-time streaming and hidden window capture.

## Features

### Core Screenshot Capabilities
- **Window targeting**: Capture by title, class name, process ID, or window handle
- **Multiple image formats**: PNG, JPEG, BMP, WebP with configurable quality
- **Region capture**: Specify rectangular areas for precise screenshots
- **Advanced window handling**: Support for hidden, minimized, and system tray applications

### Chrome Browser Integration
- **Tab discovery**: Automatically find Chrome instances and enumerate tabs
- **Direct tab capture**: Screenshot specific browser tabs via Chrome DevTools
- **Multiple Chrome support**: Handle multiple Chrome processes simultaneously

### Real-Time WebSocket Streaming
- **Live streaming**: Real-time window feeds via WebSocket connections
- **Configurable quality**: Adjust FPS (1-60), quality, and format dynamically
- **Multiple sessions**: Support concurrent streaming sessions
- **Session management**: Start, stop, and monitor active streaming sessions

### Dual Protocol Support
- **REST API**: Traditional HTTP endpoints for easy integration
- **Model Context Protocol (MCP)**: JSON-RPC 2.0 for AI agent integration
- **Health monitoring**: Built-in health checks and status reporting
- **CORS support**: Cross-origin requests enabled for web applications

## Quick Start

### Installation

```bash
# Download latest release
curl -L https://github.com/your-org/screenshot-mcp-server/releases/latest/download/screenshot-server.exe -o screenshot-server.exe

# Or build from source
git clone https://github.com/your-org/screenshot-mcp-server.git
cd screenshot-mcp-server
go build -o screenshot-server.exe ./cmd/server
```

### Basic Usage

```bash
# Start the server
./screenshot-server.exe --port 8080

# Health check
curl http://localhost:8080/health

# Basic window capture
curl "http://localhost:8080/api/screenshot?method=title&target=Notepad" -o notepad.png

# Full desktop capture
curl "http://localhost:8080/api/screenshot?method=desktop&monitor=0" -o desktop.png
```

## API Reference

### REST Endpoints

#### Health Check
```http
GET /health
```
Returns server status and version information.

#### Screenshot Capture
```http
GET /api/screenshot
GET /v1/screenshot
```

**Parameters:**
- `method` (required): `title`, `pid`, `handle`, `class`
- `target` (required): Window identifier (title, PID, handle, class name)
- `format`: `png`, `jpeg`, `bmp`, `webp` (default: `png`)
- `quality`: 1-100 for lossy formats (default: 95)
- `cursor`: `true`/`false` to include mouse cursor

**Examples:**
```bash
# Window by title
curl "http://localhost:8080/api/screenshot?method=title&target=Calculator" -o calc.png

# Window by PID
curl "http://localhost:8080/api/screenshot?method=pid&target=1234&format=jpeg&quality=80" -o app.jpg

# Window by class name
curl "http://localhost:8080/api/screenshot?method=class&target=Notepad&cursor=true" -o notepad.png
```

#### Chrome Integration
```http
GET /v1/chrome/instances          # List Chrome instances
GET /v1/chrome/tabs               # List all Chrome tabs
POST /v1/chrome/tabs/:id/screenshot  # Capture specific tab
```

### WebSocket Streaming

Connect to `ws://localhost:8080/stream/{windowId}` for real-time streaming.

**Query Parameters:**
- `fps`: Frames per second (1-60, default: 10)
- `quality`: Compression quality (10-100, default: 75)
- `format`: `jpeg` or `png` (default: `jpeg`)

**Client Example:**
```html
<!DOCTYPE html>
<html>
<body>
    <img id="stream" style="max-width: 100%;">
    <script>
        const ws = new WebSocket('ws://localhost:8080/stream/0?fps=15&quality=75&format=jpeg');
        ws.onmessage = function(event) {
            const data = JSON.parse(event.data);
            if (data.type === 'frame') {
                document.getElementById('stream').src = 'data:image/jpeg;base64,' + data.image;
            }
        };
    </script>
</body>
</html>
```

### Model Context Protocol (MCP)

In addition to the REST server, the project ships a dedicated **MCP stdio server**
(`cmd/mcp`, built as `screenshot-mcp.exe`) for direct integration with AI agents and
MCP clients such as Claude Code. It communicates over stdio using the MCP protocol —
no HTTP port is involved.

**Register it with an MCP client** (e.g. `.mcp.json`):

```json
{
  "mcpServers": {
    "windows-screenshot": {
      "command": "C:\\path\\to\\screenshot-mcp.exe",
      "type": "stdio"
    }
  }
}
```

> Use an absolute path for `command` so the client can launch it regardless of its
> working directory.

#### Available Tools

| Tool | Description |
| --- | --- |
| `capture_window_by_title` | Capture a top-level window found by title (see matching rules below). |
| `capture_window_by_pid` | Capture the main window of a process; `hidden=true` uses DWM/PrintWindow fallbacks. |
| `capture_window_by_handle` | Capture a window by its `HWND`. |
| `capture_window_by_class` | Capture a window by its class name. |
| `capture_burst` | Capture a throttled series of frames of one window over time (see below). |
| `capture_full_screen` | Capture a single monitor by index (`0` = primary). |
| `list_chrome_tabs` | List open tabs across all detected Chrome instances. |
| `capture_chrome_tab` | Capture a specific Chrome tab by ID. |
| `find_tray_apps` | Enumerate system tray applications. |
| `find_hidden_windows` | Enumerate hidden top-level windows. |
| `enumerate_process_windows` | List all windows owned by a process, including hidden/cloaked. |

All capture tools accept `format` (`png` default, or `jpeg`) and `quality` (1-100).

#### Title matching (`capture_window_by_title`)

By default the `title` argument is a **case-insensitive substring** ("lazy") match —
`"command"`, `"prompt"`, and `"Command Prompt"` all match a window titled
*Command Prompt*. When several windows match, the best target is chosen (visible and
non-minimized windows first, then an exact title match, then the shortest/closest
title); other matches are listed in the result text so an agent can re-target.

Two independent flags tighten the match:

- `exact` — require the title to **equal** the given string in full, instead of just
  containing it as a substring. Default `false`.
- `case_sensitive` — make the comparison respect letter case. Default `false`.

They combine freely (e.g. `exact=true, case_sensitive=false` is a full-title match
that ignores case).

#### Burst capture (`capture_burst`)

Captures a rapid series of screenshots of a single window over time and returns
**every frame as a separate image**, which is useful for watching a window change
(animations, progress bars, flicker, intermittent rendering).

Target the window with **either** `title` (case-insensitive substring, same matching
as `capture_window_by_title`) **or** `handle` (`HWND`). The window is resolved once up
front, so each frame costs only a capture + encode.

Throttle and frame count:

- `interval_ms` — spacing between frames. Default `500`; a **minimum of 100 ms is
  enforced** so the target window is not hammered.
- `count` — number of frames. Default `5`.
- Total capture time is `interval_ms × count` and is **capped at 30 seconds**. If the
  request exceeds that, `count` is reduced to fit and the result text notes the
  adjustment.

Unlike the single-frame tools, burst **defaults to JPEG at quality 80** to keep the
multi-image payload small; pass `format=png` for lossless frames. Frames are paced
against an absolute schedule so capture/encode time does not drift the cadence, and a
per-frame failure is reported in the summary rather than aborting the whole burst.
Optional `gpu=true` is supported but re-initializes per frame and is slower for bursts.

> **Payload size:** many full-resolution frames in one response is heavy even as JPEG.
> The 30 s cap and minimum interval bound it, but for high frame counts consider a
> lower `quality` or fewer frames.

#### Monitor selection (`capture_full_screen`)

`monitor` is a zero-based index: `0` is the primary display, `1+` are the remaining
monitors ordered left-to-right then top-to-bottom. An out-of-range index returns an
error stating how many monitors are available.

#### Window rendering note

Visible windows are captured via `PrintWindow` with the `PW_RENDERFULLCONTENT` flag,
which correctly captures GPU / DirectComposition-rendered surfaces such as
`conhost.exe` (Command Prompt), Chromium-based browsers, Electron apps, and modern
WinUI apps. It falls back to a `BitBlt` of the window DC for legacy GDI windows.

### Server Configuration

The server can be configured via environment variables or command-line flags:

```bash
# Start with custom port
./server.exe --port 9090

# Start with custom host
./server.exe --host 0.0.0.0 --port 8080

# Environment variables
export SCREENSHOT_PORT=8080
export SCREENSHOT_HOST=localhost
./server.exe
```

## Examples & Use Cases

### [Basic Examples](examples/basics/)
- [Single Window Capture](examples/basics/single-window.md) - Simple window screenshots with REST API, CLI, and programming examples

### [Streaming & Real-time](examples/streaming/)
- [WebSocket Live Streaming](examples/streaming/websocket-streaming.md) - Real-time window feeds with JavaScript, Python, and Node.js clients

### [Hidden & System Integration](examples/hidden-and-tray/)
- [Hidden Window Capture](examples/hidden-and-tray/hidden-window.md) - Advanced techniques for minimized and system tray applications

### [Browser Integration](examples/chrome/)
- [Chrome Tab Capture](examples/chrome/chrome-tabs.md) - Direct browser tab screenshots with Chrome DevTools integration

### [Testing & Quality Assurance](examples/testing/)
- [Visual Regression Testing](examples/testing/visual-regression.md) - Automated UI change detection with Python framework

## Advanced Configuration

### Server Configuration

The server uses a default configuration that can be customized:

```go
// Default settings
type Config struct {
    Port              int    // Default: 8080
    Host              string // Default: "localhost"
    DefaultFormat     string // Default: "png"
    Quality           int    // Default: 95
    IncludeCursor     bool   // Default: false
    LogLevel          string // Default: "info"
    ChromeTimeout     string // Default: "30s"
    StreamMaxSessions int    // Default: 10
    StreamDefaultFPS  int    // Default: 10
}
```

### Chrome DevTools Setup

For Chrome tab capture, launch Chrome with debugging enabled:

```bash
# Windows
"C:\Program Files\Google\Chrome\Application\chrome.exe" --remote-debugging-port=9222

# Launch with temporary profile
chrome.exe --remote-debugging-port=9222 --user-data-dir=temp-profile
```

## Building from Source

### Prerequisites
- Go 1.21 or later
- Windows OS (for Windows API support)
- Git

### Build Instructions

```bash
# Clone the repository
git clone https://github.com/your-org/screenshot-mcp-server.git
cd screenshot-mcp-server

# Install dependencies
go mod download

# Build the server
go build -o server.exe ./cmd/server

# Run tests
go test ./...

# Start the server
./server.exe
```

### Project Structure

```
├── cmd/
│   ├── server/          # REST / WebSocket HTTP server
│   ├── mcp/             # MCP stdio server (screenshot-mcp.exe)
│   └── mcpctl/          # MCP control utility
├── internal/
│   ├── screenshot/      # Screenshot capture engines
│   ├── chrome/          # Chrome DevTools integration
│   ├── window/          # Window management
│   └── ws/              # WebSocket streaming
├── pkg/
│   └── types/           # Shared data structures
└── examples/            # Usage examples and documentation
```

## Architecture

The server follows a modular architecture:

- **HTTP Server** (Gin framework) - REST API endpoints
- **WebSocket Manager** - Real-time streaming support
- **Screenshot Engine** - Core capture functionality with multiple methods
- **Chrome Manager** - Browser integration via DevTools protocol
- **MCP Handler** - JSON-RPC 2.0 support for AI agents

## Contributors

- **[Amaf Jarkasi](https://github.com/amafjarkasi)** — original author and maintainer of the [upstream project](https://github.com/amafjarkasi/windows-screenshot-mcp-server).
- **[Michael Frye](https://github.com/MikeSemicolonD)** — [fork](https://github.com/MikeSemicolonD/windows-screenshot-mcp-server) maintainer; MCP stdio server, lazy title matching, per-monitor capture, and `PrintWindow` rendering fixes, GPU accelerated captures.

Contributions are welcome — please open an issue or pull request.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Support

- **Issues**: [GitHub Issues](https://github.com/your-org/screenshot-mcp-server/issues)
- **Documentation**: See `/examples` directory for usage examples

---

**A powerful Windows screenshot server built for modern automation and AI integration.**
