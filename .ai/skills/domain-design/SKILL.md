---
name: domain-design
description: The fractal top-down design method — event-storm discovery, context positioning, aggregates and invariants, aspect mounting, artifacts — repeated at every level (L0 product map, L1 context modules, L2 hexagonal components) before any code starts.
---

# Domain design

Every feature — and every level of zoom — runs the same top-down
method before code. Methodology is upstream-first: the patterns come
from Khononov (Learning Domain-Driven Design), Evans, Vernon
(aggregate heuristics), Brandolini (event storming), and Brown (C4);
this skill encodes how they combine with this repository's artifacts.

## When

- A change crosses a context boundary (design-first trigger,
  AGENTS.md) → run at L1 for the affected context.
- A new context or subdomain appears → amend the L0 map first.
- A module grows its first hexagonal component → run at L2.

## The process

1. **Discover** — event-storm the feature: actors, commands, events,
   policies. Harvest the ubiquitous-language terms; glossary entries
   are added in the same PR.
2. **Position** — place the new pieces on the context map. Choose the
   relationship pattern deliberately:
   - **Anti-Corruption Layer** — integrating with foreign models we
     don't control (SCM providers, IM platforms, document systems).
   - **Open Host Service + Published Language** — we are the platform
     others plug into (session protocol, harness adapters).
   - **Customer–supplier** — we consume another context's stream
     (records, metering).
   - **Conformist** — we accept an authority as-is (registry
     admission decisions).
   - **Separate ways** — no shared model, minimal coupling.
3. **Model** — define aggregates and their invariants. An aggregate
   owns one consistency boundary; invariants that must never be
   optional are written down as invariants (AD-27 §2 sense), not
   conventions.
4. **Mount aspects** — walk the aspect checklist and say where each
   one executes for this feature: PEP, identity propagation, audit
   emission, metering emission, ACL trimming, event transport,
   revisions/idempotency. An aspect silently omitted is a finding.
5. **Produce artifacts** — context-map/glossary updates, C4 container
   and component diagrams where layout is non-obvious, an AD
   (`adr_manager.py`) if a decision was made, boundary dependency
   rules where two modules now depend.
6. **Review gate** — the design lands as a `kind/design` issue with a
   reviewed PR; implementation issues are filed only after. Code
   before the design review is out of process.

## Done criteria

- Map and glossary updated in the same PR as the code that assumes
  them (co-change rule).
- Every aspect in the checklist has a stated mounting point or an
  explicit "not applicable" with the reason.
- Aggregates have their invariants written in the design doc, not
  only implied by code.
- Boundary dependencies are linted in CI once code exists (L2).

## Levels

- **L0** — product context map (`docs/architecture/context-map.md`).
- **L1** — module design inside one context (gateway first).
- **L2** — hexagonal components inside a module (ports/adapters,
  dependency lint from day one).

The method does not change between levels; only the vocabulary's
radius changes.
