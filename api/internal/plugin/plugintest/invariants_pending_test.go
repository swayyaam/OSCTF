package plugintest_test

// Invariant coverage map — which of the 12 loader invariants (docs/v0.3/03-plugin-loader.md,
// "Invariants the tests pin") are pinned NOW vs. blocked on a later sub-step, and WHY. Written
// from the spec before the loader exists, so the loader is built to satisfy these, not the
// tests shaped to match whatever the loader turns out to do. The blocked ones cannot yet be
// written even as a failing test: the assertion would call a loader API (Load/state/route/
// reload/stop/boot-sweep) or a domain path that does not exist, so the test would not compile
// — sketching that API now is exactly the implementation-shaping this ordering avoids.
//
// PINNED NOW (doubles_test.go — P3-a transport + real subprocess doubles):
//   - The core never blocks indefinitely on a hung call: the host-side deadline fires (hung +
//     slow doubles). [partial — the semaphore/load-shed half is P3-d]
//   - ABI-major mismatch refused pre-call (wrongabi).
//   - A crash-on-launch plugin never serves (crashlaunch); a mid-call crash errors (crashafter).
//   - A malformed/error response surfaces as a mapped gRPC error, not a silent wrong value.
//   - Kill reaps the child, including a plugin that ignores SIGTERM — no leaked process.
//   - No goroutine outlives a dial->Kill cycle (residue guard, transport scope).
//
// ALREADY PINNED (P1):
//   - Inv 12 "a registry swap is atomic from a reader's perspective" — the auth/scoring/
//     challenge-type registries already carry reader-atomic contention tests under -race
//     (internal/{auth,scoring,challenges}). P3-e's plugin registration reuses those registries;
//     P3-e adds a plugin-driven contention case.
//
// BLOCKED ON P3-c (the loader) — no loader API to compile against yet:
//   - Inv 1  orphan reclamation: the graceful-reap half is pinned now; the pidfile BOOT-SWEEP
//            backstop needs the loader's boot reconciliation. Double: any (SIGKILL the harness).
//   - Inv 2  "a non-ready state never serves": needs the state machine + routing.
//   - Inv 4  crash-loop cap/quarantine: needs the restart cap (crashlaunch/crashafter -> <=5
//            attempts -> terminal `failed`, bounded processes/sockets).
//   - Inv 5  reload idempotent: needs reload (reload x2 -> one PID, one entry, no leak).
//   - Inv 7  full stop cleanup: dial->Kill scope is pinned now; the load->serve->stop lifecycle
//            with the per-plugin context (goleak + fd/PID) needs the loader.
//
// BLOCKED ON P3-d (failure isolation):
//   - Inv 11 resource budget: needs the global in-flight semaphore (OSCTF_PLUGIN_MAX_INFLIGHT)
//            — N slow-double calls beyond the cap shed 503, in-flight bounded, host responsive.
//            Per-plugin latency as a METRIC (osctf_plugin_call_duration) is recorded on the
//            loader's call wrapper, so "consistently slow is observable" lands there too.
//
// BLOCKED ON P3-e (registries + bus) — need the wired domain paths:
//   - Inv 3  registry-never-holds-stopped (revert-before-death) — needs the loader (stop) AND
//            the registry wiring.
//   - Inv 8  challenge-type outage never consumes an attempt — needs the challenge-type
//            registry + the submissions tx (fail CheckFlag; no solve, no attempt decrement).
//   - Inv 9  scoreboard recomputable from the log (fallback off/on) — needs scoring wiring +
//            the scoreboard (force a scoring failure; recompute == served; scored_by=fallback).
//   - Inv 10 dropped notification observable — needs the event bus (fail/hang a subscriber;
//            event dropped + counted + logged, originating action still commits).
