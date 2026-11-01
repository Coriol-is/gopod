# Elevated host session — staged delivery

**Proposal:** [CP-038](../../proposals/CP-038-elevated-host-session.md) ·
**Spec:** [Architecture and contract](../specs/2026-11-01-elevated-host-session.md)

Not a development authorisation. Each slice is a worktree, a PR, an independent
source review and green CI before the next starts. `GOPOD_ELEVATED` stays off
through every slice, so nothing here changes a running deployment.

| Slice | Scope | Depends on | Checks that must pass |
|---|---|---|---|
| E-pre button fence | Populate `MessageID` in `internal/telegram/cmdbuttons.go` `callbackCaller`; test that a press reaches the Router with it | — | A press carries a non-zero `MessageID`; `/restart force` confirmation, `/restart status` and `/restart cancel` are accepted where they are currently rejected; no other caller field changes |
| E0 images | `container/elevated/Dockerfile` and `Dockerfile.codex`, both added to `container/build-profiles.sh`; arm64 build; base pinned by digest; Docker CLI checked against the Pi's daemon | — | Both images build for arm64; `docker`, `nsenter`, `ps`, `ip`, `lsof`, `sqlite3`, `jq` present and runnable; `container/personal` and `container/group` images unchanged |
| E1 profile and guard | `ElevatedSpawnConfig` and its builder; `RoleElevatedHost` in `Valid()` and `roleGrants`; reserved `ScopeRef` constant; role compared before fingerprint in `decideContainer`; `PidMode`/`NetworkMode`/`IpcMode` added to `ContainerFacts`, the inspection and `hardening` as `omitempty`; elevated ensure wired to the Docker facade's `containerEngine`, not to `r.managed` | E0 | Spec B: `BuildContainerArgs` output byte-identical to the pre-change fixture; an existing chat container's recorded digest still matches after the `hardening` change; a role the plan did not request is denied; no `policyRevision` bump anywhere in the diff |
| E2 folder and journal | Reserved `elevated` folder, bootstrap directories, registration rejection, idle-watcher and task-creation exclusions; exec journal table with journal-first ordering, 4 KiB argv cap and redaction | E1 | Registration and task creation refuse the folder by name; the idle watcher leaves the folder alone past the timeout; the intent row precedes the exec and is settled after; a refused journal write refuses the exec |
| E3 command, routing and marker | `/root` with provider picker, `status`, `log`, `stop`; `Confirm` gate; live-session state and modal routing of owner-private messages; host-rendered start, prefix and teardown markers; `/stop` keeps its CP-036 meaning | E-pre, E2 | Spec A, plus: routing reverts on close, a live-session state with no container is closed rather than answered, and the prefix appears on every reply from the session |
| E4 lifecycle | One teardown function with its four callers; boot removal placed between `restart.recycle` and `CleanupLeftovers`; drain and force integration | E3 | Spec C, including a `kill -9` survivor removed without blocking the controller's execution path |
| E5 docs and ADR | `docs/ISOLATION.md`: a trust-profile row and a section for the profile, plus the `§6.1` drift the code comments still cite; `docs/CONTROL.md` command rows; `.env.example` flags in their off state; an ADR for the §1 decisions | E4 | Link and consistency checks; the ADR is a new `D0NN`, not an edit to an existing one |
| E6 host evidence | Real-Docker checks on the Pi: privileged creation through the socket by the non-root controller, host PID and network namespace sharing, `nsenter` into PID 1, teardown, boot removal | E5 | Spec D; separately authorised, and deployment stays a separate owner decision |

Notes.

- E-pre is first because E3 cannot be accepted without it, and because it
  repairs a shipped command on its own. It is worth merging even if the rest of
  this plan is never built.
- E1 is where a mistake is expensive: it touches the file every ordinary turn
  goes through and the digest every existing container is compared against. Its
  first two tests — the byte-identical builder fixture and the unchanged digest
  for an existing container — are written before the new builder exists.
- CP-037 is not a dependency. The reserved folder reuses the folder-keyed
  machinery that already exists rather than anticipating P1's session entity.
  When P1 lands, folding the reserved folder into it is a reconciliation slice
  of its own, and the single teardown function plus the single reserved name are
  what keep that small.
- E6 is evidence, not activation. Enabling the flag on the Pi is a separate
  owner decision, and the owner has deferred Pi deployment until CP-037 is
  complete.
