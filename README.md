# docu-kiosk

A system for routing DocuSign signing sessions from an MSR's workstation to a member-facing kiosk at a credit union or bank branch.

## How it works

When a core banking application opens a DocuSign signing URL, the MSR's browser would normally display it — but the member can't see the MSR's screen. This system intercepts that URL and pushes it to a kiosk the member can see and interact with directly.

**Flow:**
1. Core banking app opens a DocuSign URL in the browser
2. A Chrome/Edge extension intercepts the request and cancels it (so it doesn't open on the MSR's screen)
3. The extension POSTs the signing URL to the broker, along with the kiosk ID
4. The broker pushes the URL to the paired kiosk over a WebSocket
5. The member sees and signs the document on their own screen

## Components

### Broker (`cmd/server/`)
A Go HTTP server that manages kiosk WebSocket connections and receives push requests from the extension. Serves the kiosk web frontend as static files.

### Kiosk frontend (`web/`)
A Svelte SPA served by the broker. Kiosks register with a name and a secret key, receive a token, then save the page as a home screen shortcut. On each load the token is used to authenticate and establish a WebSocket connection. When the broker pushes a signing URL, the kiosk displays the DocuSign iframe.

### Extension (`extension/`)
A Manifest V3 Chrome/Edge extension installed on MSR workstations. Intercepts requests to `*.docusign.net` and `*.docusign.com`, forwards the signing URL to the broker, and cancels the original request. Configured via an options page (broker URL and kiosk ID).

## Development

**Prerequisites:** Go 1.25+, Node 22+

```bash
# Terminal 1 — broker
DOCU_KIOSK_TOKEN_SECRET=dev DOCU_KIOSK_REGISTRATION_KEY=dev go run ./cmd/server

# Terminal 2 — frontend (hot reload via Vite proxy → broker)
cd web && npm install && npm run dev

# Extension
cd extension && npm install && npm run build
# Load extension/dist/ as an unpacked extension at edge://extensions
```

The Vite dev server proxies `/api` and `/ws` to the broker at `localhost:8080`, so the frontend and broker can be developed together without a production build.

## Deployment

The broker and frontend are deployed together as a Docker container behind Caddy, which handles TLS automatically using its built-in certificate authority.

### First-time setup

**1. Clone the repo onto the server**
```bash
git clone <repo-url> docu-kiosk && cd docu-kiosk
```

**2. Create your `.env` file**
```bash
cp .env.example .env
```

Edit `.env` — generate strong secrets with `openssl rand -base64 32`:
```
DOCU_KIOSK_TOKEN_SECRET=<long random string>
DOCU_KIOSK_REGISTRATION_KEY=<long random string>
BROKER_HOST=broker.yourdomain.local
```

**3. Create an internal DNS A record** pointing `broker.yourdomain.local` to the server's IP.

**4. Start the stack**
```bash
docker compose up -d
```

**5. Export the root certificate** for distribution to client devices:
```bash
docker compose exec caddy cat /data/pki/authorities/local/root.crt > docu-kiosk-ca.crt
```

**6. Distribute the root certificate**
- **MSR workstations (domain-joined):** deploy via GPO → `Computer Configuration → Policies → Windows Settings → Security Settings → Public Key Policies → Trusted Root Certification Authorities`
- **Kiosk tablets (iPad):** Settings → install profile → General → Certificate Trust Settings → enable full trust. Or deploy via MDM.

### Updating

```bash
git pull
docker compose build
docker compose up -d
```

## Configuration

### Broker environment variables

| Variable | Required | Description |
|---|---|---|
| `DOCU_KIOSK_TOKEN_SECRET` | Yes | Secret used to sign kiosk authentication tokens |
| `DOCU_KIOSK_REGISTRATION_KEY` | Yes | Key required to register a new kiosk |
| `BROKER_HOST` | Yes | Hostname Caddy will serve and issue a TLS certificate for |

### Extension options

Configure via the extension's options page (`edge://extensions` → DocuSign Kiosk → Details → Extension options):

| Option | Description |
|---|---|
| Broker URL | Full URL of the push endpoint, e.g. `https://broker.yourdomain.local/api/push` |
| Kiosk ID | ID of the kiosk to push signing URLs to (from `GET /api/kiosks`) |

The extension also supports enterprise MDM/GPO deployment via managed storage (`chrome.storage.managed`), which pre-configures options across all workstations without manual setup.

## Kiosk registration

1. Open `https://broker.yourdomain.local` on the kiosk device
2. Enter a name (e.g. `Lobby Kiosk 1`) and the registration key
3. When prompted, save the page as a home screen shortcut
4. The shortcut URL contains the kiosk's authentication token — opening it automatically connects the kiosk to the broker

## API

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/kiosks` | Register a kiosk — returns an auth token |
| `GET` | `/api/kiosks` | List currently connected kiosks |
| `GET` | `/ws?token=<token>` | Establish a kiosk WebSocket connection |
