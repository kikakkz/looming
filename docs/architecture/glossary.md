# Glossary — product-plane ubiquitous language

Terms are normative: code, docs, and issues use them with exactly
these meanings. Amendments ride the same PR as the context-map
changes that need them.

| Term | Meaning |
|------|---------|
| **Surface** | A thin renderer of the session stream (our CLI, IM bots, later web). AD-27 §3. |
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
| **Quota** | A per-user/org consumption policy owned by Identity & access; enforced statelessly at the gateway. |
| **ScmProvider** | The SCM-integration context: issues, review threads, CI status, merge control, webhooks. |
| **FailCase** | A CI gate's failing case; judge-derived fail cases land here (judge→CI loop). |
| **Credential proxy** | The only minter/holder of AD-6 scoped credentials; tokens never enter sandboxes or model context. |
| **Bundle** | The single deployable packaging of all components; topology wizard at first boot. |
