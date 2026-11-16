# Real-Time Chat & Messaging Platform: developer entry points. Run `make help`.
SHELL := /bin/sh
COMPOSE := docker compose --env-file .env -f infrastructure/docker/docker-compose.yml

-include .env
export

.DEFAULT_GOAL := help
.PHONY: help env bootstrap deps frontend-deps ai-deps \
        up up-streaming up-apps down down-volumes ps logs \
        migrate-up migrate-down migrate-status \
        run build test test-integration lint fmt check-health \
        ai-test frontend-build compose-config docs-check

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

# ---- setup ---------------------------------------------------------------------------------
env: ## Create .env with generated secrets (never overwrites)
	@./scripts/init-env.sh

deps: ## Resolve and record Go dependencies (first run: creates go.sum; commit go.mod/go.sum)
	cd backend && go get github.com/jackc/pgx/v5 github.com/redis/go-redis/v9 \
			golang.org/x/crypto github.com/golang-jwt/jwt/v5 \
		&& go get -tool github.com/pressly/goose/v3/cmd/goose \
		&& go mod tidy

frontend-deps: ## Install frontend dependencies with exact versions (creates package-lock.json)
	cd frontend && npm install --save-exact react react-dom \
		&& npm install --save-exact --save-dev vite @vitejs/plugin-react typescript \
			@types/react @types/react-dom @types/node tailwindcss @tailwindcss/vite

ai-deps: ## Create venv and lock files for the AI service
	cd ai-service && python3 -m venv .venv \
		&& .venv/bin/pip install --upgrade pip \
		&& .venv/bin/pip install -e . \
		&& .venv/bin/pip freeze --exclude-editable > requirements.lock \
		&& .venv/bin/pip install -e ".[dev]" \
		&& .venv/bin/pip freeze --exclude-editable > requirements-dev.lock

bootstrap: env deps frontend-deps ai-deps ## One-time setup: env, Go, frontend and AI dependencies

# ---- infrastructure --------------------------------------------------------------------------
up: env ## Start core services (postgres, redis, objectstore) and wait until healthy
	$(COMPOSE) --profile core up -d --wait

up-streaming: env ## Start core + kafka and kafka-ui
	$(COMPOSE) --profile core --profile streaming up -d --wait

up-apps: env ## Start core + backend, frontend, ai-service (needs `make bootstrap` first)
	$(COMPOSE) --profile core --profile apps up -d --build --wait

down: ## Stop all services (keeps data volumes)
	$(COMPOSE) --profile core --profile streaming --profile apps down

down-volumes: ## Stop all services and DELETE data volumes
	$(COMPOSE) --profile core --profile streaming --profile apps down -v

ps: ## Show service status
	$(COMPOSE) --profile core --profile streaming --profile apps ps

logs: ## Tail logs (usage: make logs S=postgres)
	$(COMPOSE) --profile core --profile streaming --profile apps logs -f $(S)

compose-config: ## Validate the compose file renders with the current .env
	$(COMPOSE) --profile core --profile streaming --profile apps config --quiet

# ---- database --------------------------------------------------------------------------------
migrate-up: ## Apply migrations (DATABASE_URL from .env)
	cd backend && go tool goose -dir migrations postgres "$(DATABASE_URL)" up

migrate-down: ## Roll back the most recent migration
	cd backend && go tool goose -dir migrations postgres "$(DATABASE_URL)" down

migrate-status: ## Show migration status
	cd backend && go tool goose -dir migrations postgres "$(DATABASE_URL)" status

# ---- backend ---------------------------------------------------------------------------------
run: ## Run the backend on the host (needs `make up` and `make migrate-up`)
	cd backend && go run ./cmd/api

build: ## Build the backend binary into backend/bin
	cd backend && CGO_ENABLED=0 go build -trimpath -o bin/api ./cmd/api

test: ## Go unit tests with the race detector
	cd backend && go test -race -count=1 ./...

test-integration: ## Integration tests: PostgreSQL schema/store, Redis limiter, end-to-end API (needs `make up` and `make migrate-up`)
	cd backend && go test -tags=integration -race -count=1 ./internal/...

lint: ## golangci-lint (backend) and ruff + mypy (ai-service)
	cd backend && golangci-lint run ./...
	cd ai-service && .venv/bin/ruff check . && .venv/bin/mypy app

fmt: ## Format Go and Python code
	cd backend && gofmt -w . 
	cd ai-service && .venv/bin/ruff format .

check-health: ## Probe the running backend
	@curl -fsS http://127.0.0.1:8080/healthz && echo && curl -sS http://127.0.0.1:8080/readyz && echo

# ---- other services --------------------------------------------------------------------------
ai-test: ## AI service tests
	cd ai-service && .venv/bin/pytest -q

frontend-build: ## Typecheck and build the frontend
	cd frontend && npm run build

# ---- docs ------------------------------------------------------------------------------------
REQUIRED_DOCS = README.md docs/PRD.md docs/functional-requirements.md \
 docs/non-functional-requirements.md docs/HLD.md docs/LLD.md docs/database-design.md \
 docs/api-specification.md docs/event-catalog.md docs/security.md docs/scalability.md \
 docs/failure-scenarios.md docs/testing-strategy.md docs/deployment.md docs/observability.md

docs-check: ## Verify all required documentation files exist
	@missing=0; for f in $(REQUIRED_DOCS); do [ -f $$f ] || { echo "MISSING: $$f"; missing=1; }; done; \
	[ -d docs/adr ] || { echo "MISSING: docs/adr/"; missing=1; }; \
	[ $$missing -eq 0 ] && echo "All required docs present."
