# web

Svelte 5 SPA served by the broker as the kiosk frontend.

## Development

```bash
npm run dev    # dev server at localhost:5173 (proxies /api and /ws to broker at :8080)
npm run build  # production build → dist/
```

Run alongside the broker with `make dev` from the repo root.