# Contributing to Looming

## Development setup

- Run `make ci-gate` locally; it is the same gate CI runs.
- External agent skills are installed from `.ai/skills.lock.toml`
  (sha-pinned). Do not copy external skill content into this repository.

## Issue-driven development

Every change starts as an issue (`bug`, `feature`, or `task`). Branches
are named `<issue>-<slug>`; PRs reference the issue with `Closes #N`.
Non-trivial work posts a plan to the issue before implementation.

## Issue taxonomy

Issues are classified on two axes, modeled on the Kubernetes SIG
convention:

- `kind/*` — what type of work: bug, feature, task, design, spike,
  cleanup, docs. Set automatically by the issue template.
- `area/*` — which module: gateway, runtime, sandbox, registry, memory,
  scm-adapters, pipeline, bundle, ci, docs, ai-assets.

Labels are defined in [.github/labels.yml](../.github/labels.yml) and
synced to GitHub automatically by the `labels` workflow. Filing through
a template sets both axes for you.

## DCO sign-off

Every commit must carry a `Signed-off-by` line from a human:

```bash
git commit -s
```

By signing, you certify the
[Developer Certificate of Origin](https://developercertificate.org).

## AI contribution policy

AI-assisted contributions are welcome and follow the
[Linux/OpenStack attribution convention](https://docs.openstack.org/nova/latest/contributor/commit-messages.html):

- Disclose AI assistance with trailers: `Assisted-by: <tool> [model]` for
  human-led work, `Generated-by: <tool> [model]` for machine-led work.
- `Co-Authored-By:` must never be used for AI — it misattributes
  authorship and contaminates DCO.
- AI never signs `Signed-off-by:`. A human signs and takes full
  responsibility for the change, including reviewing it.
- Bots and agents follow the same CI, issue, and trailer rules as humans.

CI enforces the trailer policy on every PR.

## Pull requests

- Title in [Conventional Commits](https://www.conventionalcommits.org/)
  format (squash-merged into `main`).
- CI added/updated in the same PR as any code change.
- New languages, frameworks, or designs land with their full supporting
  kit (CI checks, skills, convention updates) in the same PR.
- `make ci-gate` green; PR template checklist complete.
