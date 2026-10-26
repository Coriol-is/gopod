# Scoped Telegram user-account reading

Date: 2026-10-26. CP-028. Status: **proposed design**, not implemented or enabled.
Planning wave: 4 deferred; size estimate: L.

[Proposal](../../proposals/CP-028-telegram-account-reading.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Reading Telegram sources beyond the bot's retained authorized messages may be useful, but a
user's account session can expose far more than the requested channel.

## Dependencies and boundaries

CP-020 credential isolation; CP-027 grants; CP-024 provenance; CP-007 private backups.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- First prefer already retained authorized bot conversation data; adding a user account is an
  optional separate capability, disabled by default. Initial rollout is personal-only and
  read-only.
- Use a trusted broker with a separate persisted user session outside agent mounts. Enforce
  explicit peer/chat allowlists, time windows, pagination, byte/download quotas and purpose on
  every operation; no generic tdl/MTProto shell or arbitrary account API is exposed.
- User login/challenges are owner-private, expiring and cancelable; never echo QR/session
  secrets in groups, ordinary artifacts or logs. Verify current official Telegram and selected
  client authentication/support constraints before implementation; no actual login is
  authorized by this design.
- No send/edit/delete/reaction/contact-management surfaces. Retrieved messages/forwards are
  untrusted data, never owner instructions, approval or confirmed preferences. Provenance
  records account-source scope without disclosing membership/peer metadata to unauthorized
  chats.
- Groups receive no personal account session or implicit read grant. Any later group access
  needs a separately approved narrow broker capability and cannot search all private chats or
  leak that a private peer exists. Cross-publication follows CP-027.

## Acceptance

- Synthetic account has allowed public peer and forbidden private peer; only allowed bounded
  reads succeed.
- Forged peer IDs, pagination escape, download oversize, expired session/challenge and forwarded
  commands fail safely.
- Agent filesystem/env/commands cannot retrieve account credentials; denied group queries
  disclose no private peer metadata.
- No write operation exists in the exposed registry; live account acceptance is separate
  explicit authorization.

## Migration, retention and rollback

Do not reuse/copy the donor's real user session or auto-import history. New session/grants are
created only after operator approval; revoke/delete follows an explicit credential/retention
procedure.

## Risks and decisions before implementation

Broad account credential authority, source privacy, client/API changes and accidental public
disclosure.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

First-wave implementation, account-wide search by groups, sending as the user, CAPTCHA/2FA
bypass or automatic history ingestion.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/tdl-auth.js; docs/telegram-read.md; shared-skill/telegram-read/`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
