# v1.0 — Stability & Ecosystem

> Status: **planned stub — not yet build-ready.** Scope below is inherited from the roadmap in [`../project-desc.md`](../project-desc.md#L184). Do not start building from this file; first expand it into full topic docs following the [`../v0.1/`](../v0.1/README.md) template.

## Theme

**Other people's work runs on the platform.** v1.0 is a **stability promise, not a feature dump.**

## Scope (from roadmap)

**v1.0 itself:**
- API v1 + plugin API **frozen under semver** — no breaking changes after this line.
- **Migration guides from CTFd** (the CTFd import-path decision from the vision doc's open questions lands here at the latest).
- Production hardening, backup/restore, upgrade-path guarantees.

**Post-1.0, driven by RFCs** (each likely its own minor version):
- **Marketplace** for challenges/plugins/themes.
- **Plugin SDK** and **client SDKs** (JS/Python).
- **Theme system**.
- **GitOps challenge pipeline**: a challenge is a Git repo — CI validates, publishes, platform deploys.
- **AI features**: hints, difficulty estimation, cheat detection, solution verification.

## Exit criterion

The **marketplace has more community-authored content than first-party content**, and **a breaking change hasn't shipped since v1.0.**

## Builds on v0.1

- [`../v0.1/00-overview.md`](../v0.1/00-overview.md) — the license open question (shipped as a `TBD` placeholder in v0.1) **must be resolved before any public 1.0**.
- [`../v0.1/05-api.md`](../v0.1/05-api.md) — the `/api/v0` → `/api/v1` promotion happens in v0.3; v1.0 is where v1 is *frozen* under a semver contract.
- The whole v0.1 spec is written "so deferral never becomes a rewrite" — v1.0 is where that discipline is cashed in as a durable stability guarantee.

## To make this build-ready

Because post-1.0 items are explicitly **RFC-driven**, this directory should first hold the **stability/versioning policy** and the **1.0 release checklist**; each ecosystem feature (marketplace, SDKs, themes, GitOps, AI) then gets its own RFC and, once accepted, its own detailed spec — same depth as `../v0.1/`.
