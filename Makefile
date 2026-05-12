.PHONY: all build web server extension clean \
        dev dev-web dev-broker run \
        test test-race vet \
        docker-build docker-up docker-down \
        cert-export \
        help

all: build

## Development
dev: ## Start broker (hot-reload) + frontend dev server together
	$(MAKE) -j2 dev-web dev-broker

dev-web: ## Start Vite dev server
	cd web && npm run dev

dev-broker: ## Start broker with air (hot-reload)
	air

run: ## Run broker with dev secrets (no hot-reload)
	DOCU_KIOSK_TOKEN_SECRET=dev DOCU_KIOSK_REGISTRATION_KEY=dev go run ./cmd/server

## Testing
test: ## Run Go tests
	go test ./...

test-race: ## Run Go tests with race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

## Build
build: web server extension ## Build all components

web: ## Build Vite frontend → web/dist/
	cd web && npm run build

server: web ## Build broker binary → ./server
	go build -o server ./cmd/server

extension: ## Build Chrome/Edge extension → extension/dist/
	cd extension && npm run build

clean: ## Remove build artifacts
	rm -f server
	rm -rf web/dist extension/dist tmp

## Docker
docker-build: ## Build Docker image
	docker compose build

docker-up: ## Start stack in the background
	docker compose up -d

docker-down: ## Stop stack
	docker compose down

## Certificates
CERT_FILE ?= docu-kiosk-ca.crt

cert-export: ## Export Caddy's root CA cert for distribution (→ docu-kiosk-ca.crt)
	docker compose exec caddy cat /data/caddy/pki/authorities/local/root.crt > $(CERT_FILE)
	@echo "Exported to $(CERT_FILE) — distribute to MSR workstations and kiosk tablets"

## Help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'
