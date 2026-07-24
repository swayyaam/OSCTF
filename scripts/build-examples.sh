#!/usr/bin/env bash
# Build the example challenge container images and (re)generate committed assets.
# Invoked by `make examples`. Images are tagged osctf/example-<slug>:0.1 and are
# NOT pushed — the seeder creates the challenge rows; deploy happens from the
# admin panel once these local images exist (docs/v0.1/13-example-challenges.md).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EX="$ROOT/examples/challenges"

echo "==> regenerating standard-challenge assets"
( cd "$EX/hidden-in-plain-sight" && go run src/make_asset.go )
printf '%s' 'SjVKVUdWQ0dQTldHQzZMRk9KWlY2MzNHTDVSR0M0M0ZMNVFYRVpLN05aWFhJWDNET0o0WEE1RFBQVT09PT09PQ==' \
  > "$EX/base-what/files/encoded.txt"

echo "==> compiling the xor-me checker ELF (linux/amd64)"
docker run --rm --platform linux/amd64 -v "$EX/xor-me:/w" -w /w gcc:13 \
  sh -c 'gcc -O2 -o files/checker src/checker.c && strip files/checker'

echo "==> building container challenge images"
for slug in robots-rule cookie-monster env-hunter overflow-lite; do
  echo "    osctf/example-$slug:0.1"
  docker build -t "osctf/example-$slug:0.1" "$EX/$slug/src"
done

echo "==> done. Example images:"
docker images --filter=reference='osctf/example-*' --format '    {{.Repository}}:{{.Tag}}'
