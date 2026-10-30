# Security and logic review, 2026-10-30

Reviewed against `main` = `38f3dcc`. Method: entry-point map (Telegram updates,
callback buttons, groups, `/login`; container IPC files; agent stdout to HTML;
scheduler and proactive; CLI; isolation proxy; media downloads; env), then
seven package-scoped review passes (telegram, control, ipc+artifactfs, runner,
store+queue+scheduler+taskexec+proactive, groupruntime+groupsandbox+isolationproxy,
cmd+config+log+memory+backup), each finding traced entry -> sink and triaged
for attacker control, reachability and blast radius. Every High and most
Medium findings were re-read in source by the coordinator. Machine checks:
`go vet` clean, `staticcheck` style/deprecation only, `govulncheck` (see Info),
`go test -race -short` on control/ipc/telegram/store/queue/runner green.

Trust model used: the owner's private chat is unrestricted by owner decision
and nothing of the form "the owner can do X" is reported; registered groups are
semi-trusted; all agent (LLM) output, IPC file content and metadata, and
third-party content the agent reads are untrusted.

No Critical finding. No path was found where a non-owner obtains `IsOwner`,
reaches a `PermOwnerOnly` handler, or acts on another chat's folder, task,
session or memory through `internal/control`.

## High

### H1. Group detection fails open; a group folder can receive an owner-tier container
CWE-636, ASI03. `internal/runner/group_runtime.go:35-52` (`groupRegistration`
returns `false` on any `ListRegistered` error), `runner.go:214-221`
(`ensureContainer`: not-a-group -> `tier = TierOwner`),
`admission_policy.go:49-73` (`authorizeProfile` accepts a group registration on
the next successful read). One transient `SQLITE_BUSY` (single pooled
connection, `store.go:67`; the SQLite log handler, IPC watcher and scheduler
share it) and a group prompt runs in a container with `store.sqlite` RW, the
vault RW, `ANTHROPIC_API_KEY`, `--dangerously-skip-permissions`, no output
redaction and no `checkGroupInput`. Same check at `runner.go:1122`
(`CheckAuth`, `ClearSession`, `CompactSession`). Trigger is not attacker
controlled but M3 raises its frequency. Confidence: Likely (path confirmed
end to end; needs one failed read followed by a successful one).
Fix: `groupRegistration` returns `(RegisteredChat, bool, error)` and every
caller denies on error; `ensureContainer`, `ensureRole` and `turnRuntime`
require a positive owner check (`JID == ownerJID && IsOwner`, the pattern of
`chat_workspace.go:28-44`); never derive `TierOwner` from a negative check.

### H2. One `uncertain` outbox part freezes a chat's deliveries forever
CWE-703. `internal/store/outbox_dispatch.go:196-201` (`nextCandidateQ` NOT EXISTS
over `pending|sending|uncertain` earlier in the conversation), `:217-228`
(`expireDueQ` resolves `pending` only), `:424-446` (`MarkUncertain`,
`SweepSending` turns every in-flight part uncertain at restart). No command in
`internal/control` or `cmd/gopod` settles an uncertain part; the spec's
operator resend (`docs/superpowers/specs/2026-10-26-telegram-outbox.md:122`) is
not implemented. `GOPOD_OUTBOX_DISPATCHER` is on in production, so one Telegram
timeout or a restart mid-send silently stops all later IPC messages and
reminders for that chat, owner included. Confidence: Confirmed (logic);
whether production already has rows: run
`SELECT count(*) FROM outbox_parts WHERE state='uncertain'`.
Fix: owner-only `/outbox` command through `internal/control` that settles a part
with evidence (`sent:<msg_id>` or `unsent`) and re-aggregates the group; also
exclude parts whose delivery group passed `expires_at` from the NOT EXISTS
predicate.

