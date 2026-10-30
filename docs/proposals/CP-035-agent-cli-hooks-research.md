# CP-035 — Research: agent CLI hooks inside gopod containers

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-30 |
| Origin | Owner question 2026-10-30: what can be moved onto Claude Code / Codex hooks in gopod containers; autonomy analysis open item (does a Claude `PreToolUse` hook fire under bypass) |
| Size | S (research only; any build is a later CP or spec) |
| Touches | none yet; candidates would touch `internal/runner` (provider args, mounts, policy fingerprint) and `container/` images |
| Depends on | — |
| Spec | [research survey and plan](../research/2026-10-30-agent-cli-hooks.md) |

## Problem

Behaviors that must act inside the agent loop (deny a tool, inject context, require an outcome before
stopping) are either done by flags that cannot express them (Claude has deny-by-name only, so a tool
added by a connector update passes) or by workarounds (Codex memory is prepended to the prompt).
Both CLIs now document hooks that could do this, but gopod disables hooks everywhere on purpose, the
agent can write its own config directories, and Codex hooks need a trust step whose behavior under
`codex exec` is undocumented. Without evidence we cannot tell which uses are safe.

## Proposal

Run the nine synthetic experiments in the survey (E1-E9) on the pinned Claude Code 2.1.310 and Codex
0.164.0 images, and produce a verdict per candidate (move to hook / keep host-side / defer). No
production code, no image changes, no owner account, no Telegram.

## Out of scope

Building any hook; changing group isolation (hooks stay disabled there until E8 says otherwise);
developer-time repo hooks (candidate C9, a separate small track); bumping the pinned CLI versions.

## Risks

Docs describe newer versions than the pins; results may only hold after a pin bump. Experiments run
containers on the owner's machine (local Docker, synthetic data). A positive result tempts a quick
build; the verdict must go through a spec, and an ADR if it changes container policy.

## Acceptance

- Each of E1-E9 has a recorded result (command, version, observed output, pass/fail) in the survey.
- The survey states, for Claude and Codex separately, one delivery channel the agent cannot edit, or
  says none exists.
- The survey answers: does `PreToolUse` deny hold under the bypass flag on each pinned version.
- Every candidate C1-C9 has a verdict and the reason, using the criteria in the survey section 5.
- The "documented / not confirmed" labels in the survey are replaced by observed results where tested.
