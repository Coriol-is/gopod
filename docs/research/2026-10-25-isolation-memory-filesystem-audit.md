# Personal/group memory and filesystem isolation audit

Date: 2026-10-25. Source: `ea38581e95ae214ff2a4c0dd5fc8f7f40b1f7d86`.
Status: read-only audit and implementation input; no source or production changes.
Related service/environment audit is separate. No live credentials, private data,
model requests or production exploit were used.

## Conclusion

The ordinary memory SQL/API path already separates distinct, nonempty chat
folders. A group using its own trusted folder does not automatically receive
owner memory. This is insufficient to promise personal/group isolation:
configured Obsidian vaults are deliberately mounted into every registered chat,
and an agent-writable parent permits replacing the standard memory mount source
with a host-resolved symlink before the next container recreation. Fix filesystem
boundaries before claiming the group cannot retrieve personal information.

## Trust-boundary map and existing protections

| Surface | Authority and current behavior | Evidence |
| --- | --- | --- |
| Automatic memory context | Runner passes current `chatFolder, thread`; every compiler slot filters the exact folder | `internal/runner/runner.go` Run/RunStream; `internal/memory/compiler.go:43,108,128,167` |
| Search | Vector and FTS paths filter `m.chat_folder`; empty folder explicitly means all folders and is a trusted API, not a group request option | `internal/memory/memory.go:154,161,390,438` |
| Memory HTTP API | Random per-exec bearer capability resolves folder/thread. Query/body `chat=owner` does not choose the namespace | `internal/runner/capability.go:39`; `internal/memory/api.go:88-101`; `internal/memory/tokens.go` |
| Memory writes | Manual/API writes use thread 0 of the capability's chat; extraction/summaries retain the turn's folder and thread | `internal/memory/api.go:152`; `cmd/gopod/main.go:244,264`; `internal/memory/extract.go` |
| Thread visibility | Main sees the chat's fact rows; a topic sees main + own. Summary rows are own-conversation only. This is within-chat sharing, not cross-chat sharing | `internal/memory/memory.go` visibleIn; `internal/memory/compiler.go` |
| Commands | `/remember`, `/recall`, `/memory` resolve the calling Telegram chat through registered storage; missing registration does not become empty-folder search | `internal/control/memory_cmds.go`; `cmd/gopod/main.go:349` |
| Native sessions/history | DB session key is folder/thread/provider; continuity queries filter folder/thread. CLI homes use `data/sessions/<folder>/.claude` and `.codex` | `internal/store/sessions.go:15`; `internal/store/continuity.go:29`; `internal/runner/mounts.go:155-160` |
| Workspace/IPC | Group baseline gets its own workspace, scratchpad, IPC and CLI homes. Project checkout and raw SQLite bind are owner-tier only | `internal/runner/mounts.go:164-191,205-212` |
| IPC privilege | Non-owner source folder cannot target another chat or schedule/manage another folder's task. Source namespace, not model JSON claims, is authority | `internal/ipc/authz.go`; `internal/scheduler/ipc_manage.go` |
| Global memory | `_global` is an ordinary explicit folder value; no automatic union with owner/group retrieval was found. No global personal-fact injection path was found | `internal/memory/memory.go:150`; compiler SQL |

These protections depend on a stable trusted namespace and an honest tier. They
are not safe substitutes for filesystem isolation. Owner-to-group deliberate
posting/forwarding is a separate disclosure action, not a retrieval bypass.

## Findings

### High: every registered chat can read the configured personal vault

Location: `internal/runner/mounts.go:194-203` (CWE-200/CWE-862,
OWASP A01 Broken Access Control, LLM02 Sensitive Information Disclosure).

Path: untrusted group request -> TierRegistered agent with Read/Grep/Bash ->
`/workspace/vault` -> entire `Paths.ObsidianVault`, mounted read-only.
Read-only prevents writes; it does not prevent disclosure. The normal group
agent has every capability needed for this path whenever the vault is configured.

The DB ingester stores vault documents under `owner`
(`cmd/gopod/main.go:295`), but this does not protect direct mounted files.
The generated CLAUDE/AGENTS identity sheet explicitly tells non-owner agents to
read/search the operator's notes (`chattmpl/CLAUDE.md.tmpl:100-113`).

Confidence: confirmed code path and local synthetic mount fixture. Actual
production vault configuration/content was not inspected.

