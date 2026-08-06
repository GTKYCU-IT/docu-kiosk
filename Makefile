.PHONY: all build web server extension clean \
        dev dev-web dev-broker run \
        test test-race vet \
        docker-build docker-up docker-down \
        release \
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
	DOCU_KIOSK_TOKEN_SECRET=dev go run ./cmd/server

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

clean: ## Remove build artifacts
	rm -rf web/dist extension/dist tmp

## Release
release: ## Cut a release — bumps extension version, tags, and pushes (VERSION=x.y.z)
	@test -n "$(VERSION)" || (echo "Error: VERSION is not set. Usage: make release VERSION=x.y.z"; exit 1)
	@test -z "$$(git status --porcelain)" || (echo "Error: working tree is dirty"; exit 1)
	cd extension && npm version $(VERSION) --no-git-tag-version
	git add extension/package.json extension/package-lock.json
	git commit -m "chore: bump to $(VERSION)"
	git tag v$(VERSION)
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
