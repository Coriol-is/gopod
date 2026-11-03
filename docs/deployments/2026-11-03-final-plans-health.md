# Final plans, topic quiescence and container health — 2026-11-03

## Release and validation

Production source is `a25f22a3b1efccb4390f2528e6081d3557ce38b3`, built for
linux/arm64 with Go 1.26.10 and version `a25f22a`. Previous production was
`e6f0fad`. Owner authorized parallel implementation, review/merge/deploy and
bounded autonomous Telegram acceptance. No host reboot or provider login occurred.

| Stream | Reviewed PR head | Squash merge | Evidence |
|---|---|---|---|
| Healthchecks | `14d40d0` | `067c016`, #165 | [Report](../reviews/2026-11-03-container-healthchecks.md), [matrix](../CONTAINER-HEALTH.md) |
| CP-002 final plans | `149e1d8` | `de8293b`, #166 | [Report](../reviews/2026-11-03-final-publication-plan.md), D063 |
| CP-016 quiescence | `5a7a4da` | `a25f22a`, #167 | [Report](../reviews/2026-11-03-topic-quiescence.md), D064 |

Independent reviews, full build/vet/tests and affected race suites passed.
Final CP-016 full tests and Telegram race passed after login fixes; earlier
combined runner/queue/store races passed. Final source equals reviewed `5a7a4da`
for cmd/internal/container/Dockerfile/module inputs. Required test and Docker
isolation jobs passed on all PRs and on exact
[main CI 41681289877](https://github.com/Coriol-is/gopod/actions/runs/41681289877).

Sonar is advisory: #165 ERROR (security B, S5332 for literal loopback HTTP);
#166 and final #167 OK. The endpoint carries only readiness, requires loopback,
and the client disables proxies/redirects. Findings were not suppressed. Main
Sonar analysis is stale: revision `4e3031a`, 2026-10-26T23:01:37Z, not this release.

## Image preparation and rollout

Nine overlays preserve existing runtime configuration except intended health
configuration and controller health address. Controller and relay binaries were
replaced; native provider binaries and auth homes were preserved. One-shot tools
explicitly have no periodic health process. Original images, rollback/base tags,
worktrees and backups remain. A first raw-image-ID build failed before creating
an image because BuildKit interpreted the ID as a registry name; explicit local
base tags pinned to those same IDs fixed preparation. No live alias changed then.

Controller image: `sha256:bd20a5427066672184a8958c8c7bbd266981221cd0af533852eeff479dd05439`.
Controller binary SHA256: `9b7673dd735ed6480102913c8386a856a36174b76745321dd88428ea1801d7e2`.
Relay binary SHA256: `95e11ef58fb4f2855268efc5fadbdef619483205aadc11604dced5d193663a07`.
Transfers and the running controller binary matched. Isolated exact-image boot
passed without network, host mounts or credentials before production switching.

| Live image alias | New image prefix |
|---|---|
| gopod-gopod:latest | bd20a5427066 |
| gopod-agent:latest | 90a69283712f |
| gopod-agent-codex:latest | 5770a6e1b1b6 |
| gopod-agent-elevated:latest | 06c2c72f19e1 |
| gopod-agent-elevated-codex:latest | c40d4437e148 |
| gopod-agent-group:cp020-v2 | 63be3269ecd6 |
| gopod-agent-group-codex:cp020-v2 | 1496d59066be |
| gopod-group-relay:cp020-observer | c8fefa5a1510 |
| gopod-group-tools:cp020 | 910f52e9a49f |

Idle was checked before and after controller stop. Verified matched-stopped
backup `20261103T171231Z-ms-c88b23ae`: 43 tables, 9,103 rows, four files;
database/config/artifact coverage, not provider homes or workspaces. Those homes
and workspaces were preserved in place. Full-row fingerprints of registered chats,
sessions and scheduled tasks matched across rollout; SQLite integrity was OK.

The migration adds `final_reply_intents` and an immutable outbox reply target.
Older binaries do not understand nonzero reply targets and may refuse new-schema
backup restore. Foreground final-plan production is still disconnected; no new
plans were created during this rollout. Keep the verified pre-migration backup.

## Observed acceptance

At 17:14:23 UTC, controller and Codex owner container were running/healthy, zero
restarts, OOM false. Exact controller StartedAt was
`2026-11-03T17:13:09.183174209Z`; owner StartedAt was
`2026-11-03T17:13:12.498182875Z`, image `5770a6e1b1b6`.
All eight idle counters, including final replies pending, were zero. Six unrelated
service containers remained healthy. Optional group/relay/elevated runtimes were
not activated merely to populate the health matrix; reboot remains unperformed.

Telegram command `3176408` in retained test topic `3176309` received correlated
reply `3176409`: `gopod a25f22a (go1.26.10)`, in the same topic. This proves API
routing/version, not visual rendering or new native/model behavior. No test topic
or existing user data was deleted; no new paid model/TTS call was made.

## Remaining work

- CP-002 stays in progress: foreground producer, conditional failed-code-upload
  fallback, preview ownership, late cancellation and reply-action parity.
- CP-016 stays in progress: durable legacy fallback ownership after restart/cache
  eviction, uncertainty reconciliation, managed cancellation, native login exit
  qualification and confirmed delete/outcome/retirement orchestration. No
  `/thread delete` command is exposed.
- Managed tasks stay disabled. A pre-existing queue stress defect was reproduced
  on unchanged main `572e246`: `TestConcurrentDispatchAndEnqueueRace -count=100`
  failed three times with 8–11/12 settlements. Track separately; the release's
  full local and exact CI suites passed without hiding that evidence.
- Real optional-runtime and separately authorized reboot qualification remain.