Bounded fix: mount the private vault only for the personal profile. Group
profiles get no vault mount or vault instructions. Explicit shared knowledge
must be a separate sanitized directory/dataset granted to named groups, never a
read-only copy of the owner's entire vault.

### High: group-writable parent can redirect a standard host bind mount

Location: `internal/runner/mounts.go:150-151,205-208,275-316` and
`internal/runner/runner.go:207-212` (CWE-59/CWE-61, OWASP A01 Broken Access
Control, LLM02 Sensitive Information Disclosure).

The host source for `/workspace/memory` is `<ChatsDir>/<group>/memory`, while
its parent `<ChatsDir>/<group>` is writable through `/workspace/chat`.
The group agent can rename its own `memory` subdirectory and replace it with an
absolute symlink naming a predictable host owner-memory directory or DataDir.
On subsequent Ensure/recreation:

1. `EnsureChatDirs` accepts the existing symlink to a directory through MkdirAll;
   template seeding can also follow it on the host.
2. `BuildMounts` emits that source without resolving/checking containment.
3. Docker resolves a bind source on the host, outside the container's namespace.
   The new `/workspace/memory` mount can therefore expose owner files or the
   directory containing the complete SQLite database. The emitted mount is RW.

The group needs no Docker socket or host shell. It already controls the source's
parent and can wait for idle recycling/restart. The exact host target must exist;
this precondition is met by normal DataDir/owner-workspace deployments. This is
not an escape merely from reading an ordinary symlink *inside* the container:
the dangerous step is the host/daemon resolving a new mount source.

Confidence: a temporary Go overlay test called the actual EnsureChatDirs and
BuildMounts against synthetic directories, replaced group memory with a symlink
to synthetic DataDir, and confirmed the resulting RW source resolved outside
the group's root. No real container or private files were involved.

Bounded fix: place independently mounted scratchpad data under a host-controlled
parent not writable through another group mount. Reject symlink sources and
unexpected resolved roots before directory seeding and before container mounts.
Do not describe one EvalSymlinks/containment check as race-proof while an active
group agent still controls the source parent. Review all standard mounts for
analogous writable-parent aliases; validation must cover existing mounts/reuse,
not only newly created directories. Treat contaminated historical workspaces as
untrusted and quarantine suspicious paths without following/copying them.

### Medium: folder reassignment/reuse can inherit another chat's private state

Location: `internal/store/registered_chats.go:43-64`,
`internal/control/chats.go` registerHandler; owner-folder special cases in
`internal/ipc/authz.go`.

Registration validates the folder alphabet and the DB enforces UNIQUE among
*current* rows, but an update can change an existing chat's folder. Historical
memory, history, sessions and files remain keyed by the old folder. Reusing the
vacated folder for another chat makes the new chat inherit that history/files.
This requires owner/admin registration actions; an ordinary group participant
cannot call `/register` successfully.

Additionally, no store invariant reserves literal `owner` for IsOwner=true.
Registering a non-owner chat as `owner` when that folder is not occupied grants
owner-*namespace* behavior: memory capability scope and IPC authorization use
that folder even if its runtime tier remains registered. An existing owner row
normally makes the UNIQUE constraint reject this case; this is not an automatic
bypass in the standard already-registered deployment.

Confidence: confirmed registration/namespace code path; no live registration
changes performed.

Bounded fix: reserve owner namespace in trusted registration/storage; reject
ordinary reassignment of established folder identity. Maintain durable folder
ownership/tombstones (including retired identities), or use immutable chat IDs
as security namespace and keep folder names as display/workspace aliases.
Explicit migration must move/check all state together and reject private->group
reuse. Never silently copy owner CLI homes to authenticate a group.

### Design hazard: profile authority currently trusts mutable stored flags

`telegram/handler.go:223` copies registered `IsOwner` into queue items; the
runner chooses TierOwner from it (`handler.go:384-386`). Owner permissions for
commands instead compare configured owner chat ID. Startup explicitly warms the
registered configured-owner JID as TierOwner (`cmd/gopod/startup.go:57-72`).
There is no private-chat-type check on the configured owner identity at these
seams. A configured group as owner, or a wrongly privileged registered row, can
therefore receive personal mounts and memory context. Configuration/storage is
trusted here: no ordinary group-user path to mutate IsOwner was found.

