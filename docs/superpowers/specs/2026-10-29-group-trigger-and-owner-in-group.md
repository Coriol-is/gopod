# Group reply triggers, owner-in-group identity and group login guidance

Date: 2026-10-29. Status: **requirements draft; not implemented**. Follows the default group admission
change ([D047](../../DECISIONS.md)). Statements marked *(owner)* come from the owner; *(guess)* are proposals.

## Why
*(owner)* Registered groups now run the agent by default. The owner wants to configure when the bot answers in a
group: to every message of the owner, to every message, or only when addressed with `@`. The owner is also the
person who controls the group, but the bot currently treats the owner's group messages as `owner: no`.

## Current state (verified 2026-10-29)
- There is no local trigger gate: every message the bot **receives** in a registered group becomes an agent turn.
  What it receives is decided by Telegram. With privacy mode on (`can_read_all_group_messages=false` per `getMe`)
  the bot gets commands addressed to it, replies to its own messages and explicit mentions; plain text without a
  mention is not delivered. Seeing every message needs privacy off (BotFather, bot re-added) or admin rights,
  which only the owner can do; gopod never changes them (see
  [group mention research](../../research/2026-10-25-group-mention-delivery.md)).
- Ownership is by **chat**: `IsOwner = (chat.ID == ownerChatID)` (`internal/telegram/handler.go`), so the owner's
  private chat is privileged and the owner's messages in a group are `owner: no`. Owner-only commands from a
  group (`/logs`, `/isolation`, `/restart`) are denied with `permission denied … is_owner=false`.
- Group `/login` after the private owner confirmation calls `Runner.Ensure` for the group folder, which always
  returns `ErrProfileUnavailable` for groups, so it fails with "Не удалось запустить вход". Groups have no login
  of their own: the group runtime reads the owner's personal subscription credentials (`AuthFile`, read-only,
  inference-only), so the account is refreshed with the owner's `/provider <name>` + `/login` in the private chat.

## Requirements
### R1 Trigger mode per group
1. Setting `trigger` per registered group with values `owner`, `mention`, `owner+mention`, `all`:
   - `owner`: answer only messages whose sender `user id` equals the owner id;
   - `mention`: answer only messages that mention the bot (`@username` entity), reply to a message of the bot, or
     are a command addressed to it;
   - `owner+mention`: owner messages always, others only when addressed;
   - `all`: every message the bot receives.
2. The gate runs in the message handler **before** a turn is enqueued. A message that does not match is ignored
   silently: no turn, no model call, no quota use, no group notice. It is still stored as ordinary chat history
   *(guess: yes, so later addressed turns have context)*.
3. *(guess)* Defaults: new groups `mention`; the existing group keeps `all` (today's behavior) until the owner
   changes it. The owner decides the default for the existing group (open question 1).
4. The setting persists in SQLite next to the registration (scoped by namespace, survives epoch rotation) and is
   changed by `/trigger [mode]` through `internal/control`. `/trigger` without an argument shows the mode and
   reminds that `all` and `owner` need the bot to actually receive those messages (privacy mode).
5. The agent is told the current mode and that group members other than the owner are untrusted.

### R2 Owner-in-group identity
1. A message is "from the owner" when `m.From.ID == ownerChatID` (the owner's Telegram user id equals their
   private chat id). This identity is used **only** for group-scoped decisions: the `owner` trigger and
   authorization of group-scoped control commands. It never grants the owner tier, personal mounts, personal
   memory or host-level commands.
2. Group-scoped control commands (`/trigger`, `/isolation status|disable|enable|prepare` for this group,
   `/model`, `/provider` for this group) accept the owner by user id. State-changing ones go through the existing
   confirmation flow **in the owner's private chat** (the same approve mechanism as group `/login`). Personal and
   host commands (`/logs`, memory, schedules, `/restart`, `/telegram doctor`, backup) stay private-chat only.
3. Spoofing: authorization is `From.ID` supplied by Telegram for the message in a registered group, never data
   typed by the sender; anonymous admins (`sender_chat`) and forwarded messages are not the owner.

### R3 Group `/login` guidance
Replace the generic failure with a neutral group reply ("sign-in is managed in the owner's private chat") and a
private note to the owner explaining the actual step (`/provider <name>`, then `/login` in the private chat to
refresh the account that groups use). The private confirmation mechanism itself is unchanged.

## Non-goals
Per-member allowlists, per-topic modes, enabling privacy mode changes from the bot, a Guest-bot summon path, any
widening of what a group runtime can reach.

## Open questions
1. Default for the existing group: keep `all` (my default) or switch to `owner+mention`?
2. Which control commands should work from the group for the owner (R2.2 list), and which need private
   confirmation?
3. Should messages that do not match the trigger still be stored as chat context (my default: yes)?
4. Should `owner` mode also answer replies to the bot from other members, or only the owner's own messages?
