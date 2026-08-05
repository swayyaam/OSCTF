# Security Policy

OSCTF is software other people self-host, so vulnerabilities are handled privately and
disclosed in coordination — please do **not** open a public issue for one.

## Reporting a vulnerability

- **Preferred:** GitHub **private vulnerability reporting** — the *"Report a vulnerability"*
  button on the repository's **Security** tab
  ([open one here](https://github.com/swayam-mishra/OSCTF/security/advisories/new)). It goes
  straight to the maintainer and stays private.
- **Fallback:** if you can't use GitHub, email **swayammishra1504@gmail.com** with
  `OSCTF security` in the subject.

### What to include

- The **version** (release tag or commit).
- The **deployment shape**: Docker Compose; Docker Desktop (macOS/Windows) vs a Linux host;
  behind a reverse proxy or not.
- **Reproduction steps.**
- The **impact** — what an attacker actually gains.

## Response expectations

This is a solo-maintained project, so the honest version: **best-effort acknowledgement
within about a week**, and I'll work with you on a fix and **coordinate disclosure timing**
before anything is made public. There is no paid bounty and no fixed fix-by SLA — but past
security work (below) shows these get acted on.

## Supported versions

Only the **latest release** receives security fixes at this stage.

| Version | Supported |
|---|---|
| Latest release ([Releases](https://github.com/swayam-mishra/OSCTF/releases)) | Yes |
| Anything older | No — upgrade to the latest |

## Scope

OSCTF runs deliberately vulnerable challenge containers on purpose, so "scope" matters more
than usual — please read this before reporting.

**In scope** (please report):

- **Privilege escalation between participants** — a participant gaining another participant's
  or an admin's capabilities.
- **Flag disclosure** — a flag reaching any surface a participant shouldn't see it on
  (API, WebSocket, logs, metrics, audit).
- **Scoreboard or freeze integrity** — manipulating standings, or reading data the freeze is
  meant to hide.
- **Container escape** — breaking out of a challenge container to the host, or to another
  team's containers or network.
- **Authentication or authorization bypass.**

**Out of scope** (by design — please don't report):

- **Challenge containers being intentionally vulnerable, or running as root.** That is the
  entire point of a CTF challenge; the runtime is designed to host hostile code
  ([runtime doc](docs/v0.1/08-challenge-runtime.md)).
- **Cross-team network isolation not holding on Docker Desktop (macOS/Windows).** A
  documented limitation — run events on Linux
  ([issue #2](https://github.com/swayam-mishra/OSCTF/issues/2),
  [runtime doc](docs/v0.2/03-runtime.md)).
- **Anything requiring admin credentials or host access the operator already controls.** The
  admin is trusted, and mounting the host Docker socket is root-equivalent by design — run on
  a dedicated host, per the deployment doc.
- **Missing hardening on a deployment misconfigured against
  [`docs/v0.1/10-deployment.md`](docs/v0.1/10-deployment.md)** (e.g. no reverse proxy, rate
  limits disabled, default admin password left unchanged).

If you're unsure whether something is in scope, report it privately and ask — a quick
question is fine.

## Track record

Security fixes are documented in the [CHANGELOG](CHANGELOG.md): **v0.2.1** was a dedicated
security release, and **v0.2.2** hardened the concurrency surface. Reports are acted on.
