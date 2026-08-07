#!/usr/bin/env bash
# Bring the full stack up with docker compose and run the smoke or e2e suite against it, then
# tear down. Single source: invoked by `make ci-smoke`/`ci-e2e` and by the CI smoke/e2e jobs,
# so the check can't differ between local and CI. (CI adds only failure-artifact upload.)
set -euo pipefail

mode="${1:?usage: ci-compose-check.sh smoke|e2e}"
cd "$(git rev-parse --show-toplevel)"

[ -f .env ] || cp .env.example .env
# On Linux the docker socket is group-owned; grant the platform container that gid so it can
# reach the daemon (the .env.example default of 0 is only correct on Docker Desktop). No-op on
# macOS/Desktop, where `stat -c` isn't available and gid 0 is right.
if gid=$(stat -c '%g' /var/run/docker.sock 2>/dev/null); then
  if grep -q '^OSCTF_DOCKER_GID=' .env; then
    sed -i.bak "s/^OSCTF_DOCKER_GID=.*/OSCTF_DOCKER_GID=$gid/" .env && rm -f .env.bak
  else
    echo "OSCTF_DOCKER_GID=$gid" >> .env
  fi
fi

trap 'docker compose down -v' EXIT

case "$mode" in
  smoke)
    docker compose up -d --build --wait
    set -a; source .env; set +a
    BASE_URL=http://localhost:8080 bash scripts/smoke.sh
    ;;
  e2e)
    docker build -t osctf/example-per-team-web:0.2 examples/challenges/per-team-web/src
    docker compose up -d --build --wait
    set -a; source .env; set +a
    ( cd dashboard && npm ci && npx playwright install --with-deps chromium \
        && BASE_URL=http://localhost:8080 npm run e2e )
    ;;
  *)
    echo "unknown mode: $mode (want smoke|e2e)" >&2; exit 2 ;;
esac
echo "compose $mode: OK"
