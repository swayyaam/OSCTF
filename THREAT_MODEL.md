# OSCTF Threat Model

OSCTF runs **deliberately hostile code**: participants get root inside their own challenge
containers, on a host you run. That makes "who is the attacker and what can they reach" the
first question, so this document is organized by **adversary**, not by feature. For each one it
states what is reachable and marks it:

- **Mitigated** — a control stops it, and the **named Go test that pins that control** is cited.
- **Accepted** — a residual risk we carry on purpose, with the reason and where it is documented.
- **Out of scope** — outside what OSCTF undertakes to defend (mirrored from [`SECURITY.md`](SECURITY.md)).

**This is a live document, not aspiration.** Almost every mitigation below names the test that
enforces it. The tests are the source of truth — the testing contract in
[`AGENTS.md`](AGENTS.md) is where they are declared "never weakened to make a build go green" —
and this file is the human-readable map of them. If a referenced test is deleted or weakened, the
guarantee is gone and this file is wrong; fix both together. Where a control is enforced only *by
construction* (e.g. `crypto/subtle`) with no behavioural test, or is *documented but not yet
enforced*, that is stated plainly rather than dressed up as a mitigation.

To report a vulnerability, and for the maintainer's disclosure policy and in/out-of-scope
statement, see [`SECURITY.md`](SECURITY.md). For where these controls sit in the system — the
isolation gate on the container-deploy path, the fail-closed rate limiter, the plugin
process boundary — see the [architecture diagrams in `docs/architecture/`](docs/architecture/).

## Trust boundaries

| Actor | Trust | Notes |
|---|---|---|
| Participant browser / CLI / API token | **Untrusted** | Authenticated but adversarial. |
| Code inside a challenge container | **Untrusted (hostile by design)** | Participants run as root here on purpose. |
| Plugin subprocess | **Trusted within its type, isolated only for availability** | Process isolation stops a crash taking the host down; it does **not** sandbox authority. An auth plugin with the `password` capability sits *inside* the credential trust boundary. |
| Platform binary · operator/admin · host · Docker daemon | **Trusted** | The Docker socket mount is root-equivalent on the host by design. |
| Postgres | **Authority** | The single source of truth. |
| Redis | **Derived / ephemeral** | Sessions, rate-limit buckets, scoreboard cache — reconstructible. The **freeze snapshot** is the one exception (see §1). |

---

## 1. A participant with an account

Authenticated, adversarial, using the API and WebSocket as themselves.

**Other teams' flags — Mitigated.** Static flags are compared in constant time
(`subtle.ConstantTimeCompare`, `submissions/service.go:436`). A per-team instance flag is
**redacted before it is ever stored or echoed** — on a correct or shared submission the stored
`provided` value becomes a placeholder (`redactedFlag`, `service.go:32`, applied `:280`), so the
admin submissions view can never replay one team's instance secret. A submission that matches
*another* team's instance flag raises a **detection-only sharing signal**: it stays `correct:false`,
the flag value is never recorded or returned, and only a metric + audit row are written
(`comparePerInstance` → `service.go:307`). Pinned by
`handlers/flag_containment_integration_test.go :: TestFlagContainmentIntegration` (a secret-scanner
that sweeps REST, WebSocket, logs, and `/metrics`) and
`submissions/service_perinstance_integration_test.go :: TestPerInstanceSubmissionIntegration`.

**Other teams' instances — Mitigated.** Every per-team instance operation is scoped to the
caller's own team (`callerTeam`, `handlers/instances_scheduler.go:26`); `Scheduler.Stop`/`Extend`
resolve `GetTeamInstance(challenge, callerTeam)` and return `ErrNotFound` when the caller's team has
none. There is **no participant instance-by-id route** — the only id-addressed route is admin-only.
A team cannot even tell whether another team has an instance: pinned by
`handlers/enumeration_integration_test.go :: TestEnumerationOtherTeamInstanceIndistinguishable`
(stop/extend/detail against a challenge another team has running is byte-identical to one nobody
does) and `handlers/instances_scheduler_integration_test.go :: TestPerTeamInstanceEndpointsIntegration`.

