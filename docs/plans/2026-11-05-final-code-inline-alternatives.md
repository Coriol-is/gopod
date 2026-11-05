# CP-002: immutable code-document / inline alternatives

Status: proposed implementation contract; owner decision pending.
Base: `a215a28`. Production remains `d58cdd2`.

## Concrete problem

`PrepareFrozenFinalReply` snapshots oversized code, then freezes a document part
and text containing its filename. A snapshot failure already keeps code inline.
A later upload refusal currently leaves the filename note in the frozen text.
Legacy `attachLongCode` instead renders that code inline after upload failure.

A document-to-text alternative is not the photo-to-document transition from
PR #172: its output can contain several messages and changes subsequent prose,
chunk boundaries, first-message reply target, last-message keyboard and edit
eligibility. Rewriting pending payloads would violate D063 and immutable-part
triggers. Sending an extra code message while retaining the filename note would
not implement the existing presentation contract.

## Recommended contract: bounded, fully frozen alternatives

Before dispatch, freeze a document-upload prefix and every possible rendered
text suffix for that prefix's accepted/refused outcomes. Each suffix is generated
using existing `splitLongCode.text`, `finalReplyHTML` and `chunkHTML`, including
its exact reply/edit targets and keyboard. No rendering, preference reads, file
reads, model calls or TTS occurs when selecting a branch or recovering.

Bound preparation to at most four extracted code documents (16 suffix variants)
and 2 MiB of serialized conditional-plan bytes. If the original reply exceeds
that extraction limit or the variant byte bound, prepare the whole reply inline
before any upload, record `code_alternatives_budget_inline`, and retain the usual
32-part limit. Do not truncate code. An over-limit all-inline plan remains held
with an explicit error, as an oversized ordinary plan does today. This fallback
changes large-reply attachment presentation and needs explicit acceptance.

Keep the existing successful presentation byte-for-byte for plans within those
bounds. Snapshot failures are fixed inline in every variant and do not consume
an outcome bit. Mixed outcomes preserve only successfully uploaded filenames;
refused blocks occupy their original position in the final prose. Buffered voice
precedes documents; streaming voice follows the selected text, exactly as today.
Voice is prepared once and referenced by every branch, not synthesized per branch.

## Storage and transaction boundaries

1. Version the final-intent input/plan. Existing ready plans retain their current
   meaning and hash; do not reinterpret them as conditional plans.
2. Persist the complete immutable manifest, upload identities, variant mask,
   exact suffix parts and preparation membership before publication. Hash all
   alternatives, their policy and destinations. Apply explicit byte/count bounds
   at Store validation, not only at the Telegram planner.
3. Publish the upload prefix and conditional manifest atomically with all artifact
   references. A conditional group remains unresolved while suffix selection is
   pending, including after the last upload is acknowledged/refused. Generic
   aggregate/reply-memo code must not report completion at that point.
4. Persist each upload outcome against its existing exact claim/fence. A refusal
   is evidence, never a synthetic successful acknowledgement or remote message ID.
5. Once every upload has an eligible definitive outcome, transactionally choose
   one mask and append that variant's exact immutable suffix parts once. Recheck
   scope/topic admission, expiry, capacity and manifest hash. Bind selection to
   source part hashes and recorded attempt outcomes. Never rewrite an existing
   part or change an already selected branch.
6. The group identity/hash describes the full conditional manifest. Ordinary
   linear plan hash validation must remain unchanged; conditional validation must
   explicitly verify the selected materialized suffix against that manifest.
7. An uncertain or sending upload blocks selection. A lost selection commit ACK
   is resolved by reading the durable selection; it never licenses another upload.
   A crash after selection uses ordinary sending/uncertain recovery per suffix
   part, so already accepted text is never replayed.
8. Classify the group as delivered only when every required upload or its selected
   inline replacement and all other selected parts are acknowledged. Diagnostics
   retain refused upload evidence and report inline recovery separately. Only
   selected acknowledged text/edit parts may set the final reply memo.
9. Retention keeps unresolved manifests/outcomes/artifact references. Terminal
   compaction removes alternative payload bytes under the same retention policy
   as ordinary payloads, retaining branch identity and dedupe evidence. Extend
   backup table/count coverage and qualify populated-schema migration.

## What authorizes inline selection

A positive Telegram document receipt selects the filename-note variant.
An explicit, named document refusal or a deterministic local artifact-unavailable
failure may select inline code, because its exact original bytes are already in
the manifest. Freeze the allowed reason policy with the plan.

Do not use generic 400, authentication/permission errors, thread disappearance,
expired/stale scope, cancellation, 5xx, timeout, malformed responses or lost ACKs
as inline authorization. Select only exact document-error tokens established by
transport tests; additional live tokens require separate qualification. Known-
unsent transient/rate-limit outcomes keep ordinary bounded retry behavior; this
slice does not reinterpret exhaustion as successful delivery. A terminal outcome
outside the eligible policy holds conditional selection for reconciliation.

The same exact chat/topic is used for every branch. No fallback-to-main behavior.

## Alternatives considered

| Design | Consequence |
|---|---|
| Bounded complete suffix variants (recommended) | Preserves exact current presentation for bounded plans; explicitly bounds combinatorial preparation; large plans stay inline. |
| Independently rendered prose/code fragments | Linear storage and unlimited combinations, but splits messages at code boundaries and changes markup/edit/reply behavior even when all uploads succeed. Requires a different presentation contract. |
| Re-render after upload / replace pending text | Violates frozen ready-plan bytes and makes recovery depend on mutable presentation code. Rejected. |
| Send code separately but leave the filename note | Incorrect user-visible claim about an unsent document; does not achieve parity. Rejected. |

## Required validation

- Byte-for-byte direct/frozen presentation for all-success and each refusal mask,
  including two mixed documents, Unicode, chunk boundaries, edits and keyboards.
- Four-document boundary, five-document inline fallback, serialized byte bound,
  all-inline 32-part overflow, and snapshot failure in a mixed plan.
- Single voice generation and correct buffered/streaming order in every branch.
- No inline call for timeout, 5xx, generic 400, auth, thread/scope failure or lost
  upload ACK. Exact refusal/local-failure policy tests through real-library fake HTTP.
- Transaction visible before first suffix HTTP call; rollback/commit-ACK fault
  injection, duplicate/concurrent selection, stale claim/fence and forged routes.
- Restart before outcome, after outcome, after selection, and after each suffix
  acknowledgement; uncertain attempts never replay and corrupt manifests hold.
- Scope/topic deletion, expiry, Stop, quotas, retention, backup and old populated
  schema migration. Group completion/reply memo must reflect selected parts only.
- Full build/vet/test, affected race suites, independent review and required CI.

## Rollout and remaining boundaries

This proposal does not activate the foreground producer, change running Pi code,
perform Telegram sends or enable managed tasks. Preview ownership, late
cancellation and reply-action integration remain separate cutover requirements.

An old binary does not understand an unresolved conditional group. Downgrade must
hold dispatch or first settle all such groups using a compatible binary; never
remove the manifest to make old code run. Backup compatibility is not restore
qualification. An accepted contract requires a new ADR extending D063 before
source implementation begins.
