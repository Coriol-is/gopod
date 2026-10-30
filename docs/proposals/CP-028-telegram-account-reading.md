# CP-028 — Telegram user-account connector (read and write, Go)

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | L |
| Planning wave | 4 deferred — candidate, not a change to the approved queue |
| Touches | `internal/tgaccount` broker, owner-only `/tg` command, encrypted session file, store tables, runner capability, agent guide |
| Depends on | CP-020 credential isolation; CP-027 grants; CP-024 provenance; CP-007 private backups |
| Spec | [Go user-client design (2026-10-30)](../superpowers/specs/2026-10-30-telegram-user-client.md); supersedes the read-only [2026-10-26 design](../superpowers/specs/2026-10-26-telegram-account-reading.md) |

## Problem

Reading Telegram sources beyond the bot's retained authorized messages, and acting as the owner
in their own dialogs, may be useful, but a user's account session can expose far more than the
requested chat, and a send from the real account cannot be undone.

## Proposal

Owner decisions of 2026-10-30: port to **Go** with `gotd/td` (the owner's Python `telegram-cli` and
`telegram-mcp` are only a map of the needed calls), support **read and write** under a per-class
policy (default: ask the owner before anything that leaves a visible trace), and make it available
**only in the owner's private chat**, never in groups and never reachable from group runtimes. The
session never enters an agent container or a mount. The design (broker, capability-based tool API,
QR login through the private chat, encrypted session file under a refining ADR, flood-wait and account
safety, untrusted-content handling, `/tg` command, tests, PR outline) is in the linked spec. This
proposal authorizes no implementation, credentials, account login or rollout until the owner accepts
the spec.

## Out of scope

Group or shared-chat use, account administration (join/leave, contacts, profile, privacy, other
sessions, takeout), automatic history ingestion, bulk or unconfirmed mass messaging, CAPTCHA/2FA bypass,
a generic MTProto shell, and any Python component in this repository.

## Risks

Broad account credential authority, source privacy, client/API changes and accidental public
disclosure.

## Acceptance

- Synthetic account has an allowed and a forbidden peer; only bounded, in-contract reads succeed; forged
  peer handles, pagination escape, oversize downloads, expired or revoked sessions and forwarded commands
  fail safely and uniformly (no existence oracle).
- The agent filesystem, environment and commands cannot retrieve the account session or its key; `GOPOD_TG_*`
  are absent from every container env allowlist; `data/tgaccount/` is not mountable and not in any backup.
- Group runtimes and task runs get no capability and no route; `/tg` is refused in groups for everyone.
- A write requires a separate explicit request and, by default, the owner's button press on the exact payload;
  content read from messages cannot trigger a send; an unknown send outcome is `uncertain` and is never replayed.
- Live account acceptance is separate explicit authorization.
