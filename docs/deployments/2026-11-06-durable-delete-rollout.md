# Durable placeholder deletion rollout — 2026-11-06

## Release

Pi controller deployed `0200892127074d6b48ef5bb9ae62baaf23a2b81d`, replacing
`6820422`, StartedAt `2026-11-06T15:07:27.852789822Z`. #193/D071 journals the
registered-private sync voice wait-placeholder deletion before HTTP. Unknown or
false/nil outcomes block Finished editing and TTS. Exact ACK retires only the
captured control and retains a raw-target tombstone; it never asserts text delivery.
No worker, agent-image, provider, credential, foreground, managed-task, parallel
session or topic-deletion activation. [Review](../reviews/2026-11-06-durable-delete-outcomes.md).

Full build/vet/tests, affected races and independent exact-head review passed.
Required PR CI42238584841 on5ff03d3 and exact-main
[CI42239307009](https://github.com/Coriol-is/gopod/actions/runs/42239307009) passed.
Go1.27.3 CGO-disabled linux/arm64 binary built from exact merged source archive;
running binary matches SHA256
`d19a4a01253f4503ff745518ecf2d914532af432073dabc228f572a54ecf17b5`.
Exact-prior-image overlay passed isolated readiness: no network, credentials or
host mounts, read-only root, capabilities dropped.

## Preservation and acceptance

Ten idle counters zero before stop; eleven after migration, including
`deletes_pending`. Graceful controller stop and idle native runtime checks preceded
matched-stopped backup `20261106T150647Z-ms-e23748f5`: scratch verification PASS,
53 tables/10054 rows/one file. Post-migration online database-only set
`20261106T150818Z-db-12e18380`: scratch verification PASS,54 tables/10085 rows/one file.
This verifies the new schema/allowlist; it does not replace matched artifact coverage.
Full workspace/provider auth/session/IPC restore remains unqualified, preserved in
place. No live restore occurred. Registration/session/task full-row hashes unchanged;
integrity OK, mapping unchanged and pending adoption zero.

Controller healthy, restart0/OOMfalse; owner and six unrelated services healthy.
Read-only deletion-ledger check: exists,zero rows. No deletion fault injection or
live voice-generation acceptance claimed. Telegram retained test topic3176309:
command3177469 and correlated reply3177470 at15:08:16UTC confirm
`gopod 0200892 (go1.27.3)`. Restart notices3177467/3177468 observed.
Version/routing acceptance is not native/visual or full foreground qualification.

Rollback `gopod-rollback:pre-0200892` and all prior backups/images retained.
Zero pending deletions does not make old-binary downgrade safe: ACK tombstones
must also remain enforced. Evidence `/tmp/gopod-release-0200892` on Pi.

## Sonar scope and housekeeping

Owner explicitly excluded tests from Sonar entirely. Added automatic-analysis
`.sonarcloud.properties`, aligned CLI properties and mirrored project exclusions;
Go test/race and mandatory CI unchanged. PR5ff03d3 analysis14:57:45UTC: OK,16 file
components/no tests,22 open smells,no bugs/vulnerabilities. Eleven test findings
closed automatically,none manually suppressed. Historical pre-exclusion ERROR
remains in the review. Main taskAaGxYlh3DnatzzXUJ49A at15:03:06UTC confirms exclusions
in scanner context but fails50k plan limit (57,111 lines); published main baseline
remains2026-10-26. No production code hidden or subscription changed.

Proved exact source PR-file equivalence to squash and main ancestry before removing
clean feature worktree and local/remote `feat/durable-delete-outcomes`. Candidate
readiness container/image alias removed; live and rollback images retained.
Two canonical/session worktrees retained. Docs PR192 branch cleanup follows merge.
Backup root345MiB; no backup deletion, broad prune, NAS or unrelated-service changes.
Protected untracked credential backup remains untouched.

Remaining: group/unregistered compatibility, other non-edit effects, historical
missing provenance and explicit unknown reconciliation, eager cleanup, mixed-path
ordering and integrated foreground qualification. CP-002/CP-016 remain partial.
