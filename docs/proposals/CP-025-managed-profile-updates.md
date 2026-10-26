# CP-025 — Managed profile sections and concurrent edit protection

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | M |
| Planning wave | 3 prerequisite — candidate, not a change to the approved queue |
| Touches | profile storage/API, instruction assembly, runtime fingerprints |
| Depends on | CP-020 profile policy; existing seeded AGENTS/CLAUDE files |
| Spec | [Design](../superpowers/specs/2026-10-26-managed-profile-updates.md) |

## Problem

Seed-only templates do not update existing custom profiles safely; parallel tasks/providers can
overwrite changes read at an older revision.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

Autonomous personality replacement, changing owner goals, or full personal filesystem lockdown
as a side effect.

## Risks

Lost custom text; policy drift across providers; mistaking a hash check or high-priority prompt
for a sandbox.

## Acceptance

- Two writers read one revision: first patch commits, stale second is rejected and custom text
  survives.
- Image policy update reaches legacy/resume/fresh sessions for both providers without rewriting
  unrelated preferences.
- Workers, forwards and forged owner/profile claims cannot acquire profile writer authority.
- Managed section collision, unsafe projection path and interrupted write retain original bytes
  and a recoverable conflict.
