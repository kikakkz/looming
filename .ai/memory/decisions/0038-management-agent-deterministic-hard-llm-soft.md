---
number: 38
title: "management agent: deterministic-hard, LLM-soft placement derivation"
date: "2026-10-09"
updated: "2026-10-09"
status: "accepted"
supersedes: []
adopted-at: "2026-10-09"
---

# AD-38 — management agent: deterministic-hard, LLM-soft placement derivation

## Context

The bootstrap scenario (#108 discussion) needs placement derivation: when
the admin declares machines, something must derive which components can run
where, and a new machine's join must answer what it can host. The
maintainer's direction fixed the positioning first: **looming is itself an
agent, not a traditional tool with an LLM bolted on** — the default
interaction paradigm is understand-environment → propose → human confirms →
execute; command forms exist but are explicit modes. This is the context
map's Orchestration context (#4) instantiated for the first time, before
blueprints/runs exist — looming dogfoods its own orchestration posture on
its own bootstrap problem.

Two derivation-quality risks needed explicit allocation. Hard requirements
(resource floors, network egress, port conflicts, arch match) must hold
with certainty — a probabilistic pass here puts components where they
cannot run. Soft trade-offs (cloud vs LAN placement, latency vs egress
quality, existing load, fault-domain spread) are exactly the class of
knowledge-work LLMs are good at, and they carry no correctness guarantee
anyway because the human decides. Mature precedents split the same way:
deterministic schedulers (k8s kube-scheduler: predicates hard-filter,
priorities soft-rank) vs LLM-in-the-loop ops copilots that propose and
never apply unreviewed.

## Decision

1. **Reasoning split: deterministic hard constraints, LLM soft
   reasoning.** Hard rules are domain logic — pure functions, fail-closed,
   never given to the model. Soft reasoning (ranking feasible solutions,
   explaining trade-offs, inferring implicit constraints from operator
   free text) is LLM work under the standing rule **"LLM proposes, human
   decides"** — the artifact the model influences is the topology.yaml
   `placements:` section, and it reaches disk only after human confirm.
2. **The derivation product is declared topology — zero new interfaces.**
   Advise renders candidate placements into the existing topology.yaml
   `placements:` section and the existing Declare surface
   (`DeclareInput{Hosts, Placements, Access}`) converges it. No parallel
   task plane, no advisor-owned store of desired state.
3. **Component knowledge is data, not code: the ComponentProfile.**
   Per-component requirement profiles (hard floors + soft preferences)
   live in `platform/go/advisor/profiles/components.yaml`, schema-driven —
   the evaluator consumes only `hard:`, `soft:` serializes into the LLM
   context, and adding a component is adding a profile, never a code or
   prompt change. Profiles are review-gated knowledge (PR + CI schema
   validation), same class as AGENTS.md/.ai assets; when the Registry
   context (#8) lands, profiles migrate there as entries and the loader
   swaps to retrieval — schema unchanged. Kind-level defaults with
   per-component override (e.g. `kind: model-gateway` defaults
   `needs_egress: true`).
4. **Machine facts are a first-class derivation input.**
   `Host.capabilities` extends the topology Host aggregate: hardware
   (cpu/mem/disk/arch) + network (zone: cloud|lan, egress, optional
   latencies_ms). Slice 1.1: admin declares facts by hand in
   topology.yaml; slice 1.3: join-time capability discovery automates the
   collection (egress probe + hardware read — ansible facts / k8s node
   labels precedent). Fail-closed throughout: a missing fact the
   evaluator needs fails that machine for that component with a named
   gap — never a default pass.
5. **Network connectivity is a first-class dimension.**
   Egress-needing components (SCM adapters, model gateways, connectors)
   hard-require egress-capable machines; cross-domain trade-offs (cloud
   egress quality vs LAN latency to identityd) are LLM soft reasoning.
6. **LLM output never bypasses the evaluator.** Every model-proposed
   placement is re-run through the deterministic evaluator before
   display; a violation rejects that candidate and regenerates (max 3),
   then degrades to the deterministic table mode — the hard layer is the
   backstop, not the model's own judgment.
7. **Module placement: domain logic in platform, interaction in CLI.**
   `platform/go/advisor/` holds the profile schema/loader, the pure
   evaluator, and (slice 1.2) the LLM client interface;
   `cli/internal/advisor/` holds the `looming topology advise`
   interaction (diff preview, confirm/edit/regenerate/abort, session
   log). Dependency direction: cli → platform/go/advisor → platform/go/
   config; never reversed.
8. **Session log is append-only local record.**
   Each advise session records facts snapshot, profile-version hash, raw
   LLM output with reasoning, and the human decision at
   `~/.looming/advisor/sessions/<ts>.jsonl` — the sediment the later
   event backbone will absorb; this slice builds no event bus.
9. **`--yes` non-interactive pass-through: deferred, not rejected.**
   Deferred out of slice 1 (no automation consumer exists yet); the
   maintainer judged it non-blocking. If it returns, the shape is the
   mature-practice one (Terraform `-auto-approve` et al.): allowed only
   with all hard constraints passing, confidence never blocks, full
   session record marked `auto=yes`.
10. **Sliced delivery.** 1.1 deterministic-first (profiles + facts fields
    + evaluator + `advise` table mode — valuable and testable without any
    LLM), 1.2 LLM reasoning (genesis model channel per #143 lands here,
    closing #143), 1.3 join-time facts discovery. Orchestration loop runs
    embedded in the CLI process (sandbox-zero-dependency two-layer split
    per #144) — sandboxed execution arrives with agentruntime later.

## Consequences

- The design lands as `docs/architecture/advisor-l1.md`; implementation
  slices 1.1–1.3 are tracked in #144, genesis (#143) closes with 1.2.
- `Host.capabilities` is a schema addition to the topology config/host
  aggregates — Declare's interface and invariants do not change, phase-1
  topology remains valid with capabilities absent (evaluator treats
  missing as gap, not pass).
- New components acquire derivation ability by adding a profile —
  scheduling knowledge grows by review, matching how AGENTS.md/.ai
  knowledge grows.
- The management agent's scope grows along the maintainer's stated path
  (placement → capacity management → registry admission assist → fault
  response) by widening the advisor module, not by new contexts —
  advisor-l1 is Orchestration's document carrier until blueprints/runs
  land.
- No `--yes` in slice 1: advise is interactive-only; CI/automation use
  cases wait for a real consumer to design against.