### H3. Agent-swapped `.processed`/`.failed` symlink makes archiving a no-op; perpetual redelivery
CWE-59, ASI02. `internal/ipc/namespace.go:67-81` (`archiveFile`: `root.MkdirAll`
+ `root.Rename` without NOFOLLOW; `pruneNamespace` and `processChannel` do use
`O_NOFOLLOW`). `data/ipc/<folder>` is mounted RW and the container uid equals
the host uid. `rm -rf .processed && ln -s messages .processed` makes
`rename(x, x)` succeed, the file stays, and the message is re-sent every tick
(1 s), surviving restarts; Telegram per-bot flood limits then degrade the
owner's chat. Same on `tasks/`. Confidence: Confirmed (empirical `os.Root`
repro); for native group namespaces under `shared-namespaces/` needs
verification that the group uid can replace the directory.
Fix: open the destination with `root.OpenFile(O_DIRECTORY|O_NOFOLLOW)`, `Lstat`
and refuse non-directories, rename through `Renameat` on fds, and assert
`root.Lstat(channel/base)` is ENOENT afterwards.

### H4. Unbounded scheduled-task creation from a non-owner folder
CWE-770, LLM06. `internal/scheduler/scheduler.go:261-270` (`ValidateInterval`
floor 1 ms; cron `* * * * *` accepted), `scheduler/ipc_sink.go:26-58` and
`store/tasks.go:46` (no per-folder cap), `ipc/tasks.go:84-89` (N files = N
tasks), `control/tasks.go:40` (`/tasks add` is `PermChatLocal` and itself spends
a model call). The scheduler polls every 60 s but the task count is unbounded,
so a group or a prompt-injected group agent keeps its container in
back-to-back LLM turns on the owner's key and holds one of
`DefaultMaxAgents = 2` slots. Confidence: Confirmed.
Fix: for `!IsOwner` enforce max active tasks per folder (about 10), a minimum
period (about 15 min) for interval and cron, a daily run budget, and notify the
owner chat when a non-owner folder schedules.

## Medium

- **M1. Reply-row and quota-fallback buttons bypass the group trigger gate.**
  CWE-862. `internal/telegram/actions.go:34-88,190-229` (`retryTurn`,
  `regenerate` check `turn.ChatID == q.ChatID` only), `telegram/provider_fallback.go:36-47`,
  `control/provider_fallback.go:26-69` (no `groupMemberDenied`). A member of a
  `trigger=owner` group re-runs the owner's prompt (whose stored `turn.Text`
  carries the host line `[gopod group: ... sender=owner]`) and can switch the
  group's provider. Confirmation tokens are bound to chat, not user
  (`telegram/cmdbuttons.go:38-46,146-166`). Confirmed.
- **M2. `/cmd@bot` from a member passes the trigger gate in every mode.**
  `internal/telegram/group_trigger.go:77-80`; then `/tasks add`, `/compact`,
  `/memory gc` start agent runs or persist schedules. Confirmed.
- **M3. Full group admission plus fd tree walk runs on every stream delta.**
  CWE-400, ASI08. `internal/runner/runner.go:628-636` -> `group_runtime.go:64` ->
  `groupruntime/manager.go:73-102` (about 6 SQL reads and `checkTree` with heal
  deletes per delta; also per `onTool` and `startStream:708`). Source of the
  `SQLITE_BUSY` pressure behind H1. Confirmed.
- **M4. No wall clock on interactive turns; idle watcher cannot see `gopod-native-*`.**
  LLM06, ASI08. `runner.go:290-402,516-734`, `lifecycle.go:202-241` (looks up
  `gopod-<folder>`), `provider_group.go:180-188` (Codex group command has no
  `--max-turns`). A group can hold its slot, lease containers and network until
  gopod restarts. Confirmed.
- **M5. Bot token reaches stderr, the `logs` table and every backup via `url.Error`.**
  CWE-532. `internal/telegram/media.go:88-94`, `voice.go:66-72`:
  `FileDownloadLink` contains `bot<TOKEN>`; the download `url.Error` is logged
  under key `err`, which the key-only redactor (`log/sqlite_handler.go:95-123`)
  does not match; the stderr handler has no redactor at all. Confirmed path;
  trigger needs a failed download (timeout, DNS, TLS).
