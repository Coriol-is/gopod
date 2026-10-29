# CP-032 — Read Telegram reactions, including custom emoji

**Status:** accepted (design only)
**Date:** 2026-10-29
**Origin:** owner request; explicit graceful behavior without administrator access
**Size:** M
**Touches:** telegram, store, scoped agent context, diagnostics
**Spec:** [Reaction reading](../superpowers/specs/2026-10-29-telegram-reaction-reading.md)

## Problem
Gopod sets progress reactions but does not consume incoming reaction updates.
User feedback, including custom emoji identity, is absent from conversation context.
Private chats and non-admin groups cannot be advertised as readable when the Bot API
requires administrator status for delivery.

## Proposal
Persist scoped reaction changes and anonymous count snapshots, resolve known message
references, preserve custom IDs and optionally enrich metadata. Expose bounded observations
to the next normal turn. Report unavailable/unknown capability without disrupting chat.

## Out of scope
Reaction-triggered actions or runs, account login/MTProto implementation, outbound custom
reactions, sticker media processing, group activation and production deployment.

## Acceptance
- Add/change/remove and replay are correct for emoji and custom IDs.
- Anonymous evidence never creates user attribution or duplicates actor counts.
- Unknown target/topic/scope cannot leak or fabricate context.
- Non-admin groups and private chats report the documented limitation, never false empty data.
- Admin transitions preserve coverage gaps; ordinary chat continues throughout.
- Live transport qualification is separate from fixtures and source implementation.
