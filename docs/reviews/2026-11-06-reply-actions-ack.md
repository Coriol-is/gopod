# CP-002/003 acknowledged reply actions

Base `6c20646`, branch `feat/reply-actions-ack`.

## Checkpoint 1: bounded callback read model

More/Back/As file currently use only a process-local reply memo. A durable ACK
sets the turn's reply message but does not populate that memo. This slice reads
the immutable original result only through a fully acknowledged, uncompacted
controller-result delivery and its exact last text/edit action row. Current
registration/topic, source member attempt, message ID and original action markup
must match. Frozen-but-unpublished, partial/uncertain, stopped, bare-keyboard and
compacted results fail closed instead of falling back to stale memory.

Newest-row checks compare durable conversation sequences only. A different known
legacy row is a conservative hold: this slice cannot prove mixed-path ordering.
Unknown older direct rows after restart cannot be reconstructed. Automatic eager
clearing at ACK and global mixed-path newest-row lifecycle remain explicit gates.
No schema or new ADR, foreground producer, runtime flag or deploy is introduced.
Existing durable retention bounds source availability; this may exceed legacy
memoTTL, but compaction ends action authority even while result evidence remains.

Owned files: new store read helper/tests, Telegram action/readthrough helpers and
tests, this report. Topic agent owns lifecycle files; root owns global docs.

## Checkpoint 2: implementation

The consistent store read distinguishes legacy absence from managed ineligibility.
It requires the current member attempt, interactive result, full sent delivery,
last acknowledged text/edit, non-bare keyboard and uncompacted evidence. It
validates the saved registration/topic and checks later acknowledged durable text
by conversation sequence. Callback attribution additionally requires exact
chat/folder/thread/message and the original `mo:<turn>` button, so a coalesced
member cannot borrow a different member's row. Regenerate and Steps use the same
managed authority guard as the rows they are reached from; legacy behavior stays.

Back/As file read the original frozen Markdown instead of the rendered payload.
The source is not cached into a successor's memo. Durable HTTP actions revalidate
inside the existing transport lease, never redirect a gone topic, and do not
retry an ambiguous operation. Stale rows only toast: clearing a reused edit target
could erase a successor's row. Eager clearing is not implemented here.

Tests now cover post-reopen More/Back/As file, exact original bytes/Copy content,
wrong topic/message, pending/uncertain delivery, bare keyboard, compaction,
registration changes, later durable replies and conservative mixed-legacy holds.
No schema, ACK mutation, producer activation or global docs edit was introduced.

## Checkpoint 3: independent review and activation boundaries

Independent review found a pre-dispatch classification bug: a callback superseded
between lookup and lease recheck could falsely latch transport uncertainty without
sending HTTP. Local validation failures now carry the existing known-unsent
ownership marker. A deterministic newer-ACK regression verifies zero dispatch and
no uncertainty latch. Added coverage for compacted successor supersession, plain
fallback ACK, and D065 refused-document inline selection with partial/full suffix
ACK. These cases do not authorize the original failed document as an action row.

Legacy `RepairControls` arbitration is explicitly outside this slice: a durable
final edit that reuses a placeholder can still conflict with pending legacy control
repair, and More/Back do not checkpoint control lifecycle state. Coordinating that
repair with final ACK and callback edits remains a foreground activation gate,
alongside eager row clearing and mixed-path newest-row ordering. Foreground stays
disconnected; this report does not claim those lifecycle gates are closed.

Before review corrections, full build/vet/tests and full store/Telegram race tests
passed. Final combined-head validation is recorded below after merging main.
