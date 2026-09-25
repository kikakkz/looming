# AGENTS.md — `.ai/`

Agent assets live here. Everything in this directory is checked by CI
(`make ci-gate`); anything added here must arrive with its checks in the
same PR.

## `skills/` — self-authored skills

- One directory per skill, each containing a `SKILL.md` following the
  Agent Skills open standard: YAML frontmatter with required `name` and
  `description`.
- Frontmatter may set `license: Apache-2.0`.
- Body: concise, imperative, tool-agnostic instructions. Link, don't copy,
  external reference material.

## `skills.lock.toml` — external skill pins

- External skills are **never copied** into this repository. They are
  pinned here by immutable git revision and installed with
  `npx skills add` (or equivalent).
- One entry = one skill. Fields: `name`, `source` (host on the allowlist),
  `path` (directory containing the upstream `SKILL.md`), `revision`
  (full 40-char sha), `license`.
- Adding or upgrading an entry is a PR; CI validates schema, sha format,
  and license allowlist.

## `mcp/servers.lock.toml` — MCP server pins

- Intentionally empty at bootstrap. Add an entry only with (a) a
  demonstrated need in the issue, (b) an immutable revision pin, and
  (c) an admission security scan report attached to the PR.
- Schema matches `[[server]]` entries: `name`, `source`, `revision`,
  `license`.

## `tools/` — repository scripts

- Every script ships with unit tests in `tests/` in the same PR.
- Bash scripts must pass `shellcheck`; Python must be stdlib-only.
- Scripts must be deterministic and side-effect free outside their args.

## `memory/` — memory bank

- Plain markdown with `updated:` frontmatter; files interlink with
  wikilinks. No vector store, no binary formats.
- `progress.md` and `activeContext.md` are **episodic**: agents update
  them directly at session boundaries.
- **Addressing rule**: memory entries reference stable identifiers only —
  issue numbers (`#N`) and decision numbers (`AD-N`) — never file anchors
  or line numbers, so rotation never breaks links.
- `progress.md` **rotates monthly**: `python3 .ai/tools/memory_housekeeping.py
  rotate` moves older entries to `progress/YYYY-MM.md` and prepends a
  generated "carried over" section. Rotation is done by the script, never
  by hand. Soft threshold: 200 lines.
- `activeContext.md` is a **links-only snapshot** (SSoT — details live in
  progress / decisions / issues; never restate content). Hard cap:
  100 lines; compress at session boundaries when over.
- **No decay or ranking** in the repo layer (AD-9): expiry and relevance
  ranking belong to the platform memory service.
- `memory_housekeeping.py doctor` audits sizes; the monthly `housekeeping`
  workflow opens a `kind/cleanup` issue when thresholds are exceeded.
- `decisions/` — **procedural**: one file per architecture decision
  (`NNNN-slug.md`), immutable except status flips. Create or supersede via
  `python3 .ai/tools/adr_manager.py` (supersede is atomic and
  bidirectional); the index (`decisions/README.md`) is generated, never
  hand-edited. All changes, including status flips, land through a
  reviewed PR (prompt-injection defense). `make check-adr` validates.
- `decisions.md` is a stable pointer stub — never add content to it.
- Never store secrets in memory files.
