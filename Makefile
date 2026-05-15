.PHONY: all build web server extension pack deploy-ext clean \
        dev dev-web dev-broker run \
        test test-race vet \
        docker-build docker-up docker-down \
        cert-export \
        migrate migrate-down migrate-status migrate-create sqlc-gen tools \
        help

DB_PATH ?= ./kiosks.db

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

server: web ## Build broker binary → tmp/server
	go build -o tmp/server ./cmd/server

extension: ## Build Chrome/Edge extension → extension/dist/
	cd extension && npm run build

DEPLOY_USER ?= gtky
DEPLOY_PATH ?= ~/docu-kiosk/extension/public/

pack: ## Sign, pack, and deploy extension (dev machine only — requires Edge + dist.pem; set BROKER_HOST)
	$(eval export $(shell grep '^BROKER_HOST' .env 2>/dev/null || true))
	cd extension && npm run pack
	$(MAKE) deploy-ext

deploy-ext: ## Copy packed extension to server (set BROKER_HOST and optionally DEPLOY_USER/DEPLOY_PATH)
	@test -n "$(BROKER_HOST)" || (echo "Error: BROKER_HOST is not set"; exit 1)
	scp extension/public/docu-kiosk.crx extension/public/update.xml \
		$(DEPLOY_USER)@$(BROKER_HOST):$(DEPLOY_PATH)

clean: ## Remove build artifacts
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
