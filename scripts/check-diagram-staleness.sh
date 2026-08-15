#!/usr/bin/env bash
#
# Warns (never fails) when the code a diagram describes has moved past the commit the diagram
# was verified at. The stamp lives in each canvas's subtitle ("verified at HEAD <sha>").
#
# Deliberately COARSE: it treats all non-test Go under cmd/internal/plugin as "code the diagrams describe",
# rather than mapping each diagram to specific packages. Such a map would itself drift and go
# stale — the exact failure this check exists to catch — so we err toward warning instead.
#
# Exit code is always 0: this is a nudge to re-verify, not a gate.
#
# NEGATIVE CONTROL (why this is not noise — do not delete it on that assumption): the diagrams
# were verified at 6c3e2f0, then the v0.3 security pass changed 13 non-test Go files (the
# isolation gate, the Redis-unavailable behaviour, the writable-plugins-dir boot check, and
# their wiring) WITHOUT anyone re-stamping the canvases. Point this check at a canvas still
# stamped 6c3e2f0 and it flags exactly those 13 files — the real drift that went unnoticed for
# 14 commits and was caught only by a manual audit. Break the mechanism (revert a stamp) and the
# warning reappears; that is the proof it works, the same discipline as the -break-readrepair
# control on the scoreboard invariant.
set -euo pipefail
cd "$(dirname "$0")/.."

head_short=$(git rev-parse --short HEAD)
warned=0

for f in docs/architecture/*.excalidraw; do
  base=$(basename "$f")
  sha=$(grep -oE 'verified at HEAD [0-9a-f]{7,40}' "$f" | head -1 | awk '{print $4}')

  if [ -z "$sha" ]; then
    echo "WARN  $base: no 'verified at HEAD <sha>' stamp found"; warned=1; continue
  fi
  if ! git cat-file -e "${sha}^{commit}" 2>/dev/null; then
    echo "WARN  $base: stamped commit $sha is not in this repo (shallow clone, or a bad stamp)"; warned=1; continue
  fi
  if [ "$(git rev-parse "$sha")" = "$(git rev-parse HEAD)" ]; then
    echo "ok    $base: stamped at HEAD ($sha)"; continue
  fi

  gap=$(git rev-list --count "${sha}..HEAD")
  changed=$(git diff --name-only "${sha}..HEAD" -- cmd internal plugin | grep -E '\.go$' | grep -v '_test\.go$' || true)
  if [ -n "$changed" ]; then
    n=$(printf '%s\n' "$changed" | wc -l | tr -d ' ')
    echo "WARN  $base: stamped $sha is $gap commit(s) behind HEAD ($head_short); $n non-test Go file(s) under the module changed since — re-verify and re-stamp:"
    printf '%s\n' "$changed" | head -12 | sed 's/^/        /'
    [ "$n" -gt 12 ] && echo "        ... and $((n - 12)) more"
    warned=1
  else
    echo "ok    $base: $gap commit(s) behind HEAD, but no Go code changed since $sha"
  fi
done

if [ "$warned" -eq 0 ]; then
  echo "diagram-staleness: all canvases current with the code they describe"
else
  echo "diagram-staleness: warnings above are advisory (this check never fails the build)"
fi
exit 0
