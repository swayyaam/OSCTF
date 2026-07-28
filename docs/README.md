# OSCTF Documentation

This directory holds the vision and the per-version build specifications for the OSCTF platform.

- **[`project-desc.md`](project-desc.md)** — the cross-version vision: mission, principles, architecture direction, and the full phase roadmap (v0.1 → v1.0+). Read this for *why* and *where it's going*.
- **`v0.1/`, `v0.2/`, …** — each directory is the self-contained build spec for one version. Point a coding agent at exactly one version directory; it should never need to look outside it (except at `project-desc.md`) to build that version.

## Versions

| Version | Theme | Status | Spec |
|---|---|---|---|
| **v0.1** | MVP — one person hosts a real CTF for ~100 players on one server | ✅ **Built & shipped (`v0.1.0`)** | [`v0.1/`](v0.1/README.md) |
| **v0.2** | Dynamic per-team instances + scheduler | ✅ **Spec complete, ready to build** | [`v0.2/`](v0.2/README.md) |
| v0.3 | Extensibility: plugin system, CLI, stable API v1, MCP server | 🕓 Planned (stub) | [`v0.3/`](v0.3/README.md) |
| v0.4 | Kubernetes runtime + operator, horizontal scale | 🕓 Planned (stub) | [`v0.4/`](v0.4/README.md) |
| v0.5 | Multi-event / multi-tenancy | 🕓 Planned (stub) | [`v0.5/`](v0.5/README.md) |
| v1.0 | Stability promise + ecosystem (marketplace, SDKs, themes, AI) | 🕓 Planned (stub) | [`v1.0/`](v1.0/README.md) |

The theme/scope/exit-criterion for every version comes from the roadmap in [`project-desc.md`](project-desc.md#L184). The stub directories restate their version's slice of that roadmap; they are **not** yet build-ready — writing a version's detailed spec (the way `v0.1/` is written) is the first task of starting that version.

## Conventions across versions

- One directory per version. A version's spec is frozen when that version ships; the next version gets its own directory rather than editing a shipped one.
- Each version directory mirrors the `v0.1/` structure (an internal `README.md` index + numbered topic docs) once it is written out in full.
- `project-desc.md` is the only document that spans versions and is updated as the roadmap evolves.

## Where to start

- **Building the MVP now?** Go to [`v0.1/README.md`](v0.1/README.md) and follow its kickoff prompt.
- **Planning a later version?** Open its stub directory, read the scope it inherits from the roadmap, then expand it into full topic docs following the v0.1 template.
