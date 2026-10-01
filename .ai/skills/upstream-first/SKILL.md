---
name: upstream-first
description: Settle methodology, process, and tooling questions by researching and adopting mature upstream practice first. Use whenever a decision has no repository rule yet. Escalate to the maintainer only when no good reference exists or the decision is organization-novel.
---

# Upstream First

This repository does not invent methodology. When a question has no rule
in `AGENTS.md` or the decision records, the default action is to find
what mature, well-known projects already do — and adopt that.

## Loop

1. **Check the house rules first.** `AGENTS.md` (root and `.ai/`),
   `docs/`, and the decision records in `.ai/memory/decisions/` already
   settle many questions. A standing rule wins over any external pack.
2. **Research upstream.** Look for the practice in mature projects with
   multiple independent adopters (e.g. language-ecosystem flagships,
   CNCF/Linux Foundation projects, widely referenced architecture
   exemplars). Prefer practices you can cite by name.
3. **Adopt and cite.** Land the practice with its provenance: name the
   reference projects in the commit or PR, and record an AD when the
   choice is architectural. Never present an adopted practice as our
   invention.
4. **Escalate only when genuinely novel.** Bring the question to the
   maintainer only if (a) no good reference exists, (b) references
   conflict materially and the trade-off is ours to own, or (c) the
   decision is organization-novel — the first of its kind for this
   project. State what you researched and why it did not settle the
   question.

## Anti-patterns

- Pausing work to ask "which one do you prefer?" when mature practice
  answers it — this is the failure mode this skill exists to prevent.
- Copying a single project's quirk that no one else adopted.
- Escalating with no research attached. "Here is what X, Y, and Z do,
  here is why that does not fit" is escalation; "what should we do?" is
  not.

## Provenance

Maintainer standing rule, recorded in the 2026-10-01 progress entry;
first executed for the methodology adoption tracked as #24–#27 →
AD-23/AD-24/AD-25 (#33–#35).
