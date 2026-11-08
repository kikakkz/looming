---
number: 42
title: "installer shrinks to CLI-only; agentic setup and purge lifecycle"
date: "2026-11-08"
updated: "2026-11-08"
status: "accepted"
supersedes: []
adopted-at: "2026-11-08"
---

# AD-42 — installer shrinks to CLI-only; agentic setup and purge lifecycle

## Context

The README one-liner conflates two different jobs: installing the
`looming` CLI and deploying the whole cluster in one shot —
`install.sh --email … --engine-url …` generates a topology and
converges the bundle on the spot. That coupling means the installer
owns topology knowledge (placements, upstream wiring, and now, per
AD-41, control-plane bootstrap), the first command needs root for
work the operator has not yet chosen to do, and the "one command"
promises more than a first-time user is ready to decide. Operator
direction (#191, 2026-11-08): one command installs looming only;
cluster bring-up is staged, driven by the looming CLI; and setup
itself is an agent — consistent with AD-38's fixed paradigm (looming
is an agent; LLM proposes, human decides), with #144's advisor
machinery (deterministic evaluator + reasoned proposal,
human-confirmed) already built for exactly this shape.

## Decision

1. **install.sh shrinks to getting the tool**: host preflight
   (docker present; the compose-plugin auto-install of #188 stays)
   plus downloading the `looming` CLI binary onto the PATH. No
   `--email`, no `--engine-url`, no topology generation, no
   converge. The default install location is the user's PATH
   (`~/.local/bin`) — root is not required to install looming; sudo
   enters only later where the cluster genuinely needs system
   access.
2. **Cluster bring-up is `looming setup`** — an agentic admin-face
   command: interview the operator (identity, upstream endpoint,
   ambition: one machine or several) → propose a topology through
   the advisor machinery (deterministic evaluator plus reasoned
   proposal, human-confirmed per AD-38) → drive the bootstrap in
   stages: resolve and pin the release (fetch bundle-manifest.yaml
   into the state dir — the installer no longer touches manifests),
   write the topology, stand up the control plane on the first host
   (headscale and derper per AD-41, join hint emitted as soon as it
   is up), converge the first host, then bring remaining hosts in
   (join + per-host converge), and finish with a cluster-wide
   health check. Re-runnable and idempotent; `--answers <file>`
   reproduces a setup headlessly so CI keeps determinism.
3. **`looming purge` tears down symmetrically, at two levels**:
   per-host — run on each host; stops that host's compose project and
   optionally wipes its local state (pgdata, node keys) — and cluster
   retirement from the control host (`--cluster`: retire node
   identities against headscale, dissolve the roster), which must run
   while the control host's compose project — hence headscale — is
   still up: after every other host's per-host purge, and before the
   control host's own stop. Setup and purge are one lifecycle axis;
   the e2e layer exercises them as a pair.
4. **Contract break handled openly**: the v0.1 installer's
   cluster-in-one-shot form is retired; the README restructures to
   install → setup → join on further hosts, and the release notes
   flag the installer-contract change.

## Consequences

- AD-41's bootstrap items (control plane, join-as-authkey, node
  identity issuance) land inside `looming setup`, not the installer:
  the installer carries zero topology knowledge forward.
- #187 (README sudo guidance) largely dissolves: the default install
  no longer needs root. It stays open until the shrink lands, then
  closes as superseded by this contract.
- The installer smoke gate (release workflow) simplifies to a
  binary-delivery check; a new e2e scenario covers
  setup → converge → purge.
- AD-39's installer-scope provisions (install.sh generating config
  and owning the manifest) are amended hereby — both move into
  `looming setup`; AD-39's release-pipeline core (artifact-only
  delivery) stands unchanged.
- cli-l1's admin face and the L0 context map (Topology & bootstrap)
  are updated in the same PR as this AD.
