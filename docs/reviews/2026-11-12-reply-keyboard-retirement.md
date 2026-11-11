# CP-002/CP-003: bounded obsolete reply keyboard retirement

Implementation checkpoint after `becd396`; default foreground delivery remains off.

## Scope and authority

Registered owner-private replies with a recorded reply-surface sequence can retire an exact older acknowledged row after a newer row ACK. This also covers the legacy reply path with foreground delivery disabled. Historical messages are not backfilled; group/unregistered cleanup is not expanded.

The store transaction immediately before dispatch rechecks current namespace, registration, source-topic admission, exact physical ownership, newer acknowledged surface and all pending edit/outbox fences. An older logical surface that aliases any later surface's physical message is rejected. A control target must still belong to that surface's turn, be terminal and have its current revision acknowledged.

Retirement authority is explicitly keyboard-only: exact chat/message, empty inline keyboard, canonical wire hash, no final-turn override and no text method. Its immutable surface-to-attempt link is inserted in the same transaction as the existing direct-edit journal. Subsequent generic markup writes to that retired physical target are refused, including the old in-memory row tracker's duplicate clear.

## Outcomes and recovery

Each eligible surface gets at most one HTTP attempt. Exact ACK or typed not-modified settles the journal. Unknown transport/server/wrong-receipt outcomes retain the existing pending edit fence; recovery never resends them. Definite refusal is terminal best effort with no automatic retry. Receipt persistence uses the existing exact in-memory receipt reconciliation; a process crash without a durable receipt remains held.

New ACKs and the control recovery tick discover at most eight candidates with a three-second budget. An in-memory rotating sequence cursor prevents persistently held candidates from starving later rows. Restart resets discovery only; the durable attempt link still prevents HTTP replay. Backup evidence counts include the new table.

Button removal is cosmetic cleanup. Stale callback authorization was already independently revoked by the shared newest-reply ledger; a failed cleanup does not restore callback authority or rerun inference.

## Validation

Focused store/Telegram regressions cover fixed wire hash, wrong method/hash, current row, later physical alias, exact empty markup, unknown server outcome, definite refusal, cursor reset and duplicate legacy generic clear. Full build/vet/test and focused race results are recorded with the committed checkpoint report.

Independent read-only review by the provider agent found no confirmed blocker before final commit.

## Remaining boundaries

No production enablement or real Telegram client acceptance is claimed. Gone-topic durable answers remain saved without sending or model rerun, per accepted D075. Historical rows, group/unregistered coverage and unknown-operation reconciliation remain separate work; no blanket historical message edits are authorized by this checkpoint.
