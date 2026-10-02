# Architecture overview

The readable companion to the decision records. Facts live in
[AD-27](../../.ai/memory/decisions/0027-product-architecture-v0-2-components-invariants.md)
(components and invariants), [AD-28](../../.ai/memory/decisions/0028-product-domain-context-map-v1.md)
(domain context map), and [AD-29](../../.ai/memory/decisions/0029-gateway-thin-slot-host.md)
(gateway slot shape); the normative vocabulary is in
[glossary.md](glossary.md). Solidified from #66 (closed) — the
diagrams below render the same v0.2 map the decisions narrate.

## One-paragraph shape

Looming is an enterprise agent-engineering platform shipped as one
bundle. A thin slot-hosting **gateway** fronts model traffic with
authn, quota enforcement, a pluggable interception chain, and
faithful forwarding. **Sessions** (durable identity plus an
append-only event stream) are the proprietary core; every surface
(CLI, later IM) is a renderer and every harness (our runtime, or
codex/claude/kimi-code/goose in sandboxes) is an adapter against the
session protocol. **Orchestration** dispatches planner/executor/
worker/judge roles into **model-triggered sandboxes**. **Knowledge**
(RAG over org documents), **Memory** (review-gated experience), and
**Registry** (admission governance for tools/MCP/skills/policy rules)
are three separate contexts. The append-only metering records,
interaction bodies, and audit decision events are owned by the
**Records/observability** context (the Session context separately
owns its runtime event stream, AD-27 §3). SCM integration and CI
orchestration connect the organization's existing engineering estate.

## Data plane — one request's journey

```mermaid
flowchart TD
  subgraph ENTRY["入口层"]
    CLI["开发者 CLI: @agent + issue（后期 IM 同一入口）"]
  end

  subgraph DP["数据面 — 一次请求的旅程"]
    GW["gateway dp<br/>鉴权 · 配额 · 忠实转发 · 计量<br/>风控拦截链（中间件插件可插）<br/>模型 provider 可插（OpenAI 兼容）"]
    RT["agentruntime（规划 + 工具调用）<br/>codex / claude / kimi / goose / 自带 可插<br/>能力级 L0–L3 · 全程携带用户身份"]
    PEP["PEP 每次工具分发前 fail-closed 判定<br/>策略引擎可插：OPA / Cedar"]
    CPX["credential proxy<br/>vault 后端可插<br/>凭证不进沙盒 / 模型上下文"]
    SB["sandbox plane 模型触发 · 弹性创建回收<br/>Docker fleet / K8s / E2B 可插"]
    SCM["SCM adapter 可插：GitHub / GitLab CE"]
    CI["org CI gate（闸位）+ judge（可插 runtime）<br/>judge fail case 回流 CI"]
  end

  subgraph KNOW["知识层 — agent 的读物与工具（AD-28: knowledge / memory 分域）"]
    KNOWDOC["knowledge RAG 领域文档<br/>ACL 裁剪检索（不变量）"]
    MEM["memory 智能体经验<br/>ACL 裁剪检索（不变量）<br/>gptmem / mem0·mem3 / mem-palace 可插"]
    REG["registry 准入治理（不变量）<br/>tools / MCP / skills · 单件可插上架"]
    CONN["connectors 可插：钉钉 / 飞书 / Confluence<br/>ACL 随文档入库（不变量）"]
  end

  subgraph CPL["控制面 — 不变量的居所"]
    ORCH["编排器 blueprint / 状态机 / 分发"]
    EB[("事件骨干 append-only · 必写")]
    TOPO["拓扑引导 首启向导 · 加机器"]
  end

  subgraph CONS["消费面 — 事件的读者"]
    AUD["审计者 who · via-whom · what · decision"]
    PA["离线 prompt 分析（直读存储）"]
    JL["judge 学习回流"]
  end

  CLI --> GW --> RT
  RT -- "每次工具分发判定" --> PEP
  RT -- "取凭证" --> CPX
  RT -- "供给请求" --> SB
  RT --> KNOWDOC
  RT --> MEM
  RT --> REG
  CONN --> KNOWDOC
  SB -- "产出以发起用户身份" --> SCM --> CI
  ORCH --> RT
  GW -- "计量" --> EB
  PEP -- "决策落账" --> EB
  CI -- "事件" --> EB
  EB --> AUD
  EB --> PA
  CI --> JL
  JL -- "品味进基建" --> REG

  classDef inv stroke-width:3px,stroke:#d73a4a;
  classDef slot stroke-dasharray:5 5;
  class GW,PEP,CPX,CI,KNOWDOC,MEM,REG,EB,TOPO inv;
  class RT,SB,SCM,CONN slot;
```

