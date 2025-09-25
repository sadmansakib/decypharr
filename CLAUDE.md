# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

### Building and Running
- **Development with hot reload**: `npm run dev` - Builds assets and starts air for live reloading
- **Build production binary**: `go build -ldflags "-X github.com/sirrobot01/decypharr/pkg/version.Version=0.0.0 -X github.com/sirrobot01/decypharr/pkg/version.Channel=dev" -o ./tmp/main .`
- **Build assets only**: `npm run build` - Compiles CSS with Tailwind and minifies JS
- **Build everything**: `npm run build-all` - Downloads assets and builds everything

### Docker
- **Build image**: `docker build -t decypharr .`
- **Run container**: Use the docker-compose configuration from README.md

### Asset Management
- **Build CSS**: `npm run build-css` - Compiles Tailwind CSS
- **Minify JS**: `npm run minify-js` - Minifies JavaScript assets
- **Download assets**: `npm run download-assets` - Downloads external dependencies

### Release
- **Create release**: Uses GoReleaser (`.goreleaser.yaml`) for multi-platform builds
- **Supported platforms**: Linux, Windows, Darwin (amd64, arm, arm64)

## Architecture Overview

Decypharr is a Go-based media management tool that provides a QBittorrent-compatible API for *Arr applications while integrating with multiple Debrid services.

### Core Components

**Entry Point**: `main.go` → `cmd/decypharr/main.go` - Sets up signal handling and starts the application

**Application Core**: `cmd/decypharr/main.go` (Start function) orchestrates:
- **Web UI** (`pkg/web`) - Full-featured frontend for torrent management
- **QBittorrent API** (`pkg/qbit`) - Mock QBittorrent API for *Arr integration
- **WebDAV Server** (`pkg/webdav`) - Provides WebDAV access to debrid files
- **Wire System** (`pkg/wire`) - Core business logic and service coordination

### Key Packages

#### Debrid Integration (`pkg/debrid/`)
- **Providers**: Real-Debrid, Torbox, Debrid-Link, AllDebrid
- **Rate Limiting**: Sophisticated rate limiting with circuit breakers (especially for Torbox)
- **Account Management**: Multi-account support with rotation
- **Types**: Common interface for all debrid services

#### Core Services (`pkg/wire/`)
- **Request Processing**: Manages torrent import requests from *Arr apps
- **Queue Management**: Handles torrent download queues
- **Workers**: Background processing for torrents, repairs, downloads
- **Storage**: Torrent state and file management

#### Infrastructure (`internal/`)
- **Config**: JSON-based configuration with hot reloading
- **Request**: HTTP client with rate limiting, circuit breakers, and retry logic
- **Logger**: Structured logging with zerolog
- **Utils**: File handling, magnet parsing, debouncing

### Service Architecture

The application runs multiple concurrent services:
1. **HTTP Server** - Serves web UI, QBittorrent API, and WebDAV
2. **Rclone Manager** - Manages filesystem mounts for debrid services
3. **Repair Worker** - Monitors and repairs missing files
4. **Background Workers** - Process torrents, downloads, and maintenance

### Configuration

- **Config Path**: Configurable via `--config` flag (default: `/data`)
- **Hot Reloading**: Configuration changes trigger service restarts
- **Format**: JSON with comprehensive debrid provider settings
- **Rate Limits**: Per-provider rate limiting with different limits for API calls vs downloads

### Rate Limiting Strategy

Decypharr implements sophisticated rate limiting:
- **General API calls**: Provider-specific limits (e.g., 4/sec for Torbox)
- **Endpoint-specific limits**: Special handling for critical endpoints (e.g., createtorrent)
- **Circuit breakers**: Protection against 429 responses
- **Concurrency control**: Semaphores for download operations

### Development Notes

- **Live Reload**: Uses Air (`.air.toml`) for hot reloading during development
- **Asset Pipeline**: Tailwind CSS + DaisyUI for styling, Terser for JS minification
- **No Tests**: This codebase currently has no test files
- **Version Injection**: Uses ldflags to inject version info at build time

### Integration Points

- **QBittorrent Compatibility**: Provides compatible API endpoints for Sonarr/Radarr/Lidarr
- **WebDAV Access**: Exposes debrid files via WebDAV for direct access
- **Rclone Integration**: Optional filesystem mounting using rclone
- **Multi-Provider**: Supports multiple debrid services simultaneously with load balancing

## Branch Information

- **Main Branch**: `main` - Production-ready code with enhanced rate limiting
- **Beta Branch**: `beta` - Development branch with simplified rate limiting
- **Current**: You are on the `beta` branch