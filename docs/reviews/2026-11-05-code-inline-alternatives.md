# D065 conditional code-inline implementation

Base `c220d5f`; branch `feat/final-code-fallback`. Contract accepted by owner.

## Implemented checkpoint

Final input v2 freezes at most four code-document uploads and all exact rendered
text suffixes. Existing v1 inputs and ready plans retain their bytes/identity.
Above four candidates or the complete serialized 2 MiB request budget, preparation
keeps all code inline. No truncation; ordinary 32-part bounds still hold. Snapshot
failures remain inline in every branch; voice is prepared once and remains before
buffered replies or after streaming text.

The Store validates all variants, policy version, document ordinals, artifact
membership, part limits and full manifest bytes. `outbox_code_plans` binds the
conditional request to the immutable group hash. Publication reserves the maximum
part count. Prefix uploads and optional trailing voice are inserted and pinned
immediately; the suffix has no dispatchable parts until selection. The trailing
voice uses the reserved final ordinal and cannot pass the virtual suffix fence.

Each eligible refused code upload retains a failed part and its attempt evidence,
not a fabricated remote receipt. Under the controller fence, once outcomes are
known, one transaction chooses the mask and inserts that suffix's immutable parts.
It validates scope, topic, expiry, canonical source parts and manifest hash first.
No network, rendering, workspace read, model or TTS call happens there. Existing
claim/sending/uncertain semantics apply to every selected text part. Generic upload
failure remains a reconciliation hold; cancellation/expiry closes the virtual
suffix without claiming delivery. Later deliveries in that conversation cannot
pass an unresolved conditional suffix.

Only exact library-shaped document 400 tokens `file must be non-empty` and
`file is too big`, deterministic missing/corrupt/quarantined snapshot content, or
unavailable upload capability are eligible. Generic artifact inventory/scope/I/O
errors, cancellation, generic 400/auth/thread errors and ambiguous outcomes do not
select inline. Tokens are fake-HTTP qualified, not live Telegram qualified.

Terminal retention clears alternative payloads from both the manifest and the
published final intent, retaining hashes, selection and ordinary dedupe evidence.
Unresolved manifests and artifact references remain pinned. Backup table counts
include the new table. Old linear zero-target part/plan hashes remain unchanged.

## Review corrections

Initial independent review identified missing canonical claim membership, LIMIT
100 starvation, false completion after prefix-only cancellation, failure paths
incorrectly clearing the virtual hold, and overbroad artifact-error classification.
Fixes validate actual part/delivery/ordinal membership, filter unready/topic-fenced
prefixes before the bounded scan, retain a virtual terminal suffix on cancellation,
retain ineligible-refusal holds, and distinguish deterministic content loss from
inventory/scope/cancellation errors. Regression tests cover these boundaries.

## Verification checkpoint

Focused Store, planner, transport and snapshot tests pass. Coverage includes all
16 masks, mixed two-document real-library HTTP parity, sync/stream voice once,
Unicode, edit-to-multichunk and keyboard/reply targets, four/five-document bound,
2 MiB fallback, all-inline overflow, mixed snapshot failure, transaction rollback,
reopen, concurrent selection, corruption, stale/forged claims, cancel/expiry,
retention, and 101 topics behind a fenced prefix population.

The first full run passed build/vet and all packages except the new 101-topic test:
its fixture hit the production 40-files/hour admission limit. The fixture now
explicitly raises its own limit; production admission policy is unchanged.
Full build/vet/test rerun passed. Full Store/Telegram race suites passed
(26.942s / 57.721s). Independent source review of `652a8ac` found no remaining
blocking defect and repeated focused races (9.816s / 10.581s). Its requested
populated-old-schema regression now covers both ready and published linear intents,
restores the old trigger before reopen and verifies unchanged bytes/hashes,
publication identity and immutability; focused race passed (6.399s). Production
source did not change after the reviewed commit. Exact final-head CI remains the
merge gate in [PR #173](https://github.com/Coriol-is/gopod/pull/173).

Sonar analysis of exact source `652a8ac` at 2026-11-05 11:31:53 UTC: gate OK,
0 bugs, 0 vulnerabilities, 27 code smells. Full open-issue page (27/27) was read
through the CLI API, without switching the active organization. Findings remain
visible: mostly cognitive complexity, plus parameter count/interface naming and
background-context guidance. Detached bounded persistence contexts deliberately
outlive an already-issued HTTP attempt so cancellation cannot discard its outcome;
no findings were accepted/suppressed to obtain a passing gate.

## Boundaries

Foreground freeze/preparation still has no production caller. Preview ownership,
late cancellation and reply-action integration remain open; CP-002 is not complete.
No deployment, Telegram action, provider login or paid generation occurred here.
Production remains `d58cdd2`. A downgrade cannot dispatch unresolved conditional
plans; settle them with compatible code or hold dispatch, never erase their ledger.
