#!/usr/bin/env bash
# Migration up / down-to-0 / up on a throwaway Postgres — proves the down migrations are real
# and the schema round-trips. Single source: invoked by `make ci-api-integration` and by the
# CI 'api integration' job, so the check can't differ between local and CI.
set -euo pipefail

# Be self-contained: go-installed tools land in GOBIN, which isn't necessarily on PATH when
# this runs standalone (via `make` it is, through the Makefile's PATH export).
export PATH="$(go env GOPATH)/bin:$PATH"
command -v goose >/dev/null 2>&1 || go install github.com/pressly/goose/v3/cmd/goose@v3.24.1

# Publish to a Docker-ASSIGNED host port, not a fixed 5432 — so this works on a dev machine
# that already has Postgres on 5432 as well as on a clean CI runner.
cid=$(docker run -d -e POSTGRES_PASSWORD=osctf -e POSTGRES_USER=osctf -e POSTGRES_DB=osctf \
  -p 127.0.0.1::5432 postgres:17-alpine)
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true' EXIT

# Wait until Postgres logs "ready to accept connections" TWICE: the image starts a throwaway
# server for initdb, then restarts to serve. pg_isready can pass on the throwaway, and
# connecting to the published port mid-restart is reset — so match testcontainers' 2x wait.
for _ in $(seq 1 60); do
  [ "$(docker logs "$cid" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 1
done

port=$(docker port "$cid" 5432/tcp | head -1 | sed 's/.*://')
dir="$(git rev-parse --show-toplevel)/api/internal/db/migrations"
dsn="postgres://osctf:osctf@127.0.0.1:${port}/osctf?sslmode=disable"
# Belt-and-suspenders: retry the first connection while the published port settles.
for _ in $(seq 1 15); do
  goose -dir "$dir" postgres "$dsn" status >/dev/null 2>&1 && break
  sleep 1
done
goose -dir "$dir" postgres "$dsn" up
goose -dir "$dir" postgres "$dsn" down-to 0
goose -dir "$dir" postgres "$dsn" up
echo "migrate up/down/up: OK"
