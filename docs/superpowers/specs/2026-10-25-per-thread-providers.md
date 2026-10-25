# Provider selection per conversation

Date: 2026-10-25. Status: design only; implementation deferred. CP-018.

## Goal and baseline
Keep Claude in one private-chat topic and Codex in another while preserving
independent sessions/history and the existing bounded cross-provider handoff.
Today history/session ids are keyed by folder/thread/provider, but provider and
model selection are chat-wide (provider:<folder>, model:<folder>). Provider
switching recreates a per-chat provider-specific container; admission serializes
turns of the chat. Workspace and credentials are chat-shared, not per-topic
security boundaries. D030 continuity and confirmed D031 quota fallback exist.
This spec does not authorize parallel agents or a new sandbox runtime.

## Resolution and persistence
Persist a chat default provider and an optional override for each conversation,
including main/thread 0. Resolve override -> chat default -> configured global
default. No override means inherit, not a copied value. Migrating existing chats
creates no overrides and preserves their current effective provider, native
session ids, histories and main-session adoption markers. Thread 0 is a real
conversation key, never a wildcard. Group conversation 0 retains the existing
single-conversation behavior; group forum topic routing is out of scope.

Store model choices by conversation and provider, with an optional per-provider
chat default. Switching provider must not pass a Claude model to Codex or erase
another topic's choice. Migrate the old chat model only to the provider it
belongs to; an incompatible/unavailable model produces a clear choice/reset,
not an invented mapping. Keep role/namespace authorization derived from chat
registration, not from user-supplied folder/runtime ids.

## Command semantics
- /provider shows the current conversation's effective provider, whether it is
  inherited or overridden, and the chat default, with picker buttons.
- /provider claude (or codex) sets an override for this conversation only,
  including main. Confirm the exact topic/scope before changing it.
- /provider inherit removes this topic's override after confirmation.
- /provider default <name> explicitly changes the chat default. Explain that
  inherited conversations are affected, but overrides remain unchanged.
- /model follows the current conversation/provider and shows inherited versus
  explicit model state. Its UI must never silently mutate all other topics.

This intentionally changes the old chat-wide meaning of /provider <name>;
announce the new scope in help/confirmation and retain an explicit default
command. Main has the same override semantics as other conversations. Treat
provider registry names and reserved subcommands unambiguously.

/clear clears conversation context across providers as today, preserving its
provider/model preference. Topic retirement (CP-016) invalidates that topic's
override/models as well as resumability. Preferences alone must never resurrect
a deleted topic. Scheduled tasks run in conversation 0 and resolve its effective provider,
including a main override. Maintenance must receive an explicit conversation
when scoped; otherwise it uses the documented chat default. IPC message
delivery does not invoke a provider. None may inherit whichever topic ran last.

## Execution and concurrency
Audit every provider lookup (runner sync/stream/fresh calls, ensure/container
creation, model/auth menus, retries, Stop, compaction and queue recovery).
Capture the effective provider/model under the chat execution lock when an
attempt starts, persist the attempted provider on the durable turn, and use
that immutable snapshot for classification and completion. Queue rows created
before a selection change resolve when their attempt begins; an already-started
attempt keeps its provider. Persisted interrupted attempts recover consistently;
an explicit confirmed fallback is a new attempt, not reinterpretation of the
source failure. Never swap a chat-global mutable provider while another turn
is using it.

Keep one agent at a time per chat in this phase. If alternating topics require
different provider images, safely recycle only the ephemeral container under
existing lifecycle/admission locks. Durable provider-specific credentials and
session storage survive; reusing the correct native session is verified before
resume. A failed allocation/switch must not lose a turn, silently change another
override or rerun an already answered turn. Provider selections are durable
data, not container ids. Implementing persistent containers per provider or
parallel execution is a separate runtime/performance decision (CP-011).

## Quota confirmation and history
An allowance rejection offers Switch and continue / Cancel in the failed
conversation. The token binds actor/chat/folder/thread, failed turn/attempt,
actual source provider, proposed target and relevant setting revision. Recheck
all of them under locks; stale/foreign/deleted confirmations do nothing.
Confirmation sets an override only for that conversation (even if it previously
inherited); it never changes the chat default or another topic. Cancel changes
nothing. Retry retains original text/media/thread metadata; a retry's own quota
failure does not create a switching loop. D030 bounded history handoff stays
thread-scoped, explicitly truncated and independent of the old provider's quota.
Changing a default does not synthesize provider summaries for inactive topics.

## Delivery slices and acceptance
First add schema/resolver/migration and snapshot-based attempt routing; then
scoped picker/model commands and confirmed fallback; finally qualify alternating
images and recovery on the real Pi. Proposed changes supersede only the
chat-wide selection aspect of D030/D031 and need the corresponding ADR update
when implemented. No behavior changes ship with this document.

Test inheritance/default/override precedence, main versus nonzero topics,
provider-specific model migration, two-topic Claude/Codex alternation, session
resume after container recreation, queue/start selection races, failed image
auth/allocation, stale confirmations/default changes, Cancel, quota retry metadata,
clear/deletion and crash recovery. Cross-topic history or model leakage fails
acceptance. Real provider/image smoke tests must use explicit harmless prompts;
unit tests alone do not prove native CLI continuity or parallel safety.
