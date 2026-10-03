---
number: 33
title: "gateway dependency matrix v0"
date: "2026-10-03"
updated: "2026-10-03"
status: "accepted"
supersedes: []
adopted-at: "2026-10-03"
---

# AD-33 — gateway dependency matrix v0

## Context

AD-23 declares the bounded-context dependency matrix
(`.go-arch-lint.yml`, "one root go.mod", the `internal/<capability>/`
four-package shape) and requires extending the map via a superseding
AD in the same PR. The gateway L1 design (docs/architecture/gateway-l1.md,
#98) fixed the module split and its three boundary rules; slice 0 (#99)
is the first code under the matrix. The V0 names `gateway/dp` and
`gateway/cp` map onto the L1 modules as `front` and `control`.

## Decision

The matrix gains the gateway's v0 entries:

- `internal/engineplane` — the engine slot contract (AD-32's
  EnginePlane.Forward + EnginePlane.Admin). It is the one cross-
  capability port: consumed by both the front layer (Forward) and the
  control plane (Admin), so it stands as its own capability with a
  single package rather than being duplicated per consumer.
- `internal/front/{domain,port,app}` — the front layer. app may depend
  on engineplane, front-domain, and front-port; nothing may import
  front-app or the engine adapters' internals.
- `internal/control/{domain,app}` — the control plane. app depends on
  control-domain only; every projection and provisioning path stays
  inside it.
- `internal/engine/default` — the default engine adapter; depends on
  engineplane alone.
- `cmd/gateway` — wiring only; the sole package allowed to import all
  of the above (manual DI, no frameworks).

This encodes gateway-l1 §5's three boundary rules as machine-checked
facts: dp-level code sees the engine only through engineplane; the only
engine write path is EnginePlane.Admin inside control-app; the front
layer holds no policy authority (it reads ports, never writes them).

## Consequences

- AD-23's map is extended, not replaced; cicd / agentruntime /
  registry entries land with their own slices, each with its matrix
  entries in the same PR.
- Adding an engine adapter (the LiteLLM adapter is the next one) is a
  new `internal/engine/<name>` module depending only on engineplane —
  a matrix edit, reviewable in the adapter's PR.
- The four-package shape (app/domain/port/adapter) applies per
  capability as code lands; empty packages are not created in advance.
