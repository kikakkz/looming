---
updated: 2026-09-25
type: decisions
---

# Decisions

Accepted architecture decisions. Append-only; changes only through a
reviewed PR (see `.ai/AGENTS.md`). Each entry: date, status, essence.

## AD-18 — 2026-09-25 — accepted

Governance formats follow mature upstream practice instead of invention:
kind/area issue label axes modeled on the Kubernetes SIG convention,
synced from `.github/labels.yml` by a standard labeler action; Conventional
Commits per the community specification. Any new language, framework, or
design must land with its full supporting kit (skills, CI, convention
updates) in the same PR. Issue-driven development is enforced from the
first post-push change; the bootstrap commit remains the single exception.

## AD-17 — 2026-09-25 — accepted

Repo home is github.com/kikakkz/looming, public, English docs. Bots:
CodeRabbit enabled early (free for public repos); Kimi Code Action when
needed. agentgateway (AAIF) gateway research scheduled after initialize.
Event-stream schema deferred to its own post-bootstrap issue.

## AD-16 — 2026-09-25 — accepted

Engineering standards: AGENTS.md as the vendor-neutral contract, nested
per-directory; AI attribution via `Assisted-by:` / `Generated-by:`
trailers; `Co-Authored-By:` banned for AI; only humans sign DCO;
Apache-2.0 + DCO; Conventional Commits enforced on PR titles.

## AD-15 — 2026-09-25 — accepted

Runtime selection funnel: 1–2 day smoke of opencode + OpenHands against a
mock gateway → full spike (1–2 weeks) on the winner (headless
issue→plan→diff, 10-run stability, attribution header injection). OpenHands
fallback triggers on hard-dimension walls; goose fallback triggers on
governance events. One active investment at a time.

## AD-14 — 2026-09-25 — accepted

Architecture principle: batteries included, everything swappable.
Pluggable slots behind small versioned interfaces; control plane
(blueprint orchestrator, event log) deliberately NOT pluggable — it is the
product IP.

## AD-13 — 2026-09-25 — accepted

Decision-engine slot uses System One decision models (Jev hosted, closed /
Laya open-weight, self-hosted default) for semantic risk interception.
Credential rejection rules are defined by channel × destination;
ambiguous verdicts escalate.

## AD-12 — 2026-09-25 — accepted

Agent runtime is a pluggable provider system with capability levels
L0–L3 (L3 = model calls routed via the Looming gateway). Default component
chosen by bake-off (AD-15); users may bring their own agents.

## AD-11 — 2026-09-25 — accepted

Unified registry covers MCP servers, skills, and tools. Default substrate
ToolHive (Go, Apache-2.0, K8s operator); official registry OpenAPI as the
contract; admission scanning (snyk/agent-scan); registration unit =
immutable version + hash; MCP tool annotations declare permissions.

## AD-10 — 2026-09-25 — accepted

Skills follow the SKILL.md open standard. This repo references external
skills by sha-pinned lockfile, never vendors (public GitHub). Vendoring is
a Looming platform mode for air-gapped/private managed repos. Superpowers
occupies the methodology slot at bootstrap; AGENTS.md wins process
conflicts.

## AD-9 — 2026-09-25 — accepted

Memory governance: dual-track writes — episodic records auto-written,
procedural knowledge only via reviewed PR (prompt-injection defense).
Org memory service models org→project→user scopes, Postgres-first. Repo
memory bank = markdown files in `.ai/memory/`.

## AD-8 — 2026-09-25 — accepted

CI and judge are different dimensions: CI = continuously updated
deterministic fail-case checks (blocking); judge = broader review engine
discovering new objective failures and taste findings (advisory).

## AD-7 — 2026-09-25 — accepted

Verifier-first quality gate. CI guards known failures, generalized via a
shared check library. Judge discovers new objective failures → codified
into CI; taste findings → org memory/skills/tools. Judge output is
advisory, never a merge blocker; high-risk actions keep human gates.

## AD-6 — 2026-09-25 — accepted

All four agent roles get disposable sandboxes; the harness runs OUTSIDE
the sandbox; per-run minted short-lived repo-scoped credentials. Sandbox
policy: no production credentials, no network egress except allowlist
(SCM + package registries), no production data. Model/gateway credentials
never enter the sandbox. Approval moves to the MR boundary with risk
tiers.

## AD-5 — 2026-09-25 — accepted

The SCM issue is the task surface and collaboration plane (plan posted as
issue comment; milestone-level write-back due to API rate limits). The
platform event log is record-only — never the work queue.

## AD-4 — 2026-09-25 — accepted

SCM integration via adapters (repo/issue/MR/webhook/checks). One repo =
one SCM flow. Repo CI stays native (GitLab CI / GitHub Actions); the
platform pipeline is self-built on K8s jobs; CI status is read back
uniformly.

## AD-3 — 2026-09-25 — accepted

Model gateway in Go: stateless OpenAI-compatible forwarding; auth, quota,
audit; append-only interaction log as sidecar (flight recorder); plugin
chain with pre-hooks (risk interception) and post-hooks (analysis); model
governance in the control plane; no smart routing in v1.

## AD-2 — 2026-09-25 — accepted

Distribution is a single self-contained bundle artifact: all components,
one version number, one download; zero external SaaS dependency; cluster
topology is config produced by a first-boot wizard; expansion = adding
nodes to that config.

## AD-1 — 2026-09-25 — accepted

Monorepo with hybrid versioning: components carry independent semver
image tags; a bundle release is a git tag plus a manifest pinning every
component's exact version; only changed components are published.
