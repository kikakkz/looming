# CLI L1 — module design

Derived in #108 (the onboarding scenario's CLI). Authority: #108's scope
statement, AD-27 §4 (third-party agent CLIs degrade to surfaces via
profiles), AD-32 (unified quota display), AD-34 (polyglot layout); AD-37
records this design's decisions. Vocabulary: [glossary](glossary.md).

## 1. Scope

The `looming` CLI: one binary, two faces — a user face that onboards
users and reconciles their local agent CLIs, and an admin face that
bootstraps and extends the cluster. The docker precedent: a single
`docker` binary carries admin subcommands (`swarm init/join`) beside
user subcommands (`run/ps`); the kubectl/kubeadm binary split is
explicitly rejected (AD-37). Component: `cli/` (Go, AD-34).

The CLI is a pure client: it holds only the user's own credentials,
talks HTTP to identity/gateway/topologyd, and never touches server
state except through those APIs. The gateway public guide page it
consumes at onboarding is topology T3's surface (#107) — this doc
names only the contract values.

## 2. Model (local state)

```
~/.looming/
  profiles/*.yaml    Profile — the shareable half: name, gateway_url,
                     identity_url, credential ref
  credentials.yaml   credential values only; mode 0600 — never inside
                     a profile
```

- **Profile** (name, gateway_url, identity_url, credential ref): the
  unit a user can show, share, and check into dotfiles. It references
  a credential; it never carries one.
- **Credentials** (`credentials.yaml`, mode 0600): raw Looming keys by
  reference. Profiles can be shown/shared; credentials cannot — the
  separation is the invariant.
- Both are **user-scoped** (`~/.looming/`), never repo state: no
  `.looming/` inside projects, nothing the CLI writes is ever committed.

## 3. Journeys