- **M6. Memory poisoning: dedup `UPDATE` in place and `trySupersede` over pinned/manual rows.**
  CWE-345, ASI06. `internal/memory/memory.go:98-101` (similarity > 0.92
  rewrites content and kind of the existing row, keeping `pinned` and `source`),
  `extract.go:128-168`. Also `memory.go` uses `m.store.DB().ExecContext`
  directly, against the "DB access through `internal/store`" rule. Confirmed.
- **M7. Memory API on `0.0.0.0:9876` with no HTTP timeouts and unbounded shutdown.**
  CWE-400. `docker-compose.yml:73`, `internal/memory/api.go:67-70,106-112,225-227`.
  Auth itself (256-bit per-exec capability) is sound. Confirmed.
- **M8. Group upload import hard-fails with no pruning.**
  CWE-400. `internal/groupsandbox/import.go:70-72,88-93`: more than 128 files or
  more than the 8 MiB snapshot budget in `uploads/` returns `ErrUnavailable` on
  every turn; `Status()` still reports enabled. Three 3 MiB documents from any
  member disable the group until manual host cleanup. Confirmed.
- **M9. Owner's complete native credential (refresh token included) is hardlinked into the group native container.**
  CWE-522, ASI03. `internal/groupruntime/boundary.go:17-49`, `manager.go:187,316-319`.
  Unreachable today (`--tools ""`, relay denies refresh routes) but zero utility
  for full blast radius. Likely. Adjacent to the deferred task credential
  adapter decision; needs the owner's decision on credential format handling.
- **M10. No per-group turn budget and no global concurrent-group ceiling.**
  LLM06. `internal/groupruntime/manager.go:196,210`, `admission.go` (only
  `MaxGroups`). Confirmed design gap.
- **M11. Legacy scheduler fires tasks for folders whose registration was removed.**
  CWE-863. `internal/store/tasks.go:122-136` (`GetDueTasks` has no join on
  `registered_chats`); unregister does not pause tasks. Needs verification
  whether `authorizeProfile` already refuses (probably: logged errors rather
  than runs).

## Low

