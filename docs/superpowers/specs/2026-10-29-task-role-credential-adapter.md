# Task-role credential adapter

Date: 2026-10-29. Status: **design for owner choice**; no code, flag or runtime change.
Follows [D044](../../DECISIONS.md) and the PR 3 paragraph on credential persistence in the
[reliability-tasks plan](../plans/2026-10-26-reliability-tasks.md). Items marked *(guess)* are
unverified beliefs about CLI behavior; nothing here ran a real CLI or account.

## 1. Intent

With `GOPOD_MANAGED_TASKS=1` every managed task is denied (`ErrTaskRoleUnqualified`), because no
`TaskRoleQualifier` approves a credential binding for `scheduled_task_personal`. The binding today
is the whole native state directory, which also holds the foreground transcripts. This spec picks a
**task credential adapter**: the way a task container gets inference authentication without
getting the foreground's conversational state, and without breaking the foreground login.

## 2. Goals, non-goals, anti-goals

Goals
- A task can run inference on the owner's existing subscription for both pinned CLIs (Claude 2.1.310,
  Codex 0.164.0) and fails closed, with a typed reason, when auth is missing or stale.
- The task container cannot read foreground transcripts, sessions, memory backups or `.claude.json`.
- A task can never rotate, clobber or delete the foreground credential.
- Binding and policy stay host-derived, fingerprinted and visible after a Docker-inspect round trip.

Non-goals
- A dedicated account, API key, paid fallback or second subscription (needs its own owner/budget decision).
- Making group auth (CP-020) and task auth one mechanism; they may share helpers only.
- Enabling any other task capability (tools, mounts, env) beyond D044 grants.

Anti-goals
- Weakening the qualifier to pass a smoke test; a "temporary" shared-directory binding left on.
- Extracting or replaying OAuth tokens in application-owned API calls (CP-020 rule: native CLI auth only).
- Hashing or logging secret values; putting any credential in git, labels, env names or reports.

## 3. Current state (verified)

- `standardMounts` binds `${DATA_DIR}/sessions/<folder>/.claude` to `/home/node/.claude` and
  `.../.codex` to `/home/node/.codex`, both writable, for every chat and provider
  ([mounts.go](../../../internal/runner/mounts.go)). `/home/node` itself is tmpfs; Claude's
  `.claude.json` lives there and is restored from `.claude/backups/` after a restart
  ([auth.go](../../../internal/runner/auth.go)). Codex keeps rollouts, thread index **and `auth.json`**
  in `~/.codex` (comment in mounts.go).
- Contents of those directories: Claude `projects/*/<session>.jsonl` transcripts, `.active_session`,
  `backups/.claude.json.backup.*`, `skills` (owner tier) and the login state; Codex rollouts, thread
  index and `auth.json` ([provider_claude.go](../../../internal/runner/provider_claude.go)).
  The Claude credential file name inside `.claude` is *(guess: `.credentials.json`)*.
- The task role uses registered-tier mounts but for the **same folder** (owner), so its state-dir
  sources equal the foreground's; `planFor` sets `SharedWithForeground=true`, requires a non-empty
  `Sources` and a non-nil qualifier ([task_managed.go](../../../internal/runner/task_managed.go),
  [exec_policy.go](../../../internal/runner/exec_policy.go)). `CredentialBinding` (provider, kind,
  sorted sources, shared flag) is folded into the `epp1:` fingerprint; the guard compares labels with
  Docker-inspected mounts/env names ([exec_guard.go](../../../internal/runner/exec_guard.go)).
- Task containers have outbound network (`NetworkDisabled: false`,
  [docker_args.go](../../../internal/runner/docker_args.go)). Env forwarded to tasks is empty by D044.
  Tasks read external content (feeds, mail, web), so prompt injection is the baseline threat.
- CP-020 groups: a single **auth-only file** is opened no-follow, must be a regular file, 0600, owned by
  the daemon uid, <=64 KiB, and pinned by a temporary hardlink in a host-only directory, then mounted
  read-only into the trusted inference container only
  ([boundary.go](../../../internal/groupruntime/boundary.go)). The inference container sits on an
  internal network behind the exact-route relay ([isolationproxy](../../../internal/isolationproxy/README.md));
  refresh/login routes are excluded, so "a read-only shared reference must never rotate the owner's
  refresh token"; expiry fails closed until the owner refreshes the source
  ([config.go](../../../internal/groupruntime/config.go), [ISOLATION.md](../../ISOLATION.md) section 4).