Solid boxes are invariants (never pluggable); dashed boxes are slots
(implementation-swappable behind small contracts, AD-27 §2).

## Surface / Session / Harness layering

Surfaces are thin and addable; the session layer is proprietary; the
harness layer is a set of adapters:

```mermaid
flowchart LR
  subgraph SURFACE["Surface 表现层 (薄,可增)"]
    CLI["我们的 CLI 客户端"]
    IM["IM bot 钉钉/微信/Slack"]
    WEB["(后期 web)"]
  end
  subgraph SESSION["Session 会话层 专有" ]
    S[("session 事件流<br/>durable identity<br/>append-only · Postgres")]
  end
  subgraph HARNESS["Harness 执行层 (可插)"]
    OWN["自带 runtime"]
    CODEX["codex CLI"]
    CLAUDE["claude code"]
    KIMI["kimi-code"]
    GOOSE["goose / 其他"]
  end
  CLI <-->|"长连接订阅(流式)"| S
  IM <-->|"线程推送(进度+审批卡片)"| S
  WEB <--> S
  S <-->|"harness adapter 协议"| OWN
  S <--> CODEX
  S <--> CLAUDE
  S <--> KIMI
  S <--> GOOSE
```

## Third-party agent CLIs: dual mode

Third-party CLIs integrate as surfaces via MCP — not by forking them.
Two modes, one default:

```mermaid
flowchart LR
  subgraph LOCAL["用户本机"]
    CC["claude code / codex / kimi-code<br/>(自己的 agent loop)"]
    SKILL["注入的 @ker skill/hook<br/>幂等配置调和 (wait-agent 机制)"]
    CC --- SKILL
  end
  subgraph BACK["Looming 后端"]
    MCP["looming MCP server<br/>session client adapter"]
    ORCH["agentruntime 编排器<br/>planner / workers / judge"]
    SB["sandbox plane"]
    MCP --> ORCH --> SB
  end
  CC -- "ker_start / ker_events /<br/>ker_reply / ker_stop (MCP tools)" --> MCP
  MCP -- "session 事件流回推" --> CC
```

With the injected profile, memory and registry tools are the
platform's (approved tools only, hidden pre-model), and the routing
invariant holds mechanically:

```mermaid
flowchart TD
  subgraph LOCAL["用户本机: 注入 profile 的第三方 CLI"]
    LOOP["本地 agent loop"]
    SKILL["@ker skill + 平台工具集(MCP)"]
    LOOP --- SKILL
  end
  subgraph BACK["Looming 后端"]
    MCP["looming MCP server"]
    ORCH["agentruntime 编排器"]
    MEM["memory ⚓ACL 裁剪"]
    REG["registry ⚓准入"]
    SB["sandbox plane"]
    LOOP2["worker 内可跑 ▸claude/codex 当 harness"]
    MCP --> ORCH --> SB --> LOOP2
    MCP --> MEM
    MCP --> REG
  end
  LOOP -- "sub-agent spawning: ker_start" --> MCP
  LOOP -- "trivial inline work stays local; platform tools via MCP" --> LOOP
  LOOP -- "memory 读写 · registry 工具调用" --> MCP
  LOOP -. "escape ①: @local (explicit pure BYO)" .-> LOOP
  LOOP -. "escape ②: worker=claude/codex(后端编排,沙盒里跑该CLI)" .-> MCP
```

**The routing invariant is one line: sub-agent ⇒ ker.** The local
spawn tool is hidden, so `ker_start` is the only door. Escapes are
explicit: `@local` (pure BYO, out of platform audit) and
`worker=claude|codex` (backend orchestration with that CLI executing
inside the worker sandbox).

## Reading order

1. [context-map.md](context-map.md) — the eleven bounded contexts
2. [glossary.md](glossary.md) — normative terms
3. `docs/component-patterns.md` — the four binding engineering patterns
4. The AD records — why each shape is what it is