- `claude ... -p <prompt>` without `--`; a text starting with `-` is parsed as an option when the host prefix is absent (`runner/provider_claude.go:49`, `provider_codex.go:66`, `provider_group.go:178`). Needs verification against the CLI.
- `tg://` allowed in agent-rendered links (`telegramhtml/sanitize.go:127-139`): `tg://user?id=` pings arbitrary users in groups.
- Provider error classification reads the model's stdout (`runner.go:688-694`, `provider_codex.go:129-134`): injected "usage limit" text plus a non-zero exit yields a spurious provider switch offer.
- Unbounded agent stdout/stderr buffers (`runner/docker.go:236-237`, `runner.go:795-816`, `stream_events.go:117-132`).
- `sendChunks` resends a chunk after any non-429, non-thread-gone error, not only a refused keyboard (`telegram/handler.go:759-776`): duplicate replies on transient transport errors.
- Device-flow login suppresses persistence and reply context for every owner message for up to 10 min (`telegram/login.go:510-516`).
- Media without caption during `/login` is fed to the CLI stdin as the OAuth code (`telegram/handler.go:212-219`).
- `GOPOD_OWNER_CHAT_ID` accepts negative ids; `!= 0` vs `> 0` predicates differ (`config/config.go:269-275`, `telegram/handler.go:357,597`, `cmdbuttons.go:178` vs `prepared.go:65`, `cmd/gopod/outbox.go:23`, `runner/admission_policy.go:50`).
- Unregistered chats persist to the store with no cap; one goroutine per update (`telegram/handler.go:58-60,869-916`).
- `truncateStr` cuts bytes, not runes (`control/memory_cmds.go:137-142`); command args echoed as Markdown render attacker-chosen links (`memory_cmds.go:57,85,89`, `tasks.go:80`, `handlers.go:209`).
- SQLite log handler: goroutine per record, no `r.Clone()`, writes after close (`log/sqlite_handler.go:52,74-112`).
- Shutdown order: `blobs.Close()` registered before `dispatcher.Stop` (`cmd/gopod/main.go:731,740-745`).
- `localContextOnly` does not block `file_url` in Responses items (`isolationproxy/policy.go:460-490`); embedded DNS `127.0.0.11` as an exfil channel is not probed by the canary; MCP lease endpoint on `0.0.0.0:9877` (`cmd/gopod/groups.go:50`).
- Every CONNECT runs full `Authorize` including the tree walk (`cmd/gopod-group-relay/main.go:129-173`, `groupsandbox/mcp.go:117`).
- Lease drain budget 10 s shorter than `group_exec` 20 s + cleanup; teardown mid-call sets `group_cleanup_unknown` (`groupruntime/manager.go:129-133`, `groupsandbox/sandbox.go:84,120-126`).
- `Manager.disabled` never written; in-memory `blocked` gate is dead code (`groupruntime/manager.go:52,78-87`).
- `/voice` writable from unregistered chats (`control/handlers.go:155-178`).
- `SpawnConfig.Validate` accepts `UID=0` with `GID != 0` (`runner/docker_args.go:83-85`); `chatFolder` not validated in `BuildMounts`/`EnsureChatDirs`/`ContainerName` (`runner/mounts.go:118-134,279-311`).
- IPC watcher maps grow unbounded (`writeFloor`, `retries`, `receiptStates`; `ipc/artifacts.go:461-471`, `durable_receipts.go:74-79`); no per-folder file budget per tick (`ipc/watcher.go:160-182`); `results/` symlink swap routes receipts into a request channel (`ipc/receipts.go:49-62`).
- Non-owner private folders may choose `taskId` (`ipc/tasks.go:62-64`): existence oracle and squatting of owner hash-derived ids.
- Config fallbacks are silent (`config/config.go:203-206` `Sscanf`; `cmd/gopod/main.go:488-490` `CompactInterval`).
- CI: actions pinned by major tag, `test` job has no `permissions:`, no `govulncheck` (`.github/workflows/ci.yml:12-34`).
- Dead legacy agent path with weaker flags (`runner/auth.go:108-223`).
- Responses `store: true` is forwarded, so group content lands in the owner's ChatGPT history (`isolationproxy/policy.go:158`).

## Info

- `govulncheck`: GO-2026-4887 (Moby AuthZ plugin bypass) and GO-2026-4883 (plugin privilege off-by-one) in `github.com/docker/docker v28.5.2+incompatible`, reached only through `init`; daemon-side, not exploitable through gopod's client use; no upstream fix yet.
- `staticcheck`: deprecated `ReverseProxy.Director`, `ImageInspectWithRaw`, `errdefs.IsNotFound`; error-string style in `cmd/gopod/restart.go`.
- Metrics listener binds `GOPOD_METRICS_ADDR` unauthenticated; document `127.0.0.1:` binding.
- `/help` lists `/provider` and `/model` to group members who are denied them.

## Checked and clean

Router re-checks `Perm` on every dispatch including button presses; `cc/cf/cx/pg`
tokens are 48-bit random, chat-bound, TTL'd, single use; `Caller` is rebuilt
from `CallbackQuery.From`; `isGroupOwner` rejects `sender_chat`, forwards and
bots; `/restart` fence (compose label, hostname, `MessageID` sentinel) is
correct. All SQL in store, queue, scheduler, taskexec, proactive and the log
handler is parameterized. HTML: every text path escapes before tag insertion,
`chunkHTML` never cuts tags or entities, outbox parts are XML-validated. Media
uploads use `os.OpenRoot` + `O_EXCL|O_NOFOLLOW` + `filepath.Base`, gated by
registration; paid Whisper is owner-private only. IPC authz compares canonical
`tg:<id>` strings; `artifactfs` does an openat walk with per-component
`O_NOFOLLOW`, `nlink == 1`, size bound and double `Stable()`; capability tokens
are 24-byte `crypto/rand`, looked up by SHA-256, bound to folder, run and
expiry. Containers: no docker.sock, `CapDrop ALL`, `no-new-privileges`,
read-only rootfs, non-root; group native containers get no workspace, IPC,
store or API key, run on a per-lease internal network with egress only through
the relay (exact host/path/method match, no suffix or regex; resolved
addresses rejected unless global unicast, no re-resolution before dial). TLS
MITM: per-lease P-256 CA from `crypto/rand`, key in the relay's 0600 file, SNI
pinned, upstream verified against system roots, no `InsecureSkipVerify`.
Backup: `O_NOFOLLOW`, 0600/0700, `fs.ValidPath`, atomic rename. Login codes
are never logged and not persisted while a session is live. Memory queries are
always filtered by `chat_folder` and thread visibility; extraction is off for
non-owner scopes.

