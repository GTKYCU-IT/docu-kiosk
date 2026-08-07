# Release process: RC → staging → stable

Every change that touches server, extension, or CI behavior ships through the same
gate: **cut an RC, validate on staging, promote to stable, pin prod**. Never deploy
`:latest` directly to prod — it only moves on a stable release.

## Versioning rules

- Tags are semver: `vX.Y.Z` (stable) and `vX.Y.Z-rc.N` (candidate).
- **The extension version in `extension/package.json` never contains `-`** — Chrome
  manifest versions must be 1–4 dot-separated integers, and both `--pack-extension`
  (CRX signing) and loading the extension unpacked reject `2.2.8-rc.1`. The `rc`
  target bumps the extension to the *stable* version (`2.2.9`) while tagging the
  candidate (`v2.2.9-rc.1`).
- Stable and its last RC point at the same commit when no fixes landed in between.

## Cut an RC

From a clean `main`:

```sh
make rc VERSION=2.2.9-rc.1
```

This bumps `extension/package.json` to `2.2.9` (Chrome-safe, no-op if already
there), commits if changed, tags `v2.2.9-rc.1`, and pushes `main` + tag.

The tag triggers the Release workflow, which must pass `go test -race` and the web
build before anything is published. For an RC tag it:

- builds `ghcr.io/<repo>:2.2.9-rc.1` — **never `:latest`**
- publishes a GitHub release marked **prerelease**, without CRX assets
- skips the signed CRX pack (Chrome can't pack a prerelease version — load the
  unpacked build from `extension/dist/` for extension testing)

## Validate on staging

```sh
# compose.staging.yaml — pinned candidate image, OWN volume (never prod's dk-data)
docker compose -p docu-kiosk-staging -f compose.staging.yaml up -d
```

Checklist before promotion:

1. Fresh staging volume; register two kiosks.
2. Both kiosk screens reach **"Ready for member"** (WebSocket `connected`).
3. Open an intercepted DocuSign URL: extension lists both kiosks.
4. **Send to kiosk** lands on the signing screen; **Open in browser** bypass opens a
   tab that survives DocuSign redirects; copy-to-clipboard fallback works when the
   bypass fails.
5. Cross-origin still fails closed:
   `curl -i -H "Origin: https://evil.example.com" http://staging:8080/ws` → `403`.
6. Broker log shows `cors: rejected origin` only for origins you don't recognize.

Fixes during validation go to `main`, then **re-cut with the next RC number**:

```sh
make rc VERSION=2.2.9-rc.2
```

## Promote to stable

After staging sign-off:

```sh
make release VERSION=2.2.9
```

This tags `v2.2.9` (no version bump needed if the extension is already at `2.2.9`)
and pushes. The workflow then:

- builds `:2.2.9` **and** moves `:latest`
- packs and attaches the **signed CRX** (`docu-kiosk.crx`) + `update.xml`
- publishes a full GitHub release

## Deploy

- Pin prod to the version tag, never `:latest`:
  `image: ghcr.io/gtkycu-it/docu-kiosk:2.2.9`
- Distribute the new CRX/update.xml to staff browsers.
- Rollback = edit the compose pin back to the previous known-good version and
  `docker compose up -d`. Keep the previous tag pinned until the new one has run
  at least a week.

## Why this exists

v2.2.5 shipped a fail-closed CORS rewrite that broke the kiosk WebSocket (blank
"Ready for member" screen) — released straight to `:latest`, caught only in prod.
The RC gate exists so the kiosk flow, send-to-kiosk, bypass, and CORS rejection are
all exercised on staging before any prod-facing tag moves.
