# D067 amendment: bind task receipts to submitted bytes

Status: proposed; owner acceptance required before implementation.
Parent: [accepted claim contract](2026-11-05-task-ipc-claim-contract.md).

## Why the accepted contract needs amendment

The claim implementation prevents archival of a replacement, preserves task
journal authority and recovers outstanding work. However, the existing receipt
pathname alone cannot distinguish an old response from a changed same-name request.

A deterministic [diagnostic](../reviews/fixtures/2026-11-05-task-claim-receipt-ambiguity.go.txt)
now demonstrates: publish original success; caller observes/removes it and submits
changed bytes; process exits before Completed persists; recovery publishes the
old success while the changed input remains pending. A later sweep correctly
returns a conflict, but a client may already have accepted the old success.
Exactly one task exists. This is false response attribution, not duplicate mutation.

Checking for a replacement before rewriting the receipt still has a check/write
race. A private staging-file rename can prove publication after process death
while the OS remains alive, but power loss before directory fsync may restore
staging after a caller has observed the receipt. It does not satisfy the current
unrestricted crash/restart contract. Independent review agrees.

## Recommended decision

Add `requestHash`, lowercase hexadecimal SHA-256 of the exact submitted bytes,
to every receipt for a bounded, successfully snapshotted task request. It is
computed by the host from the immutable snapshot, not accepted from request JSON.
This is distinct from the internal journal payload hash, which also binds policy.
The receipt filename and existing fields remain, but safe consumption requires
matching `requestFile` **and** `requestHash` to the caller's original submission.

The bundled scheduling guide/producer example must retain the submitted byte
string, compute its SHA-256, and accept success or conflict only for that hash.
An unmatched receipt is stale evidence; keep waiting for the matching receipt,
never claim success, and never create a different task filename as a blind retry.
An identical-byte replay naturally accepts the original bound result.
A changed-byte replay sees only its matching conflict as authoritative.

Nonregular or oversized inputs may have an unbound diagnostic rejection; they
must never produce `status:ok`. Do not hash an unbounded file or identify a
truncated prefix as the full request. Exact-byte matching includes whitespace
and final newlines, not reserialized JSON object equivalence.

This amends the original promise of unchanged receipt shape/no caller migration:
JSON fields are additive, but consumers must adopt matching for qualified replay.
Older clients that use only status/filename are not accepted for concurrent replay
or crash recovery. Update seeded scheduling documentation and migration notes;
do not silently claim old clients became safe. No automatic task API request
field migration, task mutation or production deployment is authorized by this
proposal alone.

## Alternative

Hold ambiguous receipt publication for explicit operator reconciliation and never
republish automatically. This preserves unversioned clients' protection against
old-success replay but gives up automatic eventual receipt recovery. It also
changes D067 and requires acceptance. The hash-aware consumer is recommended.

## Required regression and rollout gates

- Original success observed, changed replacement submitted, process exit before
  Completed: a hash-aware consumer ignores recovered original success and accepts
  only the changed request's conflict; exactly one task.
- Identical replay after each crash boundary accepts the same bound result.
- Receipt deletion/overwrites, stale hashes, malformed bytes, whitespace/newlines
  and changed policy must not create a false match.
- Exercise the actual bundled consumer example/validation logic, not only host
  JSON fields; a field addition alone does not fix the consumer contract.
- Continue all D067 scope, retention, filesystem, restart and race tests, then
  independent review and required CI. No merge until overall review passes.
- Deployment is separate; existing agent workspaces need updated instructions
  before claiming live receipt-consumer acceptance. No live probe has run.

On approval, append a new ADR extending D067 and implement this amendment in the
existing feature branch. Do not rewrite D067's historical accepted meaning.
