# Elevated profile login rollout — 2026-11-03

## Implemented contract

PR #155 (`500a094`, D062) adds `/root login`, explicit `/login elevated` and
a generation-bound login button for a confirmed owner-private ROOT session.
Ordinary `/login` remains separate. Login output and stdin are private, excluded
from model/persistence/journal payloads. Fixed login/status commands are journaled.
Stop/close/restart use the same execution lease and exit-proof lifecycle.

## Startup failure and correction

The first rollout at 12:26 UTC exposed duplicate `/root` command registration.
The controller panicked; the controller and both provider image tags were promptly
rolled back. Stable `124bc1a` reached readiness at 12:26:57, restart count zero.
The initial seconds-running check was insufficient and is not acceptance evidence.

PR #156 (`474aac0`) registers `/root` once and binds Telegram login afterwards
under the handler mutex. A subprocess regression executes real `run()` with
synthetic Telegram HTTP, missing Docker socket and disposable state. Independent
review, full build/vet/tests, affected race tests and CI passed:
[PR CI](https://github.com/Coriol-is/gopod/actions/runs/41636493608),
[main CI](https://github.com/Coriol-is/gopod/actions/runs/41637352168).
Sonar CLI returned OK for #156 (advisory).

The corrected image `gopod-gopod:login-candidate-4aac936`,
`sha256:8f04c4b4a095021988f5a3c6ddfa982c11b42f0af69303bc73ef741ad114dc5d`,
started at 12:42:52 UTC, ready 12:43:00, restart count zero through live checks.
Binary `4aac936` has identical source tree to squash `474aac0`. It was built with
Go 1.26.10 for linux/arm64, CGO disabled, over the fully built PR155 controller
image; unchanged group helper binaries were retained. A real-image boot probe
on Pi used no network, no production data, no host mounts and synthetic credentials.

Both full provider candidates were built on Pi and promoted:
`gopod-agent-elevated:login-candidate` and
`gopod-agent-elevated-codex:login-candidate`. Pinned native versions: Claude
2.1.310, Codex 0.164.0. Controller flag remains `GOPOD_ELEVATED=1` on rpi4;
default is off.

## Backups and state

- Online `20261103T115950Z-db-a3c9bbeb`: verified 43 tables / 8,448 rows.
- Matched stopped `20261103T122519Z-ms-e5af8b89`: verified 43 / 8,487.
- Matched stopped `20261103T124217Z-ms-977b35d5`: verified 43 / 8,642.
- Controller rollback: `gopod-gopod:rollback-pre-elevated-login-20261103`.
- Provider rollbacks: both elevated repositories, `rollback-pre-login-20261103`.

These backup sets cover the database, declared configuration and artifacts; they
do not cover provider auth homes. No database restore or schema rollback occurred.
Pre-rollout counts: 2 registrations, 38 ordinary session mappings, 15 tasks;
legacy mapping remains unadopted. Scheduled task rows changed during the earlier
long wait, so whole-row hash equality across that interval is not asserted.

## Actual Telegram acceptance and discovered credential loss

Using the owner-authorized telegram-cli workflow in `@Nanoclaw_go_bot`:

- Owner-stopped long Codex/du scan: explicit close 3176164 → 3176165; container
  and observed Codex/du processes disappeared. The host scan was not resumed.
- Claude `/root claude` 3176175, confirmation and login button 3176176,
  private OAuth prompt 3176177. `/stop` 3176178 → 3176179 left only service
  `sleep` in the container. `/root stop` 3176180 → 3176181 removed it.
- Codex start 3176182/3176183; `/login elevated` 3176184 → private device
  prompt 3176186. `/stop` 3176187 → 3176188/3176189 killed the login process;
  only `sleep` remained. The test ROOT session was then explicitly closed.
- No OAuth/device code or private URL was copied into this report or source.
  No browser authorization was completed by the agent.

**Acceptance found a defect:** before the Codex re-login probe, native status was
`Logged in using ChatGPT`; after cancellation it was `Not logged in` and the
profile's auth file was absent. Native Codex clears existing authorization when
starting device login. The acceptance probe therefore lost the owner's earlier
elevated authorization. Ordinary credentials were not touched or copied. Fresh
owner browser confirmation is required; prior auth is not claimed to survive.

PR #157 adds an auth-status preflight under the same lease: already-authenticated
profiles are left intact with an explanatory message; unknown/malformed/unproven
status refuses login. Explicit native text or JSON boolean and supported exit
codes are required. JSON extra text cannot override its boolean. Independent
review PASS with regression coverage for these failure paths.

## Service inventory caveat

At the repeat rollout, eight baseline containers were present: controller, owner,
OneCLI/Postgres and four Honcho services; all six configured healthchecks were
healthy. The separate `codex-agent-pi-agent-assistant-1`, present during earlier
host qualification, was already absent before this rollout. No rollout command
removed it; cause was not established. Do not equate this with the earlier
nine-container/seven-healthcheck inventory or silently recreate that service.

## Final auth-guard rollout

PR #157 merged as `8412e3b` after independent review PASS, full local build/vet/tests,
affected race suites, Sonar CLI OK and
[mandatory PR CI](https://github.com/Coriol-is/gopod/actions/runs/41638721081).
The linux/arm64 Go 1.26.10 binary `f8939b7` has the identical source tree.
Controller image `sha256:d169cdfec900f6ba18dbe6113dd0219edde0a5431ed08ac5bad4728efa902078`
started at **12:52:11 UTC**, reached readiness **12:52:19**, and remained running
with restart count zero/OOM false beyond the first minute. Both provider images
are unchanged from the successful prior rollout. The isolated real-image boot
probe also passed before activation.

Fresh matched-stopped backup `20261103T125136Z-ms-0a8d74ef` verified: 43 tables,
8,689 rows. Before/after integrity, registration/session/task hashes, mapping
mode and pending-adoption state match across both successful rollouts.

Final-version Telegram acceptance: Codex confirmation 3176196/3176197;
`/root login` 3176198 passed native unauthenticated preflight and delivered a
fresh private device prompt 3176200. This login is awaiting the owner's browser
confirmation; the agent did not authorize it. The owner was explicitly notified
of the credential loss and prompted to complete this fresh login. Do not start
another login while it is pending. Its bounded login lifetime is ten minutes;
ROOT itself requires explicit close and has no session TTL.

Exact merged-source [main CI](https://github.com/Coriol-is/gopod/actions/runs/41639154429)
also passed. Native browser completion and a subsequent bounded authenticated
reply remain owner-dependent acceptance gates, not inferred from CI or prompts.
