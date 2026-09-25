---
name: conventional-commits
description: Write commit messages and PR titles in Conventional Commits format. Use for every commit and PR; CI enforces the format on PR titles.
---

# Conventional Commits

This repository uses [Conventional Commits](https://www.conventionalcommits.org/).
PR titles are squash-merged into `main`, so **the PR title is the commit
message** — CI enforces the format there.

## Format

```
<type>[optional scope][!]: <description>
```

## Types

- `feat` — new functionality
- `fix` — bug fix
- `docs` — documentation only
- `style` — formatting, no logic change
- `refactor` — restructuring, no behavior change
- `perf` — performance improvement
- `test` — adding or fixing tests
- `build` — build system or dependencies
- `ci` — CI configuration and workflows
- `chore` — maintenance
- `revert` — reverting a previous commit

## Rules

- Scope: lowercase, kebab-case, optional: `feat(gateway): …`.
- Description: imperative mood, lowercase first letter, no trailing period.
- Breaking change: append `!` after scope/type and add a
  `BREAKING CHANGE:` footer explaining it.
- One logical change per commit; squash fixups before review.

## Examples

```
feat(gateway): add api-key authentication middleware
fix(ci): pin shellcheck version in gate workflow
docs: document bundle manifest schema
feat(registry)!: rename tool annotations field
```