- Observed: qualification failed on **expired Claude auth** and passed after the owner re-logged in
  privately ([ISOLATION.md](../../ISOLATION.md), HANDOFF 2026-10-29). So the access path ages out and
  the owner's foreground session is today the only refresher.
- Not verifiable offline, hence *(guess)*: access-token lifetime is hours, not days; the CLI refreshes
  lazily on use and writes the new token set back to the same file, possibly by rename (which would make
  an inode-pinned hardlink stale); refresh tokens are rotated on use, so two writers can invalidate each
  other; the CLI tolerates a credential file without a refresh token and then simply fails on expiry.
  Section 8 turns each into a live gate.

## 4. Options

Common frame: **source of truth** is the foreground state dir, refreshed only by the foreground CLI
(or owner login). "Task home" means the task container's tmpfs `$HOME` with no foreground mounts.

### A. Auth-only read-only file reference (CP-020 style)
Pin the credential file by hardlink, mount it read-only at the CLI's expected path; task `$HOME` is tmpfs.
- Threat delta: task reads a live credential **including the refresh token** *(guess)*. With open egress,
  an injected task can exfiltrate it: durable account takeover, not a one-session loss. Cannot overwrite
  the source (read-only inode). Groups avoid this only because of the relay; tasks have none.
- Refresh/expiry: no refresh in the task; stale inode if the CLI replaces by rename *(guess)*, so a
  directory-level pin or re-pin per attempt is needed. Expired token fails closed.
- Failure modes: CLI errors when it tries to write the read-only credential file *(guess)*; stale token
  after idle foreground; foreground refresh racing the hardlink.
- Claude/Codex: same shape; Codex `auth.json` carries id/access/refresh tokens *(guess)*.
- Size: small (reuse `authReference`, new binding kind, qualifier, ~3 files in `internal/runner` plus config).
- Tests: synthetic file, fake qualifier, Docker-inspect fixture; no accounts.
- Fingerprint: new `Kind`, source = pinned path (stable name), no secret. Inspect round trip unchanged.
- Quota: shared with foreground (one subscription).

### B. Task-only provider home seeded by the host (copy of the credential only)
The host maintains `${DATA_DIR}/task-auth/<folder>/<provider>/` containing only the credential file(s),
written atomically from the source of truth, mounted **read-only as a directory** at a fixed staging path
(`/run/gopod/task-auth`); the task entrypoint copies it into the tmpfs `$HOME` so the CLI can write
locally and lose it at exit. The foreground state mounts are dropped from the task role entirely.
- B1 (full copy): as A for the leak (refresh token present) but no inode staleness, no CLI write errors,
  and a task-side refresh dies with the container (never reaches the source).
- B2 (**access-token-only copy**): the host strips the refresh token when copying *(guess: CLI accepts
  it)*. A leaked file is useful only until access-token expiry. The task cannot refresh, so it cannot
  rotate anything; the host refuses attempts whose remaining lifetime is shorter than the task limit.
- Refresh: host copies on demand before each attempt (under the folder lock), validating the JSON and
  expiry; a torn read of a file the foreground is rewriting is retried. A stale source yields a typed
  `auth_stale` denial (settled not started, owner notice), like group expiry.
- Failure modes: foreground idle past expiry (owner must chat or re-login; optional keepalive, section 7);
  concurrent foreground refresh (retry copy); copy sync bug leaking extra files (allowlist exactly one file).
- Codex/Claude: both supported by the same adapter, per-provider file name and strip rule; the Claude
  `.claude.json` onboarding state is regenerated empty in the task home *(guess: `claude -p` needs no more)*.
- Size: medium. `internal/runner` (binding kind, mounts for task role, entrypoint copy), a small
  `internal/taskauth` (copy/strip/validate, atomic write, no-follow checks), config for source paths.
- Tests: synthetic credential files incl. torn/oversize/symlink/wrong-owner/expired; qualifier unit tests;
  Docker test with a fake CLI that reads the file; no accounts.
