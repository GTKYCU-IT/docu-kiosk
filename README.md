# docu-kiosk

[![CI](https://github.com/GTKYCU-IT/docu-kiosk/actions/workflows/ci.yml/badge.svg)](https://github.com/GTKYCU-IT/docu-kiosk/actions/workflows/ci.yml)
[![Release](https://github.com/GTKYCU-IT/docu-kiosk/actions/workflows/release.yml/badge.svg)](https://github.com/GTKYCU-IT/docu-kiosk/actions/workflows/release.yml)
[![GitHub release](https://img.shields.io/github/v/release/GTKYCU-IT/docu-kiosk)](https://github.com/GTKYCU-IT/docu-kiosk/releases/latest)

A system for routing DocuSign signing sessions from an MSR's workstation to a member-facing kiosk at a credit union or bank branch.

When a core banking application opens a DocuSign signing URL, the MSR's browser would normally display it — but the member can't see the MSR's screen. This system intercepts that URL and pushes it to a kiosk the member can see and interact with directly.

See the [wiki](../../wiki) for full architecture and operational documentation.

## Components

- **Broker** (`cmd/server/`) — Go HTTP server that manages kiosk WebSocket connections and relays signing URLs from the extension
- **Kiosk frontend** (`web/`) — Svelte SPA served by the broker; kiosks register, connect via WebSocket, and display the DocuSign iframe when a URL is pushed
- **Extension** (`extension/`) — Manifest V3 Chrome/Edge extension that intercepts DocuSign requests and lets the MSR choose which kiosk to send the document to

## Development

**Prerequisites:** Go 1.25+, Node 22+, [air](https://github.com/air-verse/air) (`go install github.com/air-verse/air@latest`)

```bash
# Start broker (hot-reload) + frontend (hot-reload) together
make dev

# Or run each separately
make dev-broker   # broker with air
make dev-web      # Vite dev server
```

The Vite dev server proxies `/api` and `/ws` to the broker at `localhost:8080`, so both can be developed together without a production build.

```bash
make build      # build everything
make web        # Vite production build → web/dist/
make server     # Go binary → ./server
make extension  # Extension → extension/dist/
make clean      # remove build artifacts
```

Load `extension/dist/` as an unpacked extension at `edge://extensions` → "Load unpacked" to test the extension locally. The extension uses `declarativeNetRequest` (no enterprise policy required) so load-unpacked works for all core functionality.

## Configuration

### Broker

The broker requires no environment variables. Kiosk registrations are persisted in `kiosks.db` (SQLite). Authentication is IP-based — no secrets or registration keys needed.

Cross-origin requests fail closed. Same-origin requests (the kiosk SPA served by the broker) and `chrome-extension://` origins are always allowed; set `CORS_ORIGINS` (comma-separated, see `.env.example`) to allow additional cross-origin callers such as an admin UI on another host.

Caddy runs independently on the server as a reverse proxy in front of the broker. See the [DevOps wiki](../../wiki/DevOps) for server configuration.

### Extension

Configure via the intercepted page's options view, or push via MDM/GPO using `chrome.storage.managed`:

| Option | Description |
|---|---|
| `brokerUrl` | Base URL of the broker, e.g. `https://broker.yourdomain.local` |
