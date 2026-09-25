---
updated: 2026-09-25
type: decisions
---

# Decisions

Architecture decisions now live **one file per decision** in
[decisions/](decisions/) — see the generated index there (AD-21 records
why). This file remains as a stable pointer so existing links keep
working.

Rules: decision files are immutable except status flips; supersede via
`python3 .ai/tools/adr_manager.py new --supersedes N`; regenerate the
index with `adr_manager.py index`; `make check-adr` validates links and
index freshness.
