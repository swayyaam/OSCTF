# v0.4 — Kubernetes Runtime (Scale, part 1)

> Status: **planned stub — not yet build-ready.** Scope below is inherited from the roadmap in [`../project-desc.md`](../project-desc.md#L184). Do not start building from this file; first expand it into full topic docs following the [`../v0.1/`](../v0.1/README.md) template.

## Theme

**The same code, from laptop to cluster.** The first of the two Phase 4 (Scale) releases; multi-event is [`../v0.5/`](../v0.5/README.md).

## Scope (from roadmap)

- A **Kubernetes backend** for the `ChallengeRuntime` interface — challenges packaged once in v0.1 run unchanged on K8s.
- The **operator** (challenge/instance CRDs) reconciling desired state.
- **Helm charts** for deploying the platform itself on a cluster.
- **Horizontal scaling** of the API behind a load balancer (the v0.1 monolith is built stateless-per-request for exactly this — sessions and scoreboard cache already live in Redis, not process memory).
- **Externalized state**: support for managed Postgres/Redis rather than compose-bundled.

## Exit criterion

Combined with v0.5: one deployment serves a **1,000+ participant event on Kubernetes** while a second, smaller event runs concurrently — **with no code differences** from the compose-based single-server path.

## Builds on v0.1

- [`../v0.1/01-architecture.md`](../v0.1/01-architecture.md) — the modular monolith keeps service boundaries in code "so the split is possible without a rewrite"; the "cloud native (later)" rule warns against anything that precludes K8s (no host-path assumptions, no single-process locks for correctness). Audit against those rules before scaling out.
- [`../v0.1/08-challenge-runtime.md`](../v0.1/08-challenge-runtime.md) — `DockerRuntime` is one implementation of the runtime interface; this version adds `KubernetesRuntime` alongside it, selected by deployment config.
- [`../v0.1/10-deployment.md`](../v0.1/10-deployment.md) — compose is the v0.1 golden path; Helm becomes the cluster path here without replacing compose.

## To make this build-ready

Write the numbered topic docs: the K8s runtime design, the operator/CRD schemas, the Helm chart structure and values, the horizontal-scaling and shared-state model (leader election for tickers, WS fan-out across replicas), the externalized-state configuration, and a milestone plan with acceptance checks — same depth as `../v0.1/`.