**The scoreboard and other data during a freeze — Mitigated.** During a freeze, non-admins are
served the frozen snapshot, captured **exactly once** via Redis `SETNX` so a later racing recompute
cannot overwrite it and leak post-freeze solves (`scoreboard/service.go:156`, `MaybeSnapshotFreeze`
`:267`). Public team/user pages filter solves through `freezeHidesSolvesAfter`
(`handlers/guards.go:35`): admins and a resource's own team see live data, everyone else sees
pre-freeze only. Pinned by
`handlers/scoreboard_freeze_integration_test.go :: TestScoreboardSolveDuringFreezeInvisibleUntilThaw`,
`handlers/freeze_leak_integration_test.go :: TestPublicRoutesDoNotLeakPostFreezeSolves`, and
`scoreboard/freeze_race_integration_test.go :: TestFreezeSnapshotWrittenOnceUnderConcurrency`. The
freeze snapshot is the one piece of Redis state whose loss is *not* silently recoverable — a
transient read failure fails closed to last-known-state rather than serving a live board
(`TestFreezeTransientReadWithoutCacheFailsClosed`).

**Admin routes — Mitigated, fail-closed.** `requireAdmin` (`handlers/guards.go:139`) **re-reads the
user row on every call** — so a ban or demotion takes effect on the next request — and fails closed:
anything that is not an existing, non-banned `admin` is `Forbidden`. Coverage is structural: every
OpenAPI route must declare an authorization policy or CI fails
(`handlers.TestPolicyTableCoversEveryRoute`), and the policy is enforced across identity × phase by
`handlers.TestPolicyMatrixIntegration` / `TestPolicyMatrixWebSocketIntegration`. The explicit
fail-closed case — a missing dependency hides rather than leaks — is pinned by
`handlers.TestFreezeFailsClosedWithoutEvents`. `requireAdmin` fires *before* any id resolution, so an
admin route leaks no existence oracle to a non-admin
(`enumeration_integration_test.go :: TestEnumerationAdminRoutesNoExistenceLeak`).

**Enumerating hidden or unreleased challenges — Mitigated.** An invisible or not-yet-released
challenge returns `404`, identical to one that does not exist (`challenges/participant.go:63`; same
on the submit and download paths). Pinned by
`handlers/enumeration_integration_test.go :: TestEnumerationHiddenChallengeIndistinguishable`, which
asserts indistinguishability in **status, body, and median timing**.

**Other participants' data — Mitigated (with one gap).** Hidden teams are excluded from the public
list (`TestListTeamsExcludesHiddenIntegration`); a public profile exposes only username + solves; an
invite code is shown only to a team member. A hidden **user** profile returns `404` to non-admins
(`handlers/users.go:20`) — this is enforced in code and declared in the policy table, but **no test
currently probes a hidden user profile** (test gap, not a code gap; tracked below).

---

## 2. A participant with code execution inside their own challenge container

The sharpest adversary: root inside a container OSCTF started, which is the entire point of a CTF
challenge. The question is blast radius.

