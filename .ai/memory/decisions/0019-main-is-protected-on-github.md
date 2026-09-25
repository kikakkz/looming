---
number: 19
title: "`main` is protected on GitHub"
date: "2026-09-25"
status: "superseded"
supersedes: []
adopted-at: "2026-09-25"
superseded-by: 20
superseded-at: "2026-09-25"
---

# AD-19 — `main` is protected on GitHub

`main` is protected on GitHub: required checks `gate` + `conventional-title`
(strict, branches must be up to date), linear history, unresolved
conversations block merge, no force pushes, no branch deletions,
`enforce_admins: true`. Review count is 0 while the maintainer is solo
(GitHub forbids authors approving their own PRs); raise to 1 and enable
code-owner review when human collaborators or agent-authored PRs become
routine. Convention change: the project name is **Looming** (renamed from
Loom in PR #2); branch naming stays `<issue>-<slug>` without a type prefix
(mature OSS practice enforces PR title and issue linkage, not branch names).