- Fingerprint: binding `Kind=task_auth_dir`, `Sources=[task-auth dir]`, `SharedWithForeground=false`; the
  dir path is stable so **content refresh never changes the fingerprint or recycles the container**.
  Mount list gains one RO target; `stateDirSources` must learn the new kind and the "no credential
  binding" check must accept it.
- Quota: shared.

### C. Host credential relay: the task holds no credential
The task container gets a placeholder credential and a proxy path; a host relay (reusing
[isolationproxy](../../../internal/isolationproxy/README.md), which already terminates TLS per lease and
sees the inner request) injects the real `Authorization`/account headers on the exact inference route.
- Threat delta: smallest. Nothing to exfiltrate; the lease capability is per-attempt and revoked at
  settlement; exact route/method/body policy bounds what a leased token can do. A task can still spend
  quota through the route (same for A/B).
- Refresh: the host owns it, so freshness is central, but then the host must either refresh by itself
  (an OAuth call: crosses the CP-020 "no token extraction / no account-service route" line and needs an
  explicit owner decision) or read the foreground's file as in B.
- Failure modes: CLI refuses a placeholder or checks token shape locally *(guess)*; request-envelope
  drift per CLI version (Codex ChatGPT account headers, Claude beta query: the existing exact-route
  qualification burden); relay down fails closed; header injection bugs are a new credential-handling code path.
- Codex/Claude: both need route-specific injection and live qualification; Codex custom-provider auth
  semantics are in [codex-group-native-sse.md](../../research/2026-10-26-codex-group-native-sse.md).
- Size: large. New injection stage in `isolationproxy`, relay/network lifecycle for tasks (as groups have),
  lease wiring in `internal/runner`, placeholder file builder, refresh sync from B, new qualifier.
- Tests: strong offline coverage with a mock upstream (the group probes show the pattern); the live gate
  is the placeholder/envelope compatibility on the Pi.
- Fingerprint: binding `Kind=task_relay_lease`, source = relay identity/route set; the network name and
  routes become fingerprint inputs. Inspect round trip must cover the network mode and proxy env names.
- Quota: shared; per-lease accounting is possible later.

