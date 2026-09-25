---
number: 21
title: "Migrate decision records to one-file-per-decision ADR layout"
date: "2026-09-25"
status: "accepted"
supersedes: []
adopted-at: "2026-09-25"
---

# AD-21 — Migrate decision records to one-file-per-decision ADR layout

## Context

`decisions.md` was a single growing file (AD-1 … AD-20). Mature-practice
survey (Python PEP, Kubernetes KEP, Rust RFC, MADR, adr-tools) converges:
one file per decision, status flow instead of edits, bidirectional
supersede links, generated index. Evaluated adr-tools for direct adoption;
its plain-text status format conflicts with this repo's frontmatter
conventions and the adopted dual-timestamp refinement, so its mechanics
are reimplemented in a minimal stdlib tool (`adr_manager.py`).

## Decision

Architecture decisions live one per file at
`.ai/memory/decisions/NNNN-slug.md` with frontmatter: number, title,
date, status (accepted | superseded | deprecated), supersedes,
superseded-by, adopted-at, superseded-at. Superseding is atomic and
bidirectional (KEP `replaces:` ↔ `superseded-by` semantics), performed by
`adr_manager.py new --supersedes`. The index at `decisions/README.md` is
generated, split Active vs Superseded. `decisions.md` remains only as a
stable pointer. `make ci-gate` runs `adr_manager.py check`: numbering,
reciprocal links, superseded-metadata completeness, index freshness.

## Consequences

Decision files are immutable except status flips; all changes, including
supersedes, land through reviewed PRs (dual-track rule unchanged).
AD-20 ↔ AD-19 is the first machine-checked bidirectional pair. Issue #5.
