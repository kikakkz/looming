# Glossary — product-plane ubiquitous language

Terms are normative: code, docs, and issues use them with exactly
these meanings. Amendments ride the same PR as the context-map
changes that need them.

| Term | Meaning |
|------|---------|
| **Surface** | A thin renderer of the session stream (our CLI, IM bots, later web). AD-27 §3. |
| **Profile** | A CLI-local connection profile: name, gateway/identity endpoints, and a credential reference, stored under `~/.looming/profiles/`; the shareable half of local CLI state — credentials never ride inside it (AD-27 §4, AD-37). |
| **AgentAdapter** | A CLI plugin that reconciles a Profile into a local agent CLI's config file via a managed block (`# looming:managed` fence or the agent's native include): byte-idempotent merge, backup before first mutation, `--undo` removes the block and restores the pre-managed content of the managed region — out-of-region edits survive (AD-27 §4, AD-37). |
| **Session** | Durable runtime identity plus the append-only event stream; the proprietary core. |
| **Harness** | An agent runtime executing work (our runtime, or codex/claude/kimi-code/goose in sandboxes); attaches via the harness-adapter protocol. |
| **ker** | The platform agent entry point (`ker_start`/`ker_events`/`ker_reply`/`ker_stop`); the only door for spawned sub-agents. |
| **Blueprint** | A reusable orchestration plan: roles, stages, tool/policy bindings. |
| **Run** | One execution of a blueprint; the unit orchestration tracks. |
| **Task** | A unit of work inside a run, routed by task class (local inline vs sub-agent ⇒ ker). |
| **Worker** | An execution slot that runs a task, optionally with a third-party CLI as its harness. |
| **Judge** | A review role over produced artifacts; its fail cases become CI FailCases, its taste proposals become Registry items. |
| **Sandbox** | An isolated execution environment provisioned by a model trigger; credentials never enter it. |
| **PEP** | Policy Enforcement Point: fail-closed evaluation before every tool dispatch. |
| **PolicyRule** | A review-gated, versioned rule artifact in the Registry (same lifecycle as tools/MCP/skills). |
| **DecisionEvent** | One append-only audit record: who · via-whom · what · decision (Agent 365 subject/actor shape). |
| **MeterRecord** | One append-only metering record emitted inline at the gateway. |
| **InteractionBody** | The raw user↔model exchange, stored async, downstream of the gateway. |
| **MemoryItem** | A review-gated memory entry at org/project/user scope. |
| **KnowledgeBase** | A RAG-style domain-knowledge collection; items carry source ACLs at ingest. |
| **Registry admission** | The review/approval/A-B workflow that admits tools, MCP servers, skills, and policy rules. |
| **Quota** | A per-user/org consumption policy owned by Identity & access; executed by the engine instance (native budgets); displayed uniformly from MeterRecords (AD-32). |
| **ScmProvider** | The SCM-integration context: issues, review threads, CI status, merge control, webhooks. |
| **FailCase** | A CI gate's failing case; judge-derived fail cases land here (judge→CI loop). |
| **Credential proxy** | The only minter/holder of AD-6 scoped credentials; tokens never enter sandboxes or model context. |
| **Bundle** | The single deployable packaging of all components; first boot converges a declarative topology file via `looming apply` — no web wizard (AD-36). |
| **Front layer** | The always-present Looming-owned component all gateway traffic passes through: authn fan-in, model-permission enforcement, interception chain, credential injection, event emission. Never routes, never executes quota (AD-32). |
| **Engine slot** | The replaceable forwarding half of a gateway instance (default thin implementation / LiteLLM / Kong); configured to pass through; owns routing config and quota mechanics. |
| **EnginePlane** | The engine slot contract: `Forward` (northbound OpenAI-compatible endpoint, plain proxying) + `Admin` (ProvisionKey / SetBudget / RevokeKey / GetUsage). |
| **IdentityMap** | The identity-context aggregate mapping a Looming key to its per-engine shadow credential; provisioned at key creation; cached read-only in the gateway. |
| **Provisioning** | Creating the engine-side shadow credential for a Looming key (Journey 2); a first-class lifecycle state, not an error (SCIM semantics). |
| **Projection** | A read-only copy of another context's data (engine config, identity caches); refreshed by events or call-through; never a write authority. |
| **Principal** | The only identity primitive in flat RBAC (AD-35): kind human or service; carries roles, keys, quota. |
| **Role / Permission** | Role = named permission bundle (builtin admin/member); permission = fine-grained string (`gateway:use`, `model:use:<id>`, …). |
| **LoomingKey** | A principal's API key; stored hashed, shown once at issuance, revoked one-way. |
| **RegistrationPolicy** | The deployment's onboarding rule: admin-only, invite, or self-register-with-approval. |
| **Topology** | The declarative deployment topology: a YAML desired-state file (hand-editable, versionable) plus the DB observed state, reconciled by `looming apply` (AD-36). |
| **Host** | A registered machine in the topology (id, address, role labels, joined_at); joins via a one-time invite token — pull self-registration, no SSH (AD-36). |
| **JoinToken** | kubeadm-style bootstrap token: one-time consume, TTL (default 24h), hash-stored, role-scoped, revocable/rotatable (AD-36). |
| **ComponentPlacement** | A component × host declaration in the topology, with port/config overrides; the gateway-front placement is exactly one in phase-1 (#109 lifts to N, AD-36). |
| **Guide page** | The cluster's public, unauthenticated onboarding page for **end users**: where to download the CLI, the cluster's identity/gateway endpoints, how to register or ask an admin. Rendered from the current Topology snapshot; served only when `access.public` (default true; off = invite-only orgs); zero credentials by invariant. It is NOT bootstrap instructions — admins get those from the repo. |
| **ComponentProfile** | Per-component requirement knowledge: hard floors (resources/egress/arch/ports) consumed only by the deterministic evaluator, plus soft preferences serialized into the model context; review-gated data shipped in the bundle, migrating to Registry (#8) when it lands (AD-38). |
| **Facts** | A machine's declared-or-discovered capabilities — hardware (cpu/mem/disk/arch) + network (zone cloud\|lan, egress, optional latencies_ms); the `Host.capabilities` block; a missing fact a hard rule needs fails closed with the gap named (AD-38). |
| **Advise** | The management agent's placement-derivation surface (`looming advise [--reason]`, #144): facts + profiles → deterministic feasibility matrix (slice 1.1); with `--reason`, model-ranked proposals with reasons/risks over the feasible set + soft preferences + session free text, every placement re-validated against the evaluator (degrading to the table on repeated violation), previewed as a unified diff, and written to the topology file only on human confirm — never auto-applied (AD-38, slices 1.2-A/1.2-B). |
| **Genesis** | The bootstrap model credential's three-stage lifecycle: direct endpoint while the cluster is empty → after convergence rides the gateway as an upstream engine key → local copy erased; the management agent's model channel (#143). |
