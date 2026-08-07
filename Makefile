.PHONY: all build web server extension clean \
        dev dev-web dev-broker run \
        test test-race vet \
        docker-build docker-up docker-down \
        release rc \
        migrate migrate-down migrate-status migrate-create sqlc-gen tools \
        help

DB_PATH ?= ./kiosks.db

# Broker build identity — injected via -ldflags. BROKER_VERSION is named to
# avoid clashing with the release target's VERSION parameter.
BROKER_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BROKER_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

all: build

## Development
dev: ## Start broker (hot-reload) + frontend dev server together
	$(MAKE) -j2 dev-web dev-broker

dev-web: ## Start Vite dev server
	cd web && npm run dev

dev-broker: ## Start broker with air (hot-reload)
	air

run: ## Run broker with dev secrets (no hot-reload)
	DOCU_KIOSK_TOKEN_SECRET=dev-only-secret-change-me-in-prod-0123456789 \
	AUTH_USERNAME=admin AUTH_PASSWORD=admin1234 \
	go run ./cmd/server

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

server: web ## Build broker binary → tmp/server
	go build -ldflags "-X github.com/calvertjadon/docu-kiosk/internal/version.Version=$(BROKER_VERSION) -X github.com/calvertjadon/docu-kiosk/internal/version.Commit=$(BROKER_COMMIT)" -o tmp/server ./cmd/server

extension: ## Build Chrome/Edge extension → extension/dist/
	cd extension && npm run build

clean: ## Remove build artifacts
	rm -rf web/dist extension/dist tmp

## Release
release: ## Cut a stable release — tags vX.Y.Z, publishes :latest + signed CRX (VERSION=x.y.z)
	@test -n "$(VERSION)" || (echo "Error: VERSION is not set. Usage: make release VERSION=x.y.z"; exit 1)
	@test -z "$$(git status --porcelain)" || (echo "Error: working tree is dirty"; exit 1)
	cd extension && npm version $(VERSION) --no-git-tag-version || true
	git add extension/package.json extension/package-lock.json
	@if git diff --cached --quiet; then echo "extension already at $(VERSION)"; else git commit -m "chore: bump to $(VERSION)"; fi
	git tag v$(VERSION)
	git push origin main v$(VERSION)

rc: ## Cut a release candidate — tag vX.Y.Z-rc.N, extension stays at Chrome-safe X.Y.Z (VERSION=x.y.z-rc.N)
	@test -n "$(VERSION)" || (echo "Error: VERSION is not set. Usage: make rc VERSION=2.2.9-rc.1"; exit 1)
	@echo "$(VERSION)" | grep -q -- '-rc\.' || (echo "Error: VERSION must be a prerelease like 2.2.9-rc.1"; exit 1)
	@test -z "$$(git status --porcelain)" || (echo "Error: working tree is dirty"; exit 1)
	@stable=$$(echo "$(VERSION)" | sed -E 's/-rc\.[0-9]+$$//'); \
	cd extension && npm version "$$stable" --no-git-tag-version || true; \
	cd ..; \
	git add extension/package.json extension/package-lock.json; \
	if git diff --cached --quiet; then echo "extension already at $$stable"; else git commit -m "chore: bump to $$stable (for $(VERSION))"; fi; \
	git tag v$(VERSION); \
	git push origin main v$(VERSION)

## Docker
docker-build: ## Build Docker image
	docker compose build

docker-up: ## Start stack in the background
	docker compose up -d

docker-down: ## Stop stack
	docker compose down

## Database
migrate: ## Apply pending migrations
	goose -dir sql/migrations sqlite3 $(DB_PATH) up

migrate-down: ## Roll back last migration
	goose -dir sql/migrations sqlite3 $(DB_PATH) down

migrate-status: ## Show migration status
	goose -dir sql/migrations sqlite3 $(DB_PATH) status

migrate-create: ## Create a new migration file (NAME=<name>)
	@test -n "$(NAME)" || (echo "Error: NAME is not set"; exit 1)
	goose -dir sql/migrations create $(NAME) sql

sqlc-gen: ## Regenerate internal/database from SQL
	sqlc generate

## Tools
tools: ## Install dev tools (sqlc, goose)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest

## Help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'
