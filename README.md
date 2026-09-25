# Loom

Loom is an enterprise agent-engineering platform delivered as a single,
self-contained bundle: an OpenAI-compatible model gateway with governance
(auth, quota, audit, risk interception), pluggable agent runtimes, an
organization-wide memory and tools/MCP/skills registry, isolated sandbox
execution, and issue-driven development pipelines across GitHub and
self-hosted GitLab CE.

Design principles: **batteries included, everything swappable**. Every major
component lives behind a small versioned interface; the bundle ships the
combination we consider best, and users may replace any slot with their own
or an open-source alternative.

## Status

Bootstrap. The repository skeleton, agent-facing conventions, and CI gates
are in place; components land issue by issue.

## Orientation

- [AGENTS.md](AGENTS.md) — operating contract for humans and AI agents.
- [CONTRIBUTING.md](.github/CONTRIBUTING.md) — DCO, AI contribution policy, dev setup.
- [.ai/memory/decisions.md](.ai/memory/decisions.md) — accepted architecture decisions (AD-1…).
- [.ai/AGENTS.md](.ai/AGENTS.md) — rules for the agent asset directory.

## License

[Apache-2.0](LICENSE). Contributions require DCO sign-off by a human.
