# OSCTF — single entrypoint for every dev task. Every target is non-interactive.
# Tool versions are pinned HERE (the one place); CI uses these same targets.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

# --- pinned tool versions -----------------------------------------------------
OAPI_CODEGEN_VERSION   := v2.4.1
SQLC_VERSION           := v1.28.0
GOOSE_VERSION          := v3.24.1
VACUUM_VERSION         := v0.17.0
GOLANGCI_LINT_VERSION  := v2.12.2

GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

# Compose invocation for local dev services.
DEV_COMPOSE := docker compose -f docker-compose.yml -f docker-compose.dev.yml

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# --- setup --------------------------------------------------------------------
.PHONY: setup
setup: setup-tools ## Install pinned tools and dashboard deps
	cd dashboard && npm ci

.PHONY: setup-tools
setup-tools: ## Install pinned Go codegen/lint tools via go install
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	go install github.com/daveshanley/vacuum@$(VACUUM_VERSION)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# --- dev ----------------------------------------------------------------------
.PHONY: dev
dev: ## Start Postgres, Redis, MinIO for local development
	$(DEV_COMPOSE) up -d postgres redis minio
	@echo ""
	@echo "Dev services up. Next:"
	@echo "  make dev-api   # run the API on :8080"
	@echo "  make dev-web   # run the dashboard on :5173"
	@echo "  MinIO console: http://localhost:9001"

.PHONY: dev-api
dev-api: ## Run the API locally against dev services
	cd api && set -a && source ../.env && set +a && \
	OSCTF_DATABASE_URL="postgres://osctf:osctf@localhost:55432/osctf?sslmode=disable" \
	OSCTF_REDIS_URL="redis://localhost:6379/0" \
	OSCTF_S3_ENDPOINT="localhost:9000" \
	OSCTF_CORS_DEV_ORIGIN="http://localhost:5173" \
	OSCTF_LOG_FORMAT=text \
	go run ./cmd/platform serve

.PHONY: dev-web
dev-web: ## Run the Vite dev server (:5173, proxies /api -> :8080)
	cd dashboard && npm run dev

.PHONY: dev-down
dev-down: ## Stop dev services
	$(DEV_COMPOSE) down

# --- codegen ------------------------------------------------------------------
.PHONY: generate
generate: ## Regenerate all committed generated code (oapi-codegen, sqlc, TS types)
	cd api && oapi-codegen -config openapi/oapi-codegen.yaml openapi/openapi.yaml
	cd api && sqlc generate
	cd dashboard && npm run generate:api

# --- quality ------------------------------------------------------------------
.PHONY: lint
lint: ## Run all linters (Go, TS, OpenAPI)
	cd api && golangci-lint run
	cd dashboard && npm run lint && npm run typecheck
	vacuum lint -r api/openapi/vacuum-ruleset.yaml -d api/openapi/openapi.yaml

.PHONY: vet-tags
vet-tags: ## Type-check EVERY build tag (integration, dockerint, soak) — catches a compile break in a tagged file that an untagged/-tags integration run never compiles
	cd api && go vet -tags "integration dockerint soak" ./...

.PHONY: test
test: vet-tags ## Type-check every build tag, then run unit tests (Go -short + web)
	cd api && go test ./... -short
	cd dashboard && npm test

.PHONY: test-integration
test-integration: ## Run integration tests (testcontainers spin up PG/Redis/MinIO)
	cd api && go test ./... -run Integration

# --- build --------------------------------------------------------------------
.PHONY: build
build: ## Build the dashboard, embed it, and build the Go binary
	cd dashboard && npm run build
	rm -rf api/internal/webdist/static
	mkdir -p api/internal/webdist/static
	cp -r dashboard/dist/* api/internal/webdist/static/
	cd api && CGO_ENABLED=0 go build -trimpath -tags embed_spa -o platform ./cmd/platform

.PHONY: image
image: ## Build the production Docker image
	docker build -t ghcr.io/osctf/platform:latest .

.PHONY: examples
examples: ## Build the container-kind example challenge images
	@bash scripts/build-examples.sh

.PHONY: smoke
smoke: ## Build the stack, run the smoke test, tear down
	docker compose up -d --build --wait
	BASE_URL=http://localhost:8080 bash scripts/smoke.sh; rc=$$?; \
		docker compose down; exit $$rc

# --- database -----------------------------------------------------------------
.PHONY: migrate-new
migrate-new: ## Create a new empty goose migration: make migrate-new name=<slug>
	@test -n "$(name)" || (echo "usage: make migrate-new name=<slug>" && exit 1)
	goose -dir api/internal/db/migrations create $(name) sql

.PHONY: seed
seed: ## Run 'platform seed' against the dev database
	cd api && set -a && source ../.env && set +a && go run ./cmd/platform seed
