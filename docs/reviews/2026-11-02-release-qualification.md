# Release qualification — read-only checkpoint, 2026-11-02

## Scope and result

Recovered owner request: backlog streams 1–3 in parallel; this is stream 1,
release qualification. This checkpoint verifies the operational baseline, not
real-client acceptance. No Telegram messages, provider requests, feature changes,
restarts, adoption, schedules or destructive tests were performed.

At 17:56 UTC the Pi runs the expected CP-040 release. All A01–A25 client gates
retain their existing partial/open status. This report corrects the stale
ACCEPTANCE deployment header and A22's obsolete “not deployed” label.

## Current evidence

Read-only SSH used the already trusted Tailscale address with
`BatchMode=yes`, `StrictHostKeyChecking=yes`; host checking was not disabled
and no SSH configuration or known-host entry was changed. The previous `rpi4`
hostname failure was not evidence of an expired or unreachable node: the
recorded address connected successfully.

- Pi checkout: `8a4f55a`.
- Controller image: `sha256:98c0f5eb14cb9e16cd5507181100fe6ca91e0b24880280155b3079a95f2e482f`.
- Started: `2026-11-02T10:17:49.65126974Z`; running, restart count 0, OOM false.
- Startup log: version `8a4f55a`, session mapping `legacy`, outbox dispatcher
  running, graceful startup notice with zero replayed turns.
- Container skills source and checkout match (`diff -rq`, no differences).
  The deployment's explicit sync requirement remains; no mount was changed.
- Host memory: 3,796 MiB total, 1,727 MiB available; swap 1,943 MiB used.
  This is a single idle snapshot, not native-agent peak/admission acceptance A16.
- SQLite opened using `mode=ro` and `query_only=on`: 241 turns, all `done`;
  scheduled tasks: 11 active, 3 done, 1 paused; 46 task run log rows marked
  success. These counters do not prove recurring digest delivery A06.
- No gopod agent container was running at the inventory time. This is not a
  native-provider readiness test. Unrelated service containers were untouched.

## Observed defects requiring follow-up

1. At 16:01 and 17:02 UTC, memory text search logged
   `sqlite3: SQL logic error: no such column: at` and fell back to vector search.
   `SanitizeMemoryFTS` in `internal/store/memory_queries.go` wraps tokens in
   double quotes without escaping embedded quotes. A synthetic local SQLite
   FTS5 query reproduces the exact error: input `x"at:y"z` becomes
   `"x"at:y"z"`. Ordinary `at` and quoted `at:home` queries succeed against
   the live FTS index. This supports a query-sanitization defect, not a missing
   `memories.at` column or a schema migration requirement. The actual private
   triggering text was not retrieved. No production query/data was modified.
   Follow-up: escape interior quotes and cover an actual store query regression.
2. At 17:01 UTC, extraction failed with `runner: agent profile unavailable;
   shared-chat isolation is not yet qualified` for the owner folder.
   The admission policy refuses unresolved/ambiguous/unqualified registrations;
   this checkpoint did not retrieve identities or infer which predicate failed.
   Follow-up: inspect sanitized registration/admission facts before changing policy.

## Remaining qualification plan

| Gates | Concrete next evidence | Boundary |
| --- | --- | --- |
| A01/A11/A18, sessions status | Owner-private command responses, inline-button render/edit and scoped callbacks | Requires explicitly authorized Telegram sends and real-client observation; no model needed for status |
| A02–A05/A08–A10/A14/A15/A17 | Harmless native turns on each provider, continuity, Stop/process proof and memory/compaction behavior | Provider calls and scoped state changes; unit tests cannot close these rows |
| A06 | Correlate an existing authorized occurrence, receipt and delivered Telegram message | No new/resumed schedule just to close acceptance |
| A07 | Actual user media and visible output/provenance | Existing owner voice baseline stays partial |
| A12/A13/A19 | Controlled recovery and startup with before/after preservation evidence | Crash/restart requires bounded operator authorization |
| A16 | Native CLI peak resource observation and admission result | Do not apply Pi profile or reboot as part of this audit |
| A20 | Owner group login redirect and private client delivery | No credentials/codes in reports; requires live client scenario |
| A21 | Silent picker, tool progress, final answer, Stop and long edit-path output | Temporarily changes chat flag; restore only within authorized test |
| A22/A23 | Fresh Claude and Codex PDF/photo/document turns, receipts and client delivery | Native calls and Telegram attachments; does not require adoption |
| A24 | Bounded invalid path and oversized fixture, refusal receipt and notice | Test workspace fixture and sends required |
| A25 | Artifacts disabled refusal and subsequent restoration | Production flag/restart is separately controlled; not attempted |

No gate is marked passed from startup logs, successful task rows, or source
inspection. `/sessions adopt`, CP-037 native resume after adoption, the RPi
profile and CP-033 P03 scheduled creation remain unexecuted.
