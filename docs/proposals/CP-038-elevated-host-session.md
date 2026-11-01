# CP-038 — Elevated host session for the owner's private chat

| | |
|---|---|
| Status | proposed |
| Date | 2026-11-01 |
| Origin | Owner asked for an explicitly started, separately commanded agent session that holds host authority on the Pi, with the provider passed through, available only in the private chat |
| Size | M |
| Touches | `internal/runner` (elevated container profile, policy role, guard fields), `internal/control` (new command), `internal/store` (exec journal), `internal/telegram` (button fence, marker, routing), `internal/config`, new `container/elevated/` images and their build profile, `docs/ISOLATION.md` |
| Depends on | D011, D012, D028, D044, D054; CP-036 Stop precedent; a button-fence fix in `internal/telegram` as a prerequisite. Not CP-037 |
| Spec | [Elevated host session](../superpowers/specs/2026-11-01-elevated-host-session.md) |
| Plan | [Staged delivery](../superpowers/plans/2026-11-01-elevated-host-session.md) |

## Problem

Every agent container gopod spawns is hardened the same way: non-root uid,
`ReadonlyRootfs`, `CapDrop: ALL`, `no-new-privileges`, bind mounts chosen by
trust tier (`internal/runner/docker_args.go:140-230`, mounts chosen in
`internal/runner/mounts.go`). That is
correct for ordinary conversation and for group work, and
`internal/runner/exec_guard.go` pins it by digest under the managed runtime so
the profile cannot drift unnoticed.

It leaves no way to do host work from the chat. Changing a systemd unit,
inspecting a wedged container, editing a file outside the mounted set or
reading the host journal all need authority the chat profile deliberately
denies, and relaxing the chat profile to get it would weaken every ordinary
conversation at the same time.

## Proposal

Add a separately started conversation, bound to one reserved chat folder, that
runs in its own container with host authority, reachable only from the owner's
private chat and only by an explicit confirmed command. The ordinary chat profile is not parameterised
and not relaxed; the elevated profile is a distinct policy role with its own
builder, its own labels and its own digest, and the two can never be
substituted for each other. The session has its own identity, its own provider
context and its own transcript; the ordinary topic is untouched.

## Out of scope

Any change to the hardened chat or group profile. A TTL or idle timer. More
than one elevated session at a time. Group or non-owner access. Scheduled,
proactive or task-driven elevated runs. Remote or multi-host operation. Pi
deployment, which stays separately authorised.

## Risks

The container is equivalent to host root on the Pi, so the usual isolation
reasoning does not apply: the remaining controls are who may start it, what
input may reach it, and what is recorded. The material risk is that text the
agent reads during an elevated session (a web page, a repository file, a mail
body) carries instructions and is acted on with host authority. The design
answers this with an explicit start, a single live session, owner-private
input only and a journal; it does not claim a sandbox. A privileged container
can also exhaust the Pi, so resource caps stay in force as an availability
control.

## Acceptance

- `/root` is unregistered unless its feature flag and an owner chat ID are
  both configured; with either missing the command does not exist.
- Start requires a real owner-private Telegram message and a confirmation
  press; a callback, a group message, an IPC message, a scheduler tick and a
  non-owner chat each fail to start it.
- The elevated container is created with the elevated policy role, and a
  container whose recorded role differs from the requested one is denied rather
  than recycled.
- The container arguments produced for an ordinary turn are byte-identical to
  the pre-change fixture, and no existing container's recorded policy digest
  changes.
- Every exec the host performs into the elevated container has a journal row
  with argv, times and exit code before the chat shows its result, readable
  with `/root log`. The journal does not claim to record the agent's own host
  commands, which never cross the Docker exec boundary.
- The session ends on `/root stop`, on `/restart` in any mode, and on
  controller exit; an elevated container found at boot is removed before any
  other startup work.
- The reserved folder cannot be registered and no scheduled task, proactive
  preset or outbox retry can target it.
- The elevated image is its own image: the chat agent image's package set is
  unchanged, and the elevated image carries the Docker CLI, `nsenter` and the
  host-inspection tools without a second copy of the host's own distribution
  tooling.