For the new contract, derive an immutable personal/group profile from trusted
chat identity/type plus configured private-owner identity; reject inconsistent
rows/queue replays. Keep group container folder/profile when login interaction
moves to the owner DM. The display/delivery destination must not select memory,
history, CLI home, mounts or runtime tier. TierOwner is currently enum zero;
new group-profile construction should fail closed rather than inherit zero-value
privilege.

### Shared instructions and extras need explicit publication boundaries

- Per-chat CLAUDE.md and AGENTS.md are generated from the same generic template,
  not copied from the owner's actual file (`chattmpl/chattmpl.go:72-80`). The
  memory template contains placeholders, not existing owner facts. No automatic
  global owner-fact/AGENTS import was found.
- The template nevertheless calls every chat the operator's personal assistant
  and says to treat every request as from the operator (`CLAUDE.md.tmpl:17-20`).
  Group profile copy must identify a multi-user group and prohibit inheriting
  personal state. Instructions do not enforce the boundary, but should not
  contradict it. Existing edited identity sheets are not overwritten, so a
  trusted per-turn group policy is needed alongside safe template seeding.
- Every chat receives the same ContainerSkillsDir. Current FreeFeed skill
  advertises reading directs and posting under an assumed configured credential.
  No personal fact contents were found in the static shared skill itself;
  connected-service credentials/authority are audited separately. Give groups
  only explicitly group-safe skills and services.
- Extra mounts with `allowed_chats:["*"]` intentionally match new groups, with
  read-only as the default non-owner downgrade (`mounts.go:220-254`). The deny
  list covers credential-shaped paths but not all private DataDir/ChatsDir/vault
  directories. Explicit operator configuration is a precondition; no wildcard
  extras were assumed present in production. New group profiles should deny
  overlaps/ancestors of protected private/state roots, regardless of RO, and
  require named-group grants for separate shared data. Do not present mount
  validation as preventing disclosure merely because it blocks writes.

## Verification performed without source changes

Existing focused tests passed (count=1):

- memory API forged chat/thread parameters, revoked/missing bearer, foreign-chat
  marker exclusion and topic visibility;
- registered/allowlist mount construction;
- IPC non-owner/cross-chat/symlink authorization fixtures.

Two additional synthetic tests passed using Go `-overlay`, with test source and
overlay map stored only under an OS temporary directory:

- configured vault appears in a TierRegistered mount list;
- group-writable memory path symlink to synthetic DataDir survives a second
  EnsureChatDirs and yields a RW standard memory bind whose real source is
  outside that group's root.

These are counterexamples to the isolation claim, not passing security gates.
There was no Docker mount attempt or actual personal-data read. The checked-in
registered-mount test uses an empty vault configuration, so its success alone
cannot qualify the configured-vault case.

## Required regression fixtures for the bounded fix

1. Seed distinct private/group A/group B facts, pinned rows, summaries, preferences,
   FTS/vector candidates and native histories. Every compiler slot, API search,
   list, /recall and provider handoff excludes private/foreign markers for groups.
2. Forge `chat=owner`, empty namespace, thread overrides, expired tokens and
   canonical command attempts. The trusted namespace never changes; denial
   happens before memory/storage access.
3. Assert full group Docker mount lists with vault configured and wildcard extras:
   no private vault/project/raw DB/owner CLI home or protected ancestor mounts.
   No profile switch on group login retry, owner-DM confirmation or restart replay.
4. Reproduce the memory-parent symlink with DataDir and owner-memory targets;
   fail before host seeding, Docker inspect/create or private file reads. Test
   parent symlinks, mount reuse and concurrent swaps; do not accept a superficial
   path-prefix check (`group` versus `group-other`).
5. Reject non-owner `owner` folder, occupied/retired namespace reuse, and private
   namespace reassignment to a group. Keep all original private state intact.
6. Generate personal/group CLAUDE and AGENTS variants and apply trusted per-turn
   policy to existing workspaces. Group metadata never advertises personal vault,
   directs, account-linked apps or ungranted services.
7. Existing containers must be inspected/reconciled against the new profile on
   rollout; passing new-container tests does not revoke previously granted mounts.

Do not purge private memory, change real chat registrations, disable live tasks
or recreate production group containers during this audit. Root's implementation
plan should define migration, reconciliation, rollback and client acceptance.
