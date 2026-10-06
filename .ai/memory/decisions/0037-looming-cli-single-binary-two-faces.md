---
number: 37
title: "looming CLI: single binary, two faces"
date: "2026-10-06"
updated: "2026-10-06"
status: "accepted"
supersedes: []
adopted-at: "2026-10-06"
---

# AD-37 — looming CLI: single binary, two faces

## Context

The onboarding scenario in #108 needs its CLI designed: one binary
serving
admin bootstrap and end-user onboarding, profile reconciliation into
local agent CLIs (AD-27 §4's consumer), the quota view (AD-32's
unified display, first instance), and a status surface future
components can join. The maintainer discussion surveyed the two
precedent shapes for dual-audience CLIs: docker (one binary; admin
subcommands `swarm init/join` beside user subcommands `run/ps`) and
kubectl/kubeadm (a split: user-facing kubectl vs cluster-admin
kubeadm, distributed separately). The split's motivation — different
distribution channels and privilege audiences — is real, but Looming
ships one bundle to both audiences at once.

## Decision

1. **Single binary, two faces.** One `looming` binary carries the user
   face (`onboard`, `configure`, `usage`, `status`) and the admin face
   (`bootstrap`, `apply`, `token create|list|revoke`, `join`) — the
   docker precedent. The kubectl/kubeadm split is rejected: the
   distribution-channel difference is real but outweighed by operator
   familiarity with the single-binary shape and by #108's explicit
   direction ("one binary, two faces").
2. **`cli/` is its own component** (Go, AD-34): one `go.mod` under
   `cli/`, the four-package layout, its own arch matrix — the CLI is
   a client, not part of gateway or topology.
3. **`platform/go/` extraction is deferred to slice CLI-1.** The
   apply/render/exec/config chain stays under `topology/internal/`
   until the CLI's admin face becomes its second same-language
   consumer (AD-34 rule 2's two-consumer trigger); the extraction is
   an implementation PR, and until it lands `topology/cmd/looming-ctl`
   remains the admin entry. Slice CLI-0 ships a pure HTTP client with
   zero platform dependency to prove the split before any code moves.
4. **`configure` reconciles via a managed block** — wait-agent #54
   pattern-1 as the mechanism source: per-agent adapters merge a
   marked region (`# looming:managed` fence, or the agent's native
   include mechanism) into the agent CLI's config; the original file
   is backed up before the first mutation; the merge is byte-idempotent
   (same input → same bytes) and `--undo` restores the pre-managed
   state. The profile is data, not fork (AD-27 §4).
5. **Credentials live outside profiles.** A Profile (name,
   gateway_url, identity_url, credential ref) is the shareable half
   of local state under `~/.looming/profiles/`; raw keys live in
   `~/.looming/credentials.yaml` (mode 0600) and are referenced, never
   embedded — profiles can be shown/shared, credentials cannot. Both
   are user-scoped; the CLI never writes repo state.

## Consequences

- The design lands as `docs/architecture/cli-l1.md`; implementation
  slices CLI-0 (skeleton + user face), CLI-1 (platform/go extraction +
  admin face), CLI-2 (codex/claude adapters + `/healthz` status
  contract) are tracked in #108 after the design PR merges.
- No code moves in the design PR: `topology/cmd/looming-ctl` keeps
  serving until CLI-1 retires it.
- `looming usage` renders identity quota read-side (identity self
  API); the meter record-plane joins the same AD-32 display later —
  the CLI grows no metering authority.
- New agent support is an adapter in a registry, not a core change;
  the status surface grows by probe registration, not hardcoded lists.
