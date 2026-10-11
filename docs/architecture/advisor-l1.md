# Advisor L1 — module design

Derived in #144 (management agent, placement-derivation slice 1).
Authority: AD-38 (the deterministic-hard/LLM-soft split and this
design's decisions), AD-36 (topology plane the product lands in),
AD-37 (CLI two faces — advise is an admin-face command). Vocabulary:
[glossary](glossary.md).

## 1. Scope

Placement derivation: given declared machine facts and component
requirement profiles, derive which components can run on which
machines (deterministic feasibility), and — with LLM assistance from
slice 1.2 — rank feasible assignments with reasons and risks. The
product is always the topology.yaml `placements:` section; human
confirm is the gate before anything reaches disk.

In scope: profile schema + loader, pure evaluator, machine-facts
fields, `looming topology advise` interaction, append-only session
log. Out of scope (named triggers, §7): capacity management,
re-balancing, auto-apply. Join-time discovery (1.3, §8) lands in this
design's slices: facts now arrive with the join payload, and the §2
"hand-declared" line describes the file side only — `topology facts
pull` merges observation in.

**Positioning.** Looming is itself an agent (AD-38): the default
posture is understand-environment → propose → human confirms →
execute; commands are explicit modes. Advise is that paradigm's first
surface and the Orchestration context's first instantiation — the
loop runs embedded in the CLI process (the sandbox-zero-dependency
two-layer split from #144); sandboxed execution arrives with
agentruntime later.

## 2. Model

```
ComponentProfile (knowledge; review-gated, shipped as data)
  ├─ name    — must match the render allowlist vocabulary; unknown
  │            names fail closed at load time
  ├─ kind    — component family: kind-level defaults, per-profile override
  ├─ hard:   — evaluator input ONLY; the model never sees ambiguity here
  │   ├─ min_cpu_cores / min_memory_mb / min_disk_gb
  │   ├─ arch: [x86_64, arm64]
  │   ├─ needs_egress: bool
  │   └─ ports: [...]      — claimed port names (conflict rule input)
  └─ soft:   — serialized verbatim into the model context (slice 1.2)
      ├─ preferred_zone: cloud | lan
      └─ spread: component  — anti-affinity preference across instances

Host.capabilities (facts; hand-declared in 1.1, discovered in 1.3)
  ├─ hardware: { cpu_cores, memory_mb, disk_gb, arch }
  └─ network:  { zone: cloud | lan, egress: bool, latencies_ms? }

Feasibility = Evaluator(facts, profiles) → per (component, host) pair:
  FEASIBLE | INFEASIBLE(violated hard rules, missing facts)
```

**Fail-closed everywhere.** A profile failing schema validation, a
component name outside the render allowlist, or a fact a hard rule
needs but the host does not declare — each fails closed with the gap
named, never a default pass. A host with no `capabilities` block
makes every component infeasible there, and the table says why.

**Knowledge growth by review.** New components acquire derivation
ability by adding a profile through PR + CI (schema validation +
allowlist cross-check) — the same review-gated class as AGENTS.md and
.ai assets. When the Registry context (#8) lands, profiles migrate
there as entries and the loader swaps to retrieval; the schema does
not change.

## 3. Journey — `looming topology advise`

1. **Load.** Read the topology file (hosts + declared capabilities)
   and the profiles; record the profile-set hash.
2. **Evaluate.** Run the evaluator → the full (component × host)
   feasibility matrix. Slice 1.1 renders it as the table (§4) and
   stops here.
3. **Reason (1.2).** Genesis channel (#143) call: feasible set + soft
   preferences + operator free text ("node3 runs a database, avoid
   it") → ranked candidate placements, each with reasons and risks.
   Free-text preferences affect the current session only — a recurring
   preference becomes a profile change through review, never
   sedimented silently.
4. **Re-validate.** Every model-proposed placement re-runs through
   the evaluator; a violation rejects the candidate and regenerates
   (max 3), then degrades to table mode. The hard layer is the
   backstop, not the model's own judgment.
5. **Preview.** Unified diff of the topology file's `placements:`
   section against current disk state.
6. **Decide.** confirm (write the file — never auto-apply; the next
   step is the operator's `looming apply`) / edit (open an editor) /
   regenerate (add a preference, re-reason) / abort.
7. **Record.** Append the session record (§6) — facts snapshot,
   profile hash, raw model output with reasoning, human decision.

## 4. Command surface

```
looming topology advise [--file PATH] [--reason] [--preference TEXT]
                        # default /etc/looming/topology.yaml
```

Table mode (slice 1.1) renders one row per (component, host) pair —
the complete matrix, no pagination: the pair count is profiles ×
hosts, bounded by definition, so a "top-K + --all" split is
unnecessary at this granularity (C2 of the design discussion resolved
this way). Ordering: components in allowlist order; hosts by memory
headroom descending, CPU headroom as tiebreak (bin-packing
convention). FEASIBLE rows show headroom; INFEASIBLE rows name the
violated hard rules and any missing facts.

Reason mode (slice 1.2, `--reason`) runs the §3 journey steps 3–7:
`--preference` carries the operator's free text (session-only;
prompted when omitted on a terminal); regeneration folds additions
forward. The proposal names (component, host) pairs; the CLI owns the
mechanical splice — moved components keep their published ports and
operator wiring, new components get the contract listen port as the
lowest free port ≥ 1024 on the target host, and the spliced document
must load through the real config validator before it is ever shown.

## 5. Evaluator contract

Pure functions in `platform/go/advisor`, one rule per hard
constraint, each independently unit-tested across the full violation
matrix:

- **resource floors** — cpu/mem/disk below the profile minimum → infeasible
- **egress** — profile `needs_egress: true` on a host with
  `egress: false` → infeasible
- **architecture** — host arch not in the profile arch list → infeasible
- **ports** — claimed port names must exist in the component's render
  Contract; feasibility reports the claimed set, while the existing
  Declare surface keeps authority over conflict arbitration — the
  evaluator never re-implements it
- **fact completeness** — any hard-rule input absent → infeasible +
  `missing: [facts...]`

Input: declared hosts with capabilities, the profile set. Output:
typed verdicts, never strings the caller must parse. No I/O, no model
access, no global state — the same purity rule as the rest of
platform/go. Module seam: `cli/internal/advisor` owns the interaction
and depends on `platform/go/advisor`; the dependency never reverses.

## 6. Aspects

- **Model channel (1.2-A landed).** Genesis three-stage lifecycle
  (#143, closed by slice 1.2-A): stage 1 calls the genesis endpoint
  directly — an empty cluster is fine; apply's converge success is the
  gateway-ready signal; stage 2 the credential rides the gateway-front
  placement's declared env file as `GATEWAY_UPSTREAM(_AUTH)` — the
  phase-1 secret channel, the same upstream-auth surface slice D
  (#114) ships — while the advisor's own calls switch to the gateway
  front with a service-identity LoomingKey (identity's kind=service
  principal, self-issued key, stored in the client-side secret
  channel); then the local genesis copy is erased — no emergency
  fallback, recovery = the admin re-provides. The `LLMClient` seam
  (OpenAI-compatible chat completions — the converged shape both the
  gateway and third-party endpoints serve) lives in
  platform/go/advisor; the CLI owns the lifecycle state at
  `~/.looming/advisor/genesis.json` (the 0600 class, secrets never in
  it) and the `looming genesis set|sync|status` surface. Slice 1.1
  shipped no model call; slice 1.2-B puts the seam to work.
- **Session record.** `~/.looming/advisor/sessions/<ts>.jsonl`,
  append-only: facts snapshot, profile-set hash, raw model output with
  reasoning, decision, rendered diff. This is the sediment the later
  event backbone (Records context) absorbs — this slice builds no
  event bus.
- **Security.** Profiles and facts are operator-visible and
  secret-free by construction; the session log may quote operator
  free text — the advisor directory is mode 0700 under `~/.looming`,
  the same discipline as credentials.yaml (AD-37 §5).
- **Test pyramid.** Evaluator: full violation-matrix unit tests.
  Table mode: golden-render tests. Journey: diff/confirm/abort flow
  against a fixture topology file in integration tests. No model call
  is testable in CI — 1.2 ships the model boundary behind an
  interface with a recorded-response fake.

## 7. Deferred (named triggers)

- **`--yes` non-interactive pass-through** — deferred per maintainer
  (non-blocking, not rejected); the shape is predetermined (AD-38
  rule 9) when a real automation consumer exists.
- **Plan-level composition** (whole-cluster assignment search beyond
  pair feasibility) — arrives with model ranking in 1.2; no
  deterministic plan solver is built before a consumer asks.
- **Profile migration to Registry (#8)** — when the Registry context lands.

## 8. Implementation slices (tracked in #144)

- **1.1 deterministic-first** — profile schema + loader,
  `Host.capabilities` fields, evaluator, `advise` table mode.
  Valuable and fully testable without any model. Landed as slice 1.1
  (#147): `looming topology advise` renders the deterministic feasibility
  table; slices 1.2/1.3 remain.
- **1.2 model reasoning** — landed in two parts. 1.2-A (#148) the
  genesis channel: the LLMClient seam, the lifecycle state machine, the
  service identity, the erasure — closes #143. 1.2-B the reasoned
  interaction: `looming topology advise --reason` proposes ranked placements
  with reasons and risks over the evaluator's feasible set + the
  profiles' soft sections + the operator's session preference; the
  guardrail re-runs every placement through the evaluator (max 3
  attempts, then degrades to the table mode); the placements splice
  previews as a unified diff and lands on disk only on confirm —
  edit (revalidated through the real config validator), regenerate
  (preference folds forward), or abort.
- **1.3 discovery** — landed as slice 1.3 (#144): join-time facts
  collection replaces hand declaration. The joining host's CLI
  collects its own machine facts — hardware from /proc + statfs
  (logical cores, memory, root-filesystem availability, arch), egress
  from a TCP probe of a well-known anycast endpoint, each source
  degrading independently — and carries them in the join payload;
  topologyd strict-validates the block (unknown keys, sanity ranges,
  arch vocabulary, a clock-skew fence on collected_at) and stores it
  on the host row (jsonb, NULL for old-CLI joins). The zone stays
  operator-declared: cloud/lan is a placement semantic no probe can
  know. `looming topology facts pull` reads the observed facts back
  (GET /v1/internal/hosts, service-token guarded) and merges them into
  the file's hosts[].capabilities — observed hardware/egress/latencies
  replace declared values, zone and labels never move — landing only
  through the real config validator, so the file stays the source of
  truth and apply stays the operator's next step. Profiles migrate to
  Registry when #8 lands.