**User onboarding** (the #108 scenario spine): visit the gateway URL →
public guide page (download CLI, endpoints, register-or-ask-admin) →
`looming onboard` (endpoint from the page; register per the
RegistrationPolicy or login; create key — the raw key renders exactly
once into `credentials.yaml`; the default Profile is written) →
`looming configure --agent kimi-code` (managed block merged into the
agent CLI's config) → chat completions flow through the gateway →
`looming usage`.

**Admin bootstrap**: install the bundle on the first host →
`looming bootstrap` (wizard: cluster name, host count, addresses,
placements → renders `/etc/looming/topology.yaml`) → invokes `apply`
(converge per topology-l1 §3; prints the invite link plus the join
hint) → operator carries the invite to the first admin's mailbox.

**Adding a host** (admin): `looming token create --role engine --ttl
24h` → on the new host: install bundle → `looming join
http://<first-host>:<port> --token <t>` → the host credential lands
at `/etc/looming/host.cred` (0600, shown exactly once) — the
topology-l1 §3 join journey, unchanged.

## 4. Command surface

**User face.**

- `looming onboard` — guide-driven: endpoint → register-or-login per
  the RegistrationPolicy → create key → writes the default Profile.
  Every prompt has a non-interactive flag (`--endpoint`, register vs
  login selection, username/password or invite token, key name,
  `--profile`); fully scriptable end to end.
- `looming configure [--agent NAME] [--profile P]` — reconciles a
  Profile into local agent CLIs through the AgentAdapter contract
  (§5): idempotent (same input → same bytes), reversible
  (`--undo`). `--agent` omitted lists detected agents.
- `looming usage` — the quota view, read from the identity self API
  (`GET /v1/self/quota`, identity-l1 §6). The first instance of
  AD-32's unified display; the meter record-plane joins it later —
  one renderer, not two sources of truth.
- `looming status` — component health. An extensible probe registry,
  not a hardcoded list: new components register their probe (name,
  endpoint, health shape) and `status` renders the table.

**Admin face** (local-root operations; `apply` is not a remote API in
phase-1, per topology-l1 §8).

- `looming bootstrap` — the wizard: asks cluster name, host count,
  addresses, component placements → renders
  `/etc/looming/topology.yaml` → invokes `apply`. Fully
  non-interactive from flags for scripted installs.
- `looming apply` — converge, per topology-l1 §3 (validate → state
  plane → persist → render → compose up; restart only what changed).
- `looming token create|list|revoke` — join-token admin, per
  topology-l1 §7 (direct to the topology DB side; `create` prints the
  raw token once).
- `looming join <first-host-url> --token <t>` — the pulling host's
  self-registration (topology-l1 §7); the join payload carries the
  host's observed machine facts (advisor slice 1.3), collected locally
  and announced on stdout.
- `looming topology facts pull` — merge the observed facts topologyd
  holds (GET /v1/internal/hosts, service-token guarded) into the
  topology file's hosts[].capabilities: observed hardware/egress/
  latencies replace declared values, the zone and labels stay
  operator-declared, the merged file re-validates through the config
  loader before it is written, and nothing applies — the operator
  reviews the diff and runs `looming apply` (advisor slice 1.3).

Naming boundary: user-face `looming status` is remote component health
(§4 user face). Host-supervision wrappers (`status/restart/tail` as
docker command wraps, topology-l1 §2/§7) stay out of the admin face as
decided; they live with the host tooling and are unaffected by this
design.

## 5. AgentAdapter contract

The AD-27 §4 consumer: the profile is "data, not fork — idempotent
config reconciliation, reversible" — wait-agent #54 pattern-1's
mechanism. Per-agent adapters (kimi-code first; codex, claude in
slice CLI-2):

- **Managed block**: the adapter merges a marked region into the agent
  CLI's config file — a `# looming:managed` fence, or the agent's
  native include mechanism where one exists.
- **Backup**: the original file is copied before the first mutation.
  `--undo` removes the managed block and restores, from that backup,
  the pre-managed content the block replaced — edits the user made
  elsewhere in the file after `configure` survive; the restore is
  scoped to the managed region, never a wholesale file revert.
- **Idempotency**: `configure` with the same input produces the same
  output bytes; reconciliation, not appending.
- **Registry**: agent name → adapter. Adding an agent is adding an
  adapter; no core changes, no CLI release coupling.

## 6. Platform extraction (the platform/go seam)

AD-34 rule 2: `platform/<lang>/` exists only at two or more
same-language consumers. The apply/render/exec/config chain gained its
second consumer the moment `cli/` shipped its admin face, so the chain
moved to `platform/go/` and both the topology component and `cli/`
consume it — landed as slice CLI-1 (#108). The kit carries the
topology/host/guide/join domain vocabulary with their ports, services,
and postgres adapters, plus the schema migrations; `topologyd` is the
topology component's only remaining binary, and the admin face lives
in the `looming` binary. Slice CLI-0 proved the split before the move:
a pure HTTP client with zero platform dependency.

## 7. Aspects

PEP — the CLI enforces nothing; it holds only the user's own
credentials (0600) and presents them where asked. Identity propagation
— the LoomingKey travels in `Authorization: Bearer` to identity and
the gateway only; it never enters agent config files, sandboxes, or
model context (the AgentAdapter merges endpoints and references, not
keys — the agent authenticates against the gateway per the platform's
own mechanism). Audit — none: a client tool, no authority. Metering —
`usage` is a read-side renderer of identity quota today; the records
plane joins the same display later (AD-32). Revisions/idempotency —
configure's managed-block merge is byte-idempotent; undo restores the
pre-managed content of the managed region only — out-of-region edits
survive; bootstrap's render and apply's converge reuse the
topology chain's idempotency.

## 8. Deferred (named triggers)

- More agent adapters — trigger: a user actually runs that CLI.
- `looming chat` REPL — trigger: TUI demand; explicitly out of scope.
- Plugin marketplace for third-party adapters — trigger: the first
  third-party adapter contribution (then the registry grows a
  distribution channel).

## 9. Implementation slices (tracked in #108 after this design lands)

- **CLI-0** — `cli/` skeleton + user face (`onboard`, `configure
  --agent kimi-code`, `usage`): pure HTTP client, zero platform
  dependency (proves the §6 split).
- **CLI-1** — `platform/go/` extraction + admin face migration +
  `topology/cmd/looming-ctl` retirement. **Done (2026-10-08, #108):**
  the headless kit lives in `platform/go/`, the `looming` binary
  carries the admin face (apply/token/guide/join), and topologyd is
  the topology component's only binary.
- **CLI-2** — remaining agent adapters (codex, claude) + status
  deep-dive (the component `/healthz` contract lands here).
