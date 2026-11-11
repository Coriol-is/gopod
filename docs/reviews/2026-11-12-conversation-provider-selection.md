# CP-018 scoped provider/model selection

Status: implementation branch; not merged, deployed or natively qualified.
Base: da529a8. Branch: feat/conversation-providers. Parent task authorizes backlog
priorities 1–3; integration follows delivery and topic retirement. Proposed ADR D074
below supersedes only chat-wide selection semantics of D030/D031.

## Implemented contract

- Namespace/epoch-bound preference document, revision-CAS mutations, optional chat
  default and true global inheritance. Initial migration assigns the legacy model
  only to its original provider. Main/thread zero is a real override scope.
- Provider-specific models retain each conversation's choice across provider
  changes. `/model default` explicitly selects the provider default; `inherit`
  removes that conversation/provider model override.
- `/provider` picker reports effective and inherited state. Explicit selections,
  inherit and chat-default changes require confirmation. Confirmation binds
  namespace, epoch, registration revision and preference revision, alongside the
  existing Telegram actor/chat/thread confirmation mechanism. CLI `--yes` remains
  usable for an immediate authorized command.
- Runner selects after its existing per-chat slot, captures provider/model in
  context and persists immutable `(turn_id, attempt)` metadata before execution.
  Sync, stream, legacy task and fresh-maintenance routing use that snapshot.
  Configuration changes wait for the active run and never overwrite the running
  process's provider/model. No parallel execution or CP-037 mode activation.
- Unscoped fresh maintenance uses chat defaults; scoped extraction/compaction uses
  the explicit conversation. Startup/auth use the selected conversation rather
  than the last topic to run. Runtime image reconciliation is under the existing
  slot and quiescence checks; failed allocation preserves both settings and attempt
  evidence. Provider-specific native session storage and continuity remain intact.
- Quota confirmation binds failed attempt and selection revision and changes only
  that conversation's override. Retry keeps original text/media/thread metadata.
- Active Telegram login reservation blocks selection mutations and model starts.
  Login allocation uses the originating topic selection; native quiescence must
  be proven before model work or container recycling. Closing an attachment is
  not treated as process-exit evidence.
- New tables are included in backup inspection/counting. No credentials or prompt
  contents are added to selection metadata.

## Integration boundary

`retireConversationPreferencesQ(ctx, q, ref, thread)` must be called inside CP-016's
RetireDeletedTopic transaction by the topic-retirement integration. It removes only
that topic's provider/model overrides and advances revision. Until that integration
is present, this branch is not independently declared a complete CP-018 release.
Existing topic admission still prevents retired-topic reuse.

The currently paused durable session binding wrapper snapshots turn-keyed metadata
before the runner slot. A conflicting existing durable binding is refused before
native execution and the selection write rolls back. This change does not refactor
or activate that CP-037 path. Actual supported rollout scope is the recorded legacy
mapping with serialized per-chat execution. Any later durable-mapping rollout must
move binding selection under the same slot and pass native acceptance.

Model catalog behavior retains the baseline: fixed Claude aliases and arbitrary
account-supported Codex IDs, plus explicit reset/inherit controls. Obvious foreign
provider families are refused. No model availability is invented or established by
these UI choices; native account rejection remains a real next-turn outcome.

## Verification

Focused tests cover migration/inheritance, zero/nonzero topic isolation,
provider-specific models, stale/forged/registration-revision snapshots, malformed
stored data, immutable per-attempt reuse, conflicting durable bindings, native
command selection, streaming and maintenance scope, slot wait/cancel, allocation
failure, login fence, confirmation controls and scoped quota retry.

Recovery validation passed build/vet/full tests on `3186ba8`, plus focused races
across store, runner, control and Telegram. Follow-up integration review found that
captured Stop/freeze identity must also survive waiting for the runner slot. The
runner now checks captured registration scope and exact freeze/selection batch
membership before persisting attempts or starting native execution. Tests cover
registration revision changes while waiting, mismatched batches, and successful
selection/result composition. Legacy scheduled batches can coalesce: every valid
member now gets the same immutable conversation-zero attempt snapshot; this does
not change the existing last-prompt execution semantics.

The parent records final integrated exact-head build/vet/full-test and race proof. Fake native-command tests do not establish real Docker image alternation,
CLI continuity or Telegram client acceptance. Those remain release gates.

## Proposed ADR D074 — conversation-scoped selection

Keep one native agent at a time per chat. Resolve provider from conversation
override, optional chat default, then configured global default. Resolve model for
that provider only, from conversation override then provider-specific chat default.
Persist settings with namespace/epoch identity and compare revisions on changes.
Capture and persist the actual provider/model per attempt under the existing agent
slot before native execution; never reinterpret an already started attempt using
new settings. Main is a scope, not a wildcard. Chat defaults are explicit commands.
Preserve scope-bound continuity and native sessions across ephemeral image changes.
Unknown process exit prevents recycling. Topic retirement deletes scoped overrides
transactionally. CP-037 durable binding integration and native rollout qualification
remain separately gated; this ADR does not authorize parallel runtimes.