### D. Owner-approved shared-directory binding
Qualify the existing `native_state_dir` binding as-is (qualifier returns nil for the owner folder).
- Threat delta: the task reads **everything** the foreground sees there: all owner transcripts
  (`projects/*.jsonl`, Codex rollouts and thread index), `.claude.json` backups (account metadata, project
  history, MCP config), `.active_session`, the full token set; and, writable, can edit or delete transcripts
  and credentials (prompt-injected persistence into the foreground's context, login destruction).
  D-RO (read-only) removes edits but the CLI then cannot write its own state *(guess: breaks)*.
- Refresh: the task CLI refreshes in place and may rotate the refresh token while the foreground holds
  another copy *(guess)*: the clobber/lockout case. Concurrent foreground and task runs race on the file.
- Failure modes: all of the above plus a task transcript contaminating `--continue`/legacy adoption
  (the reason D044 uses ephemeral runs); `--no-session-persistence` does not stop reads.
- Size: tiny (one qualifier). Tests: trivial; says nothing about safety. Fingerprint: unchanged binding.
- This contradicts the D044 and plan wording ("do not weaken policy merely to pass a smoke test").

## 5. Comparison

| | A ro file ref | B1 copy | B2 access-only copy | C relay | D shared dir |
|---|---|---|---|---|---|
| Foreground transcripts readable | no | no | no | no | yes |
| Refresh token exposed to task | yes *(guess)* | yes | no | no | yes |
| Can corrupt source/rotate refresh | no | no | no | no | yes |
| Survives refresh by rename | no | yes | yes | yes | yes |
| Fails closed on expiry | yes | yes | yes | yes | no, may self-refresh |
| Leak impact (open egress) | durable | durable | until token expiry | none | durable + data |
| Size | S | M | M | L | XS |
| Offline-testable | yes | yes | yes | mostly | trivial |
| Needs CLI tolerance guess | RO write errors | none | missing refresh token | placeholder token | none |

## 6. Recommendation

**Build B2 (access-token-only task auth copy) first, with B1 as the fallback if the CLI will not
accept a stripped file, and keep C as the hardening target; do not ship D.** B2 gives the task
no transcripts, no ability to rotate or clobber the source, and a leak window bounded by access-token
lifetime, using already-proven pieces (CP-020 no-follow/regular-file checks, the existing binding and
qualifier hooks, a stable directory so refreshes never recycle containers), and it is fully testable
with synthetic credentials. A is rejected because tasks, unlike groups, have open egress, so a readable
refresh token is a durable takeover path. C is the better end state but costs a relay lifecycle, a new
header-injection code path and per-CLI envelope qualification for one owner and one subscription; revisit
after B2 is live and if the owner wants tasks with narrower egress anyway. If B2's stripped file fails the
live gate, B1 is acceptable only together with narrowing task egress to the inference routes (reusing the
group relay) *(guess: workable; not designed here)*.

## 7. Design sketch of the recommended path

- New binding kind `task_auth_dir` (`SharedWithForeground=false`). The task-role mount set drops
  `/home/node/.claude` and `/home/node/.codex` and adds the RO staging directory; the qualifier approves
  only that kind, only for the registered task role, and only when the directory contains exactly the
  allowlisted file name per provider.
- Host component (`internal/taskauth`): `Prepare(folder, provider)` under the folder lock opens the source
  no-follow, enforces regular/0600/uid/size limits, parses, checks remaining lifetime against the task
  limit, strips the refresh token, writes via temp file plus rename into the staging dir, never logs
  content. Result: ok, `auth_missing`, `auth_stale`, `auth_invalid`. Denials map to `queue.ErrDenied`
  (settled not started, no retry) with a private owner notice using the existing rate-limited path.
- Entrypoint copy into tmpfs `$HOME` is part of the task command, so the container spec stays inspectable.
- Optional keepalive (separate decision): the host runs a tiny foreground-role exec shortly before a
  scheduled task if the source has less than the task limit left. It spends a few tokens of the shared
  quota and refreshes only through the foreground CLI.
- Quota: tasks and foreground share one subscription. A usage-limit error keeps current quota handling
  (no replay on quota error alone, plan PR 3); this design adds no quota partition.

## 8. Rollout and qualification (live gates, Pi, no flag change until each passes)

Gates, in order, each recorded in a deployment note with commands and outcomes, no credential values:
1. Offline: unit, fixture-inspect and fake-CLI Docker tests green; fingerprint stable across a credential
   content change; qualifier denies every other kind and role.
2. Pi, Claude: with a stripped copy, `claude -p` one turn succeeds ephemerally; the foreground `auth status`
   unchanged afterwards; source file hash (not content) unchanged; no transcript appears in the task home
   after exit. Same for Codex `exec --ephemeral`.
3. Pi, expiry: an expired copy yields `auth_stale` before any container starts; a copy that expires mid-run
   ends as a normal failed attempt, not `execution_unknown` by itself.
4. Pi, concurrency: foreground turn and task attempt overlap; foreground login still works after.
5. Pi, inspect round trip: guard reuse across a restart with the new mount; recycle between roles.
6. One real scheduled task (the 09:00 freefeed digest) as the owner-observed canary.

Flag staging: (0) merge code with managed tasks still off; (1) enable `GOPOD_MANAGED_TASKS=1` for one
provider via a per-provider allowlist in config *(guess: new setting)*, canary schedule only; (2) all
schedules; (3) second provider. Rollback: unset the flag or remove the provider from the allowlist
(tasks return to denied, settled not started); delete `task-auth/` (rebuildable); no foreground state is ever
touched, so no data rollback exists. A failed gate leaves the role disabled, as today.

Stays denied by default: any provider without a passed gate, any folder other than the owner folder
until separately scoped, API-key fallback, task env credentials, refresh/login routes, shared-directory
bindings, and group or channel scopes.

## 9. Open questions for the owner (most important first)

1. Do scheduled tasks need general internet egress (feeds, web, mail), or may task egress be narrowed to
   inference only later? (Yes/no; "yes" makes the stripped-token variant mandatory, not optional.)
2. Do you accept B2 as the first step, with C deferred? (Yes/no; or pick C now and accept a larger build.)
3. If the foreground is idle and the access token has expired, should tasks (a) fail with an owner notice,
   or (b) trigger a small foreground keepalive that spends quota? Pick one.
4. Which provider qualifies first: Claude, Codex, or both together?
5. Is the host-side OAuth refresh (host calls the provider token endpoint itself) acceptable at all, or must
   refresh stay inside the native CLI as in CP-020? (Decides whether C can ever be self-sufficient.)