## Work plan by priority

Each group is one PR on its own branch from `main` (worktree, PR, independent
review, CI, admin squash merge). Groups are chosen so parallel branches touch
disjoint files; `docs/HANDOFF.md` and `ROADMAP.md` are updated by the
coordinator once, after the wave. No Pi deploy is part of this plan.

| # | Group | Findings | Files | Notes |
| --- | --- | --- | --- | --- |
| 1 | runner admission | H1, M3, M4, Low uid=0, Low buffers | `internal/runner/*` | Fail-closed group detection with a regression test that injects a `ListRegistered` error; admission at most once per few seconds; bounded turn ctx; idle watcher by label. Highest risk, ship first. |
| 2 | ipc archive | H3, Low results symlink, Low watcher maps, Low per-tick budget | `internal/ipc/*` | fd-based NOFOLLOW archive with a test that plants the symlink. |
| 3 | task budgets | H4, M11, Low taskId oracle | `internal/scheduler/*`, `internal/store/tasks.go`, `internal/control/tasks.go`, `internal/ipc/tasks.go` | Non-owner caps and minimum period; `GetDueTasks` joins registrations. |
| 4 | outbox settle | H2 | `internal/store/outbox_dispatch.go`, new `internal/control/outbox_cmds.go`, `cmd/gopod/main.go` | Owner-only `/outbox` with the evidence argument; expiry stops gating. Check production rows first. |
| 5 | group gate | M1, M2, Low `tg://`, Low `sendChunks`, Low login lows, Low `truncateStr`, Low Markdown echo | `internal/telegram/*` (not `media.go`, `voice.go`), `internal/control/provider_fallback.go`, `internal/control/memory_cmds.go`, `internal/telegramhtml/` | Gate decision rebuilt for the presser; `groupMemberDenied` in fallback; tokens bound to user in groups. |
| 6 | secrets and logging | M5, Low redactor values, Low log handler goroutines, Low owner id sentinel, Low shutdown order, Low CI pins | `internal/telegram/media.go`, `voice.go`, `internal/log/*`, `internal/config/config.go`, `cmd/gopod/main.go` (logger, shutdown order), `.github/workflows/ci.yml` | Value-based `ReplaceAttr` on the base handler covering both sinks. |
| 7 | memory | M6, M7, store-layer violation | `internal/memory/*`, `docker-compose.yml` | Pinned and manual rows excluded from dedup and supersede; new row + `superseded_by` instead of in-place update; API timeouts and bridge-IP publish. |
| 8 | group runtime | M8, M10, Low dead `disabled`, Low drain budget, Low `file_url` | `internal/groupsandbox/import.go`, `internal/groupruntime/*`, `internal/isolationproxy/policy.go` | Skip oversized uploads instead of failing; per-group turn budget in `Authorize`. |
| deferred | M9 | credential derived copy | `internal/groupruntime/boundary.go` | Needs the owner's decision (format of `.credentials.json`/`auth.json`, relation to the deferred task credential adapter). |

Groups 4 and 6 both touch `cmd/gopod/main.go` in different hunks; the second
to merge rebases. Group 5 and group 6 both touch `internal/telegram/` but
disjoint files.