**Other teams' containers and networks — Mitigated on Linux; refused-by-default elsewhere.** Each
team's containers sit on their own Docker bridge; cross-team traffic is blocked by Docker's
isolation chains **on native Linux only** (`runtime/docker.go`, `ensureNamedNetwork` `:666`,
per-team network `manager.go:499`). On **Docker Desktop (macOS/Windows) this does not hold** once a
host port is published — and every instance publishes one — so team A could reach team B's published
port. This is a property of the Desktop VM, not a bug OSCTF can fix. The platform **verifies
isolation at boot** (`VerifyIsolation`, `docker.go:93`, stands up two masquerade-off bridges and
cross-probes them) and, when it is not enforced, **fails closed: container challenges are refused**
(`manager.isolationGate`, called at the top of `DeployForTeam`) — a missable warning became an
unmissable wall. The only escape is an explicit `OSCTF_ALLOW_UNISOLATED_INSTANCES=true`, which is
logged loudly at boot and on every unisolated deploy so "we ran unisolated" is in the event's own
logs. Unknown/unverified isolation also fails closed, so a startup window cannot leak an unisolated
deploy. Linux mitigation pinned (`dockerint`) by
`runtime/docker_hardening_integration_test.go :: TestDockerPerTeamIsolationIntegration` and
`:: TestVerifyIsolationSelfCheckIntegration`; the gate (off refuses, on permits, unknown fails
closed) by `runtime.TestIsolationGate`. **Run real events on a Linux host** — the override is for a
local trial only ([`docs/v0.2/03-runtime.md`](docs/v0.2/03-runtime.md), issue
[#2](https://github.com/osctf/platform/issues/2)).

**The host — Accepted (bounded, not sandboxed).** The Docker socket is mounted into the platform
and is **root-equivalent on the host by design** — documented in the runtime and deployment docs;
run events on a dedicated host/VM. Hardening only shrinks blast radius *inside* a challenge
container: `cap-drop ALL`, `no-new-privileges`, read-only rootfs, memory/CPU/pids limits, and
`noexec,nosuid` tmpfs for writable paths are all applied at deploy (`runtime/docker.go:257`), pinned
by `runtime/docker_hardening_integration_test.go :: TestDockerHardeningIntegration`. Note the
runtime **deliberately does not force a non-root user** (participants run as root on purpose), so
the escalation guards that actually bite are the capability/priv/rootfs set, not UID separation.

**The platform's own API, from inside a container — Accepted (documented residual risk).** Egress-off
uses `enable_ip_masquerade=false`, not Docker's `Internal:true`, so the bridge gateway stays
addressable and a container can reach the host and **any published host port, including the platform
API**, even with egress disabled (`runtime/docker.go:643`,
[`docs/v0.2/03-runtime.md`](docs/v0.2/03-runtime.md) reachability table). This is a knowingly-carried
weakness: the documented mitigation is to **firewall the event host**, and the real fix (proxy
instances instead of publishing host ports) is deferred. **There is no test asserting the API is
unreachable from a container — because it is not blocked.** Stated here so it is not mistaken for a
guarantee.

**Docker state — Mitigated (reconciled, never trusted).** The runtime treats the daemon as ground
truth to be aligned with the DB, never as authority: orphan containers (no row) are removed, stale
ones GC'd, containers whose `osctf.instance_id` label is missing are flagged but never removed
(they cannot be safely identified), and empty per-team bridges are GC'd unless the team has a fresh
row. The decision function is pure (`runtime/reconcile.go:131`), pinned by
`runtime.TestReconcileDecisions` (+ action-order and future-row variants) and, end-to-end, by the
`scheduler/reconcile_integration_test.go` suite; the adopt/GC label keys are frozen by
`runtime.TestLabelContract`.

**Flags leaking to a container's participant surface — Mitigated.** A reusable containment scanner
sweeps the static flag, per-instance flag, a challenge-type sentinel, and a live API token against
**every** participant-facing surface — REST on both `/api/v0` and `/api/v1`, WebSocket frames on
connect and reconnect (normal and frozen), `/metrics`, structured logs, and `audit_log` rows —
walking JSON at any depth. Pinned by
`handlers/flag_containment_integration_test.go :: TestFlagContainmentIntegration`, with a continuous
`flag-leak` invariant in the soak. Per the testing contract, a new participant-facing endpoint must
be added to the scanner's probe list.

---

## 3. A malicious or compromised plugin

Plugins are separate OS processes over gRPC. Process isolation buys **availability, not
authorization**.

**What the ABI exposes — Mitigated by contract.** The proto is the whole surface, and each type's
request carries only what that type needs. A challenge-type plugin's `CheckRequest.submitted` is the
**user's guess, never the flag** — the host builds the request from the guess and passes empty
config/instance maps (`submissions/service.go:146`, comment "Never send the flag"); the proto marks
it so (`plugin/proto/plugin.proto:118`). Scoring receives `(initial, min, decay, solves)` — no
secrets; notification receives explicitly non-secret, non-PII event data. That flags never reach a
plugin surface is covered by the same `TestFlagContainmentIntegration`.

**An auth plugin with the `password` capability — Accepted, and equivalent to a compromised core.**
This is stated plainly in the design, not glossed: a password-capability auth plugin sees the
plaintext credential a user submits, so a malicious one **harvests credentials that may work
elsewhere** (a corporate directory, email, whatever the user reused) — "equivalent to a compromised
core" ([`docs/v0.3/04-plugin-interfaces.md`](docs/v0.3/04-plugin-interfaces.md)). Installing an auth
plugin is therefore an operator trust decision on par with replacing the binary. What *is* enforced:
built-in providers are override-protected, so a plugin cannot silently hijack `email`
(`auth/registry.go:58`, pinned by `auth.TestAuthRegistryBuiltinOverrideProtected`).

What is **not yet enforced — and the most important line in this document**: auth return-path
validation (a provider's returned `Identity` cannot mint an admin, set roles, or bind to an existing
account without proof) is *specified* in the ABI trust docs but is **not implemented and not
tested**. That means the docs describe a defense that does not exist. It is harmless **only because
no auth plugin can load** — the composition root's auth registrar arm returns `nil`
(`cmd/platform/plugin_registrar.go`, no `auth` case), so there is no untrusted return path to defend
yet. That safety disappears the moment auth-plugin registration is wired (milestone **M3**).
**Therefore the return-path validation is a hard precondition of M3 — it ships before or with auth
registration, never after** ([`docs/v0.3/10-milestones.md`](docs/v0.3/10-milestones.md) records this
as a security precondition). Until then: no auth plugin loads, and this gap must not outlive the
`nil` arm that makes it safe.

**Plugin failure — Mitigated, fail-safe per type.** Isolation means a panicking or hung plugin dies
alone: crash-loops quarantine, a slow plugin cannot stall others, and boot never gates serving
(`plugin/inflight_test.go :: TestPerPluginCapDoesNotDelayOtherPlugins`,
`plugin/supervisor_test.go :: TestCrashOnLaunchQuarantinesAtCap`,
`cmd/platform/serve_boot_test.go :: TestBootDoesNotGateServing`). On failure each type fails in its
*safe* direction: a challenge-type outage rejects-and-retries with **no attempt consumed**
(`submissions/service_plugin_integration_test.go :: TestPluginRejectRetryConsumesNoAttempt`,
`:: TestPluginSwapCheckPrecedesAttemptCheck`); scoring is off the read path and records a
`fallback`/`pending` value so the board stays recomputable
(`service_scoring_integration_test.go :: TestPluginScoreFallbackRecorded`,
`:: TestPluginScorePendingWhenDeferred`); a dropped notification is always counted, and the action
still commits (`events/bus_test.go :: TestBackpressureDropsCounted`, `:: TestDeliveryErrorCounted`).

**An `ai-challenge` plugin holding provider credentials — Accepted, a larger position (planned).**
*(Planned functionality — [`docs/ai-challenges.md`](docs/ai-challenges.md) is a design, not shipped;
recorded here so the position is on the map before the code is.)* An AI-security challenge-type plugin
is a bigger trust position than a flag-checker: it holds a model provider's API key (`sdk.Config`,
env-only), makes **outbound network calls** to that provider, and executes the challenge's tools.
"Isolated for availability, not authorization" still applies — process isolation does not stop a
compromised such plugin from exfiltrating its provider key, running unbounded spend against it, or
abusing its own tool execution. Reference challenges keep tools **inert** (the win is the recorded
call, not execution); a real tool needs a sandbox the platform does not provide (next line). Installing
an ai-challenge plugin is an operator trust decision on the footing of the outbound credentials it
carries.

**Out of scope:** a syscall sandbox for plugin code. v0.3 does not add one — a plugin is trusted
within its type ([`docs/v0.3/03-plugin-loader.md`](docs/v0.3/03-plugin-loader.md)).

---

## 4. A leaked or stolen API token

**Cannot exceed its owner — Mitigated.** Two independent gates: a scope gate in middleware
(`httpserver/token_middleware.go:62`; an empty scope set grants nothing; "scope never grants what
the role lacks") and a **live role read on every request** (`auth/token.go:175`, no cache). Pinned by
`httpserver/token_middleware_test.go :: TestScopeAllows` and, end-to-end,
`handlers/token_authz_matrix_integration_test.go :: TestTokenAuthzMatrixIntegration` (a token is
`cookie ≡ token` per route, scope only narrows) and
`handlers/token_auth_integration_test.go :: TestTokenAuthIntegration` (role∩scope; a demotion takes
effect on the next request).

**Immediate revocation — Mitigated.** Revoke deletes the row, and because role/ban/existence are
read live every request, revocation, expiry, ban, and demotion all take effect on the **next
request** with no cache window (`auth/token.go:215`; the code comment explicitly refuses a token
cache "because it reintroduces a window"). Pinned by `TestTokenAuthIntegration` (revoked → 401) and
`handlers/token_endpoints_integration_test.go :: TestTokenEndpointsIntegration`.

**No self-perpetuation — Mitigated.** Token-management routes are **session-only**: a bearer
credential is rejected there regardless of scope or role, so a leaked token cannot mint a fresh one
to outlive its own revocation (`handlers/token_handlers.go:18`, `requireSession`). Pinned by
`TestTokenAuthzMatrixIntegration` (a full-scope bearer gets `403` on token-management routes).

---

## 5. An operator misconfiguration

The operator is trusted, but the platform still tries to catch the misconfigurations that silently
weaken it.

**`TrustProxy`, both directions — Mitigated one way, undetectable the other.** With
`OSCTF_TRUST_PROXY=false` (default) the server logs a **one-time warning** the first time it sees a
forwarded header — the signal that a proxy is in front but `X-Forwarded-For` is being ignored, which
collapses every per-IP limit onto the proxy's IP (`httpserver/middleware_proxy.go:20`, pinned by
`TestProxyMisconfigWarn`). The **reverse** misconfiguration — `TrustProxy=true` with no proxy, so a
client can forge `X-Forwarded-For` and evade per-IP limits — is **not auto-detectable** and is called
out as such in the code; it is an operator responsibility (documented in the deployment guide).

**No usable login method — Mitigated (refuse to boot).** If email login is disabled and no other
auth provider is registered, the platform **aborts startup** rather than serving a login-less
deployment (`cmd/platform/main.go:390`; `auth.HasUsableLogin`, pinned by
`auth.TestAuthRegistryHasUsableLogin`).

**A writable plugins directory — Detected at boot.** A plugins directory the process can write to is
a persistence path: a compromised core could drop a plugin binary + manifest there for the next boot
to launch as the platform. The recommended posture is a **read-only mount**
([`docs/v0.3/03-plugin-loader.md`](docs/v0.3/03-plugin-loader.md)). The loader now **probes this at
boot and logs a loud `SECURITY` warning if the directory is writable** (`plugin/boot.go`,
`pluginsDirWritable` — a create-and-remove probe, so a read-only bind mount is correctly detected as
not-writable even when its mode bits say otherwise), pinned by `plugin.TestPluginsDirWritable`. This
is detection, not prevention — it does not stop a write, it makes the misconfiguration
observable — so harden the mount per the deployment doc; the warning is the backstop when it is
missed.

Other misconfigurations — no reverse proxy, rate limits disabled, the default admin password left
unchanged — are the operator's responsibility per
[`docs/v0.1/10-deployment.md`](docs/v0.1/10-deployment.md) and are out of scope (below).

---

## 6. An unauthenticated network attacker

No account; only the public surface.

**Registration and login flooding — Mitigated.** Registration and login are per-IP rate limited
(`register-ip` / `login-ip`, defaults 500 per 600s — generous so a whole venue behind one NAT can
sign in at event start, tightenable for a public deployment), plus a hardcoded per-account
credential-stuffing backstop of 5 attempts / 5 min keyed on `sha256(email)`
(`handlers/auth.go:104`, `:150`). Pinned by
`handlers/register_ratelimit_integration_test.go :: TestRegisterRateLimitAllowsVenueBurst` and
`handlers/login_ratelimit_integration_test.go :: TestLoginRateLimitTightConfigStillFires`
(429 on the 4th over a burst of 3; per-IP buckets are independent). **On a Redis outage these limits
FAIL CLOSED** — login, register, submit, and token requests return `503 + Retry-After`, never
fail-open, so an outage cannot be used to strip the throttle (including the credential-stuffing
backstop) exactly when it is most wanted (`handlers.TestLimitFailsClosedWhenLimiterUnavailable`;
counted distinctly as `osctf_ratelimiter_unavailable_total`). See INVARIANTS.md ("Redis
unavailable…") for the full Redis-down posture: reads degrade to Postgres, credentials/mutations
refuse, and the freeze (§1) never falls through to a live board.

**WebSocket exhaustion — Mitigated.** Admission enforces, in order, a handshake rate, a global
connection cap, and a per-key cap, keyed on the authenticated user id where present else the client
IP (`ws/limits.go:95`), so one noisy IP cannot deny the scoreboard to everyone and the global cap is
clamped to the process fd budget. Pinned by
`handlers/ws_admission_integration_test.go :: TestWSAdmissionKeysOnUser` and the
`ws/limits_test.go` suite (global cap, per-key cap, handshake rate + forgiveness, zero-disables).

**Timing oracles — Mitigated by construction.** Password verification and flag comparison both use
`subtle.ConstantTimeCompare`, and unknown-email login is time-equalized with a dummy-hash burn so a
missing account costs the same as a wrong password (`auth/password.go:189`, `:205`;
`submissions/service.go:436`; API-token auth likewise, `auth/token.go`). The challenge-existence
timing symmetry *is* behaviourally tested (`TestEnumerationHiddenChallengeIndistinguishable`
asserts median timing), but the **constant-time property of the compare functions themselves is
enforced only by `crypto/subtle`, not by a timing test** — stated honestly as a construction
guarantee, not a measured one.

---

## 7. A competitor against an AI-challenge agent (planned)

*(Planned functionality — [`docs/ai-challenges.md`](docs/ai-challenges.md) is a design, not shipped.
Recorded here so the adversary is on the map before the code is; no control below is implemented or
test-pinned yet.)* An authenticated participant interacting with a live LLM agent instead of submitting
a flag, whose goal is to make the agent violate its stated policy. The blast radius is new.

**Inducing a forbidden tool call — bounded to inert tools; real tools out of scope.** The win
condition of a tool-abuse challenge is that a forbidden tool **was called** — recorded in the
transcript; the tool is **inert** and executes nothing. An author who wires a tool with real side
effects (a shell, a file write, an outbound request) is relying on a sandbox the platform **does not
provide** — the same class of limit as cross-team isolation on Docker Desktop (§2): a property of the
plugin trust model, not a bug OSCTF can fix. Run tool-abuse challenges with inert tools; a real-tool
challenge is unsupported.

**Driving up inference cost — turns capped, spend accepted.** Every turn is inference the operator pays
for, so cost is an economic/availability adversary. The host **can hard-cap turns** (each turn is a
host RPC; a per-team turn budget is enforceable, like the instance quota). The host **cannot cap token
spend** — it does not see the provider's tokens; a per-turn token count is **self-reported by the
plugin**, and a compromised or buggy plugin under-reporting it is an **unbounded-spend path with a real
bill attached**. **Accepted:** the enforceable mitigation is the **turn cap**, and the operator must
set a provider-side spend limit — the platform cannot be the backstop for spend it cannot observe.

**Extracting more than the canary — the host holds correctness; the agent holds only its own config.**
Extraction challenges are won by recovering a *canary*, checked **host-side** against the recorded
transcript — the plugin runs the agent, the host decides the verdict, so the agent never adjudicates
its own defeat. An agent must be scoped to its own challenge's `type_config` and corpus, with no access
to other challenges' secrets, provider credentials beyond what the plugin injects, or platform
internals. Keeping the agent's reachable context scoped is a **planned mitigation, not yet enforced by
a test** — it must not outlive the "no ai-challenge plugin exists yet" that makes it currently moot.

---

## Out of scope (by design)

Mirrors [`SECURITY.md`](SECURITY.md) — do not report these:

- **Challenge containers being intentionally vulnerable or running as root.** That is the product
  ([runtime doc](docs/v0.1/08-challenge-runtime.md)).
- **Cross-team isolation not holding on Docker Desktop.** Documented (§2, issue
  [#2](https://github.com/osctf/platform/issues/2)); run events on Linux.
- **Anything requiring the admin credentials or host access the operator already controls.** The
  admin is trusted; the Docker socket mount is root-equivalent by design.
- **Missing hardening on a deployment run against [`docs/v0.1/10-deployment.md`](docs/v0.1/10-deployment.md)**
  (no reverse proxy, rate limits disabled, default admin password unchanged).

## Known test gaps (accepted and tracked)

Honesty about where the map is thinner than the territory — these controls exist in code but are not
yet behaviourally pinned:

- Hidden **user** profile returning 404 to non-admins (§1) — code-enforced, **no test probes it**.
- Direct assertion that `RateLimitRejections` / `WSRejections` counters increment, and a token
  rate-limit 429 (§6, §4) — **no test**; the 429 *behaviour* is pinned by the register/login tests.
- The constant-time property of the password/flag compares (§6) — **construction-only** (`crypto/subtle`).
- Auth-plugin return-path validation (§3) — **documented, not yet implemented, not tested.** Safe
  only while the auth registrar arm returns `nil`; a hard precondition of milestone M3.

## Keeping this honest

The tests named here are the source of truth; this document describes them. When you change
something a section above relies on:

1. Update the code and its pinning test together (a bug fix ships with a failing test first — the
   testing contract in [`AGENTS.md`](AGENTS.md)).
2. Update the matching section here, and move an item between **Mitigated / Accepted / Out of scope**
   if its status actually changed.

A threat model that has silently drifted from the code is worse than none: it is confidently wrong,
and it is the first thing a reviewer trusts.
