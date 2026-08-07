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

# Build tags vet-tags compiles. TAG_ALLOWLIST names tags deliberately NOT compiled here,
# with the reason. The guard in vet-tags fails if the tree uses a tag in neither list, so a
# fifth build tag added later can't silently reintroduce the partial-compile gap — the same
# shape as the policy-table route-coverage gate.
VET_TAGS := integration dockerint soak
# embed_spa: compiled only in the image build — it embeds the built SPA (webdist/static),
# absent in a plain checkout, so vetting it here would fail on the missing embed.
TAG_ALLOWLIST := embed_spa

.PHONY: vet-tags
vet-tags: ## Type-check every test build tag; fail if the tree uses an uncovered tag
	@present=$$(grep -rhoE '^//go:build .*' api --include='*.go' | sed 's|//go:build||' | grep -oE '[a-z_][a-z0-9_]*' | sort -u); \
	known=" $(VET_TAGS) $(TAG_ALLOWLIST) "; \
	missing=""; \
	for t in $$present; do case "$$known" in *" $$t "*) ;; *) missing="$$missing $$t" ;; esac; done; \
	if [ -n "$$missing" ]; then \
	  echo "vet-tags: build tag(s) present in the tree but covered by neither VET_TAGS nor TAG_ALLOWLIST:$$missing"; \
	  echo "  add each to VET_TAGS (to compile it here) or TAG_ALLOWLIST (with a reason) in the Makefile"; \
	  exit 1; \
	fi; \
	echo "vet-tags: all tree build tags covered ($$(echo $$present | tr '\n' ' '))"
	cd api && go vet -tags "$(VET_TAGS)" ./...

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

# --- CI parity ----------------------------------------------------------------
# The recurring failure has one shape: a local gate whose success is compatible with not
# having run part of CI (tests dropped by name-filter, a job that exercised nothing, a build
# tag never compiled, a spec-guarding job skipped). The fix is mechanical, not a checklist:
#
#   - ONE definition per CI job (the ci-<job> targets below); CI calls the SAME target, so a
#     job cannot pass locally while running different commands in CI.
#   - `make ci-local` runs every job that needs no docker-compose stack; `make ci-local-full`
#     adds the image/smoke/e2e tier. "Green" means `ci-local` passed, not a remembered list.
#   - `ci-sync-check` DERIVES the job list from .github/workflows/ci.yml and fails if any job
#     is covered by neither target — so a new CI job can't be added without appearing locally.
#     Same shape as the vet-tags tag-coverage guard and the policy-table route gate.
CI_LOCAL_JOBS      := generate-drift api-lint api-test api-integration web
CI_LOCAL_FULL_JOBS := image smoke e2e

.PHONY: ci-sync-check
ci-sync-check: ## Fail if any CI job is not runnable via ci-local / ci-local-full
	@ci_jobs=$$(awk '/^jobs:/{j=1;next} j && /^  [a-z][a-z0-9-]+:[[:space:]]*$$/{sub(/:.*/,"");gsub(/ /,"");print}' .github/workflows/ci.yml); \
	covered=" $(CI_LOCAL_JOBS) $(CI_LOCAL_FULL_JOBS) "; \
	missing=""; for jb in $$ci_jobs; do case "$$covered" in *" $$jb "*) ;; *) missing="$$missing $$jb";; esac; done; \
	stale=""; for jb in $(CI_LOCAL_JOBS) $(CI_LOCAL_FULL_JOBS); do printf '%s\n' $$ci_jobs | grep -qx "$$jb" || stale="$$stale $$jb"; done; \
	if [ -n "$$missing" ]; then \
	  echo "ci-sync-check: CI job(s) not runnable locally:$$missing"; \
	  echo "  add each to CI_LOCAL_JOBS (no-compose tier) or CI_LOCAL_FULL_JOBS (compose tier) and wire a ci-<job> target"; \
	  exit 1; fi; \
	if [ -n "$$stale" ]; then \
	  echo "ci-sync-check: declared job(s) no longer in ci.yml (stale, remove them):$$stale"; exit 1; fi; \
	echo "ci-sync-check: all $$(printf '%s\n' $$ci_jobs | grep -c .) CI jobs are covered locally"

.PHONY: ci-local
ci-local: ci-sync-check vet-tags ci-generate-drift ci-api-lint ci-api-test ci-api-integration ci-web ## Run every CI job that needs no compose stack (the pre-push gate)
	@echo "== ci-local PASS: matches CI jobs [$(CI_LOCAL_JOBS)] + vet-tags/tag-coverage =="

.PHONY: ci-local-full
ci-local-full: ci-local ci-image ci-smoke ci-e2e ## ci-local plus the image / smoke / e2e (docker-compose) tier
	@echo "== ci-local-full PASS: all CI jobs run locally =="

# One target per CI job — CI invokes these SAME targets (see .github/workflows/ci.yml), so a
# job's commands cannot drift between local and CI.
.PHONY: ci-generate-drift
ci-generate-drift: generate ## CI job 'generate drift': regenerate + fail on any diff
	git diff --exit-code

.PHONY: ci-api-lint
ci-api-lint: ## CI job 'api lint': golangci-lint + vacuum (zero warnings)
	cd api && golangci-lint run
	vacuum lint -r api/openapi/vacuum-ruleset.yaml -d api/openapi/openapi.yaml

.PHONY: ci-api-test
ci-api-test: ## CI job 'api test': unit tier, race + shuffle (tag-selected, no -run filter)
	cd api && go test ./... -race -shuffle=on

.PHONY: ci-api-integration
ci-api-integration: ## CI job 'api integration': integration + dockerint + soak + migrations
	cd api && go test ./... -race -shuffle=on -tags integration
	cd api && OSCTF_ISOLATION_ENFORCED=1 go test -tags dockerint -race -shuffle=on ./internal/runtime/...
	@out=$$(mktemp); cd api && go test -tags soak -run TestSoak -v ./internal/soak -timeout 6m -args -duration=2m -seed=1 | tee $$out; \
		grep -q '^--- PASS: TestSoak' $$out || { echo '::error:: soak produced no PASS for TestSoak — it exercised nothing'; rm -f $$out; exit 1; }; rm -f $$out
	@bash scripts/ci-migrate-updownup.sh

.PHONY: ci-web
ci-web: ## CI job 'web': dashboard lint + typecheck + test + build
	cd dashboard && npm ci
	cd dashboard && npm run lint
	cd dashboard && npm run typecheck
	cd dashboard && npm test
	cd dashboard && npm run build

.PHONY: ci-image
ci-image: ## CI job 'image': build the production image
	docker build -t osctf/platform:ci .

.PHONY: ci-smoke
ci-smoke: ## CI job 'smoke': compose up, smoke.sh, tear down
	@bash scripts/ci-compose-check.sh smoke

.PHONY: ci-e2e
ci-e2e: ## CI job 'e2e': compose up, playwright, tear down
	@bash scripts/ci-compose-check.sh e2e

# --- database -----------------------------------------------------------------
.PHONY: migrate-new
migrate-new: ## Create a new empty goose migration: make migrate-new name=<slug>
	@test -n "$(name)" || (echo "usage: make migrate-new name=<slug>" && exit 1)
	goose -dir api/internal/db/migrations create $(name) sql

.PHONY: seed
seed: ## Run 'platform seed' against the dev database
	cd api && set -a && source ../.env && set +a && go run ./cmd/platform seed
