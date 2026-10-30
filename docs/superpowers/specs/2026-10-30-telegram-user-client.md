# Telegram user-account connector (read and write), Go

Date: 2026-10-30. CP-028 (re-scoped). Status: **design for owner acceptance**; no code, flag, credential
or account login is authorized by this document. Items marked **TO VERIFY** are beliefs not checked here.

[Proposal](../../proposals/CP-028-telegram-account-reading.md) ·
supersedes the read-only [2026-10-26 design](2026-10-26-telegram-account-reading.md) ·
[shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[scoped approvals, CP-027](2026-10-26-scoped-action-approvals.md).

## 1. Outcome and gap

The owner wants the assistant to work with the owner's own Telegram account (a real user session, not
the bot): read dialogs, history, search and media, and also send, reply, edit, delete, forward, mark read
and react. Today gopod sees only what the bot receives (`go-telegram/bot`, one channel, `go.mod:8`); the
bot cannot read channels or private dialogs it is not in, and cannot act as the owner.

Owner decisions (2026-10-30), recorded here and in CP-028:
1. **Port to Go** with `gotd/td`; the owner's Python projects `telegram-cli` and `telegram-mcp` are only a map of
   which MTProto calls matter, not code to ship.
2. **Read and write**, with a per-class policy proposed in section 8.
3. **Owner's private chat only.** Never in groups, never reachable from group runtimes, and the session never
   enters any agent container or mount.

## 2. Goals, non-goals

Goals
- A host-side broker inside the gopod process owns the one user session and exposes a narrow, versioned JSON
  tool API to the owner-chat agent (section 5). The agent holds a short-lived capability, never the session.
- Owner-only, private-chat-only control surface `/tg` (login, logout, status, policy, pending actions).
- Reads are bounded (pagination, byte and rate budgets). Writes are explicit, auditable, never replayed after
  an unknown outcome, and shown to the owner before they happen by default.
- Message content is untrusted data and can never cause a write by itself.
- Disabled by default; fails closed on any missing secret or unverified isolation.

Non-goals
- Account administration: join/leave, create channel or group, invite, ban, promote, block, contacts import or
  delete, profile or privacy changes, session management of other devices, takeout/export. Not exposed in the
  first release (section 8, class `admin`).
- Any group-chat or shared-chat use, bot-to-bot, automatic history ingestion into memory, a generic MTProto shell.
- CAPTCHA/2FA bypass, phone-number login through the bot chat, scraping other users, bulk messaging.

## 3. Verified current state

Source read in this repository (code wins over earlier notes):
- Telegram library is only `go-telegram/bot v1.27.0`; no MTProto library (`go.mod:8`). Go 1.26 (`go.mod:3`).
- **D012** says gopod never opens, parses or writes a file containing secret values (`docs/DECISIONS.md`, D012
  decision item 1). A persisted user session is exactly such a file, so it needs a refining ADR (section 4.2).
- Control: `PermOwnerOnly` is the owner chat or CLI or an authorized socket (`internal/control/control.go:39-45`);
  `/restart` shows the private-only pattern (`internal/control/restart.go:8-14`); confirmations bind to the
  command name and args and re-dispatch through the Router (`internal/control/confirm.go:8-24`); pickers follow
  `docs/CONTROL.md` section 6.1.
- Credential-bearing messages to the owner already have a no-fallback path (`internal/telegram/login_handoff.go:79-91`,
  `sendPrivateLoginText`) and the login flow has a 10-minute timeout and cancel (`internal/telegram/login.go:21`).
- A per-exec capability pattern exists: the memory API mints a bearer capability per agent exec, kept in memory,
  revoked at exec end, with a name avoiding KEY/SECRET/TOKEN (`internal/memory/tokens.go:10-19`,
  `internal/memory/api.go:82-98`); artifact capabilities bind folder, destination scope, owner and expiry
  (`internal/ipc/capabilities.go:1-60`). The memory API is "reachable from every agent container over the Docker
  bridge" (`tokens.go` comment), so a new broker listener needs its own reachability proof for groups (section 11).
- Containers receive only an env allowlist for the owner tier and nothing for restricted tiers
  (`internal/runner/admission_policy.go:76-83`), so `GOPOD_TG_*` variables are not forwarded unless someone adds them.
- Protected data roots (never an output root) are listed at `internal/ipc/capabilities.go:69` (`protectedDataNames`);
  sessions mount under `data/sessions/<folder>/{.claude,.codex}` (`internal/runner/mounts.go:154-161`); the mount
  deny list is `internal/runner/mountsec/blocked.go`.
- Backups: `internal/backup/manifest.go:115-121` already lists provider auth homes and env secrets as not covered.
- Existing CP-028 is `proposed`, read-only, "no send/edit/delete/reaction" (`CP-028` Out of scope), so this
  document supersedes its write ban by owner decision while keeping its security acceptance.
- Config flags are env based (`internal/config/config.go:220-224`); new feature stays behind one.

External facts checked in a scratch module (nothing in the repo changed):
- `github.com/gotd/td` v0.163.0, **MIT**, `go 1.25.0`. A program importing `telegram`, `session`,
  `auth/qrlogin`, `query`, `message`, `downloader`, `uploader`, `peers`, `takeout` builds with
  `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` (stripped binary of the probe: about 4.1 MB; the real impact on the
  gopod binary is likely a few MB more, not measured). Transitive modules for that import set: 24, among them
  `golang.org/x/{crypto,net,sync,sys}`, `go.opentelemetry.io/otel`, `klauspost/compress`, `coder/websocket`,
  `refraction-networking/utls`, `rsc.io/qr` (a pure-Go QR encoder usable for the login QR). `go list` shows cgo only
  for `runtime/cgo` and `net`, which are standard-library build tags, so the pure-Go build holds.
- Present in the module: QR login (`telegram/auth/qrlogin`, `Token.URL()` gives `tg://login?token=...`), phone-code and
  2FA password calls (`telegram/auth`, `Client.Password`), `session.FileStorage` (writes mode 0600, plaintext) and a
  `SessionStorage` interface (`telegram/options.go`), `Middlewares` and `tgerr.AsFloodWait`/`tgerr.FloodWait`
  (`tgerr/flood_wait.go`), a downloader, an uploader, a high-level message sender with random ids, `telegram/query`
  (dialogs and messages iterators), `peers`, a `takeout` package, raw calls for forum topics and reactions
  (`MessagesGetForumTopics`, `MessagesSendReaction`), and an in-memory test server `tgtest` and `tgmock`.
- Not in gotd itself: an automatic flood-wait waiting middleware (only the helpers; the waiter is in the separate
  `gotd/contrib` module, which we do not add: about 40 lines of our own middleware is enough, section 9).
- **TO VERIFY** before PR 1: that `go.sum` growth is acceptable to the project, the exact set of `x/` modules that
  enter `go.mod`, and licenses of the 24 transitive modules (the common ones are BSD/MIT/Apache; none checked here).

## 4. Why a Go port, and the session decision

Facts from the review of the owner's Python projects (read 2026-10-30, commits `dbad45d` and `64aaf47`):
- `telegram-cli` is a thin Click shell (32 commands) over `telegram-mcp` functions; its output is plain text, errors
  are strings, there is no JSON, and it has no tests. `telegram-mcp` has 83 tools (47 writes), Telethon, no read-only
  mode, no peer allowlist, no audit, no confirmation, no flood-wait handling, a `download_media` that accepts any
  path, and a REST wrapper on port 8001 without authentication. None of CP-028's acceptance bullets is met.
- gopod is Go-only inside this repo (CLAUDE.md "What to never do"); a Python subprocess or image would make the
  session live in a second runtime with its own supply chain, and the useful Telethon surface is about 100 lines
  of calls that gotd covers.
So: port to Go; use the Python tool list only as a checklist of operations (section 6 and 7).

### 4.1 Package layout (needs an ADR per CLAUDE.md "Layout")

```
internal/tgaccount/     client.go     one gotd client, run loop, reconnect, DC migration
                        session.go    encrypted SessionStorage (4.2)
                        login.go      QR login, password step, status
                        peers.go      peer cache and opaque handles (5.2)
                        read.go       dialogs, history, search, topics, pinned, reactions, media metadata
                        media.go      downloads to the sandbox directory with limits
                        write.go      send, reply, edit, delete, forward, mark read, react
                        actions.go    prepare -> confirm -> commit state machine, uncertain handling
                        policy.go     per-class mode, taint downgrade, budgets
                        floodwait.go  middleware, circuit breaker
                        api.go        HTTP broker, capabilities (tokens.go)
                        audit.go      content-free audit records
internal/store/tgaccount.go           tables tg_actions, tg_policy, tg_audit, tg_settings
internal/control/tg.go                the /tg command
cmd/gopod/tgaccount.go                wiring behind GOPOD_TG_ACCOUNT, CLI `gopod tg password`
container/skills/telegram-account/    agent guide (SKILL.md) + embedded guide for Codex (section 5.4)
```
DB access stays in `internal/store` (CLAUDE.md "Code"); no raw `database/sql` elsewhere.

### 4.2 ADR D052 (proposed, to be added in PR 1): the user session is an encrypted file under `data/`

D012 forbids gopod from writing secret files. The session is an MTProto auth key: whoever holds it is the account.
Persisting it is unavoidable (otherwise a QR login after every restart). Decision to propose:
- File `data/tgaccount/session.enc`, directory mode 0700, file mode 0600, written atomically (temp file, fsync,
  rename). Contents are **ciphertext only**: AES-256-GCM, versioned header, key from env `GOPOD_TG_SESSION_KEY`
  (32 random bytes, base64), additional data = the fixed string `gopod-tg-session-v1`. The secret that unlocks
  it stays in the environment, so D012's "source of truth is the process environment" still holds; only an
  encrypted blob is added on disk. Implemented as a custom `telegram.SessionStorage`.
- If `GOPOD_TG_SESSION_KEY` is missing or the file does not decrypt, the connector refuses to start and the
  login command says why (fail closed). No plaintext mode is offered.
- Never in `store.sqlite`, never in a bind-mounted directory, never in logs (the slog redactor already strips names
  matching `token|key|secret|password|cookie|auth`; do not rely on it: the session bytes are never passed to slog).
- Backups do **not** include it (section 4.3). Restore means a fresh QR login.
- Residual risk, stated honestly: anyone who can read the gopod process environment plus the file can use the
  account; host root can. The design removes the agent, the containers and the backups from that set.

### 4.3 Backup treatment (CP-007)

Add `telegram_account_session` to `notCovered()` (`internal/backup/manifest.go:115`) so every set records and
`verify` prints that the session is excluded, and add a parity test that `data/tgaccount/` is never copied by
`create`. The three new store tables are database content and are covered like any table (they hold hashes and
state, no message text after the retention in 12). `internal/store/backup_counts.go` must list each new table
(the parity test fails otherwise, as learned in CP-033 PR 1).

## 5. Architecture

```
owner private chat (Telegram) ──bot── gopod controller ─────────────── Telegram MTProto (user session)
   /tg commands, confirmations          │  internal/tgaccount (broker) ── gotd client
                                        │        ▲        data/tgaccount/session.enc (ciphertext)
                      per-exec bearer   │        │ HTTP JSON on a dedicated listener
                      capability (env)  │        │
                         owner container (agent) ┘   (personal tier only; groups: no capability, no route)
```

### 5.1 How the agent calls it

A dedicated HTTP listener (`GOPOD_TG_BROKER_ADDR`, default the Docker bridge address, separate from the memory API
so that it can be bound and firewalled independently). The runner mints a per-exec capability **only for the
owner tier personal container** and passes it as `GOPOD_TG_CAPABILITY` (the name avoids KEY/SECRET/TOKEN for the
same Codex reason as `GOPOD_MEMORY_CAPABILITY`, `internal/memory/tokens.go:15-19`). The capability maps to the
owner folder and run reference and lives in memory until the exec ends or one hour passes. Authorization derives
only from the capability, never from request fields (CLAUDE.md "Per-chat isolation by path").

The agent uses it through a skill that wraps `curl` calls (same shape as the memory skill) and Codex gets the same
text through the existing policy route (`SchedulingPolicy` is appended to both providers' prompts per the CP-033
spec section 3; a short `TelegramAccountPolicy` paragraph follows that pattern and points at an embedded guide
file seeded into the workspace).

### 5.2 Peer handles

The agent never supplies an `access_hash` or a raw id for authority. Handles are opaque strings issued by the
broker (`u:<id>`, `c:<id>`, `ch:<id>`), valid only for ids the broker has seen in this account's dialogs or
username resolution; the broker keeps the hashes in an in-memory peer cache (gotd `peers`). Unknown or revoked
handles and peers the account cannot access all return the same `not_found` (no existence oracle).

### 5.3 Output contract (stable, versioned)

Every response is JSON with `Content-Type: application/json`:
```json
{
  "v": 1,
  "data": { },
  "next_cursor": "opaque-or-null",
  "untrusted_content": true,
  "provenance": {"source": "telegram_account", "account": "a1b2c3d4", "fetched_at": "2026-10-30T10:00:00Z"},
  "budget": {"bytes_left_run": 458752, "reads_left_minute": 100}
}
```
`account` is a short hash of the account id, not the id. Errors: `{"v":1,"error":{"code":"not_found|rate_limited|budget|
policy|pending_confirmation|uncertain|unavailable|bad_request","retry_after":12}}` with the HTTP status mapping
documented in the skill. A message item:
```json
{"id": 1234, "peer": "c:77", "date": "2026-10-29T18:00:00Z", "from": {"peer": "u:5", "name": "Anna"},
 "text": "...", "reply_to": 1230, "forward": {"from": "Name", "date": "..."},
 "media": {"kind": "photo|video|voice|document|sticker|poll|other", "size": 12345, "mime": "image/jpeg", "name": "a.jpg"},
 "reactions": [{"emoji": "👍", "count": 2}], "edited": "2026-10-29T18:02:00Z", "topic": 12, "pinned": false}
```
Text is capped at Telegram's 4096-character message length; per-response cap 64 KiB; per run 512 KiB (default,
section 9). Nothing in `data` is ever an instruction (section 10).

### 5.4 Endpoint set (version 1)

| Method and path | Purpose |
|---|---|
| `GET /tg/v1/status` | connection and login state, policy summary, budgets (no identifiers beyond the hashed account) |
| `GET /tg/v1/dialogs?limit&cursor&folder&archived` | dialogs with unread counts, mute state, kind, topic-enabled flag |
| `GET /tg/v1/resolve?username=` | username to handle (read-only lookup; no phone or contact import) |
| `GET /tg/v1/peers/{peer}/messages?limit&cursor&from&to&topic&reverse` | history, date window, forum topic |
| `GET /tg/v1/peers/{peer}/messages/{id}` | one message with reply context |
| `GET /tg/v1/peers/{peer}/pinned` | pinned messages |
| `GET /tg/v1/peers/{peer}/topics` | forum topics |
| `GET /tg/v1/search?q&peer&from&to&type&limit&cursor` | in-chat search, or global search when `peer` is omitted |
| `GET /tg/v1/peers/{peer}/messages/{id}/reactions` | reaction list with counts (senders only if the account may see them) |
| `POST /tg/v1/media` `{peer, message_id}` | download to the sandbox (section 7); returns a relative path |
| `POST /tg/v1/actions` `{kind, peer, ...payload, request_id}` | prepare a write (section 8) |
| `GET /tg/v1/actions/{id}` | state of an action: `pending_confirmation|executed|cancelled|expired|failed|uncertain` |
| `DELETE /tg/v1/actions/{id}` | agent cancels its own pending action |

There is deliberately no endpoint for admin operations, takeout, contacts, profile, privacy, sessions or raw MTProto.

### 5.5 Audit record

Table `tg_audit(id, ts, run_ref, op, peer_hash, bytes, outcome, latency_ms)`; no message text, no names, no raw
ids (peer hash = HMAC of the id with a per-install key). Slog lines carry the same fields. Retention 30 days.

## 6. Authentication

### 6.1 QR login through the owner's private chat

Preconditions: `GOPOD_TG_ACCOUNT=1`, `GOPOD_TG_API_ID`, `GOPOD_TG_API_HASH` (the owner registers an application
at my.telegram.org; **TO VERIFY** current steps and ToS wording) and `GOPOD_TG_SESSION_KEY` all set, no active session.

1. Owner sends `/tg login` (Router `PermOwnerOnly`, private chat only; refused in groups for everyone).
2. The broker starts a login-mode gotd client, exports a login token (`qrlogin`), renders `Token.URL()` as a PNG
   with `rsc.io/qr` and sends it **directly** by the bot with no fallback, like `sendPrivateLoginText`
   (`login_handoff.go:79-91`), as a photo (a new `sendPrivateLoginPhoto` following the same rules). It does **not**
   go through the durable outbox or the artifact path (those are for ordinary content; a login QR is a short-lived
   credential, and replaying or retaining it is wrong).
3. Caption: `Scan this QR in Telegram on your phone: Settings → Devices → Link Desktop Device. Expires in 10 minutes.`
   with a Cancel button. The token is refreshed before it expires (`expires` from the export; **TO VERIFY** the
   interval, about 30 seconds) by editing the media; the message is deleted on success, cancel and expiry.
4. On acceptance (`UpdateLoginToken`, then `Import`) the broker reads the account (`auth.Status`) and checks that the
   account id equals the owner's Telegram user id (the private chat id equals the user id). A mismatch logs the
   session out at once and tells the owner; attaching a different account needs the host-terminal flag
   `gopod tg login --allow-other-account` (open question 4).
5. The session is stored (section 4.2), the owner sees `Connected as <name>. Reads allowed; writes ask first.` and the
   login state moves to `ready`. The device appears in Telegram's device list with the app name `gopod`
   (set through the gotd device config) so the owner can recognize and terminate it there.

Cancel (`/tg cancel` or the button), a 10-minute timeout (same constant as provider logins) and a controller restart
each tear down the login client and delete the QR message. Only one login runs at a time; a second `/tg login` while
one runs answers that it is running.

### 6.2 Two-factor password: host terminal only

If Telegram answers with a password requirement, the bot says: `Two-factor password needed. Run on the Pi:
gopod tg password`. The CLI subcommand reads the password from the terminal without echo and sends it to the running
controller over the existing authorized control socket (`PermOwnerOnly` includes the CLI/authorized socket,
`control.go:39`). The controller uses it once (`Client.Password`), zeroes the buffer, and never logs it. The password
never travels through a Telegram chat, the agent, an environment variable or a file. The same terminal-only rule
means a typed login code is never requested through Telegram (Telegram may invalidate login codes shared through
Telegram itself; **TO VERIFY**).

### 6.3 Logout and revoke

`/tg logout` asks for a Yes (Router confirmation), calls `auth.logOut` (server-side revocation) and deletes the
encrypted session file; any failure to reach Telegram still deletes the file and says the device must be removed
in Telegram's device list. `/tg status` shows `not connected | logging in | ready | writes paused (<reason>)`.

## 7. Read operations

Default mode `direct` (section 8 class `read`), all bounded:

| Operation | Source calls | Bounds (defaults) |
|---|---|---|
| dialogs | `messages.getDialogs` via `query` | page 50; 20 pages per run |
| history | `messages.getHistory` / topic history | page 50; date window required if no `limit`; cursor is opaque and server-checked (cannot escape the peer) |
| search in chat / global | `messages.search`, `messages.searchGlobal` | 20 per page; 20 searches per minute |
| topics, pinned | `messages.getForumTopics`, pinned filter | page 50 |
| reactions | `messages.getMessageReactionsList` | page 50 |
| unread and mute | from dialogs | included |
| media download | gotd `downloader` | per file 50 MiB, per run 200 MiB, sandbox total 1 GiB, 30 downloads per hour |

Media goes only to `<ChatsDir>/<owner folder>/tg-media/<action id>/<sha256-prefix>.<ext>` on the host, visible in
the container as `/workspace/chat/tg-media/...` (the ordinary per-chat workspace, the only output root the policy
allows, `capabilities.go` OutputRootPolicy). The server-supplied file name is never used; the extension comes from
an allowlist keyed by the detected MIME; no symlinks are followed (reuse the no-follow open of `internal/artifactfs`);
files are not executed or indexed. Files older than 7 days are removed by a daily sweep. Voice and video are just
files here; transcription is out of scope.

Reading **never** acknowledges messages: history, search and download calls do not mark anything read (class
`receipt` below is separate).

## 8. Write operations and policy

### 8.1 Classes and defaults

The owner decided "no restrictions in the owner's scope" in general (HANDOFF), but a send from the owner's real
account is outward facing and cannot be undone, and the content can be steered by untrusted text the agent has
read. So the proposed default is **`confirm` for anything that leaves a visible trace**, switchable per class by
the owner with `/tg policy` to `direct` or `off`.

| Class | Operations | Risk | Default | Notes |
|---|---|---|---|---|
| `read` | everything in section 7 | privacy only | `direct` | cannot be set to `confirm` (reads are not confirmable per call); can be `off` |
| `receipt` | mark dialog read | tells the sender you read it; clears unread | `direct` | reversible only by the sender seeing it; never automatic |
| `react` | add or remove a reaction | visible, reversible | `direct` | |
| `send` | send text, reply, send file or voice, forward, drafts | visible, irreversible | **`confirm`** | the main risk class |
| `edit` | edit own message | visible edit mark | `confirm` | |
| `delete` | delete own message for everyone or only for self | irreversible for others | `confirm` | switching to `direct` shows an extra warning |
| `admin` | join/leave, create, invite, ban, promote, block, contacts, profile, privacy, other sessions | high, account-level | `off`, **not implemented** | no endpoint exists in version 1; a later spec adds any of them |

### 8.2 Prepare, confirm, commit

Writes follow the CP-027 pattern, which applies here because the broker holds the credential
(`2026-10-26-scoped-action-approvals.md`, "Required behavior"):
1. The agent calls `POST /tg/v1/actions`. The broker validates the handle, the payload (length, file path under the
   output root via the artifact snapshot, which gives a verified size and SHA-256) and the class policy, stores an
   action row `prepared` with a canonical payload hash, a one-shot nonce and a 10-minute expiry, and returns
   `{"id","state":"pending_confirmation"}` (mode `confirm`) or executes at once (mode `direct`).
2. In `confirm` mode the bot sends the owner, in the private chat only, the exact intent (section 13 screen 3): the
   recipient name and handle, the full text (or filename, size, hash for files, source and target for forwards),
   and the buttons Send / Cancel. The buttons carry only an opaque token (memory only, like the existing
   confirmation tokens, `internal/telegram/cmdbuttons.go`); a press re-dispatches through the Router
   (`PermOwnerOnly`, private chat, presser must be the owner), so a forged or forwarded press fails.
3. On Send the broker verifies the stored payload hash, expiry and that the action is still `prepared`, moves it to
   `committing` durably **before** the MTProto call and fixes the `random_id` first, calls Telegram, and records
   `executed` with the message id, or `failed` (a definitive refusal such as `PEER_ID_INVALID`), or `uncertain`.
4. The agent polls `GET /tg/v1/actions/{id}`; `uncertain` is reported as such and is never retried by the broker.

A model cannot approve its own action; a request that changes recipient or text after preparation is a new action.

### 8.3 Taint downgrade (default on)

The broker marks a run's capability **tainted** once it has served message text from any sender other than the
owner. In a tainted run a write whose class is `direct` is **downgraded to `confirm`**, and the confirmation
screen adds `This run read messages from: <names>` (section 13 screen 3). This is the cheap structural defence
against "a message told the agent to write": no write can be triggered by content without the owner seeing the
exact payload. The owner may switch it off in `/tg policy` (`taint: on|off`); it is not part of any class.

### 8.4 Idempotency, `uncertain`, no replay

- `request_id` from the agent plus the agent folder is a unique key: the same request returns the same action.
- The MTProto `random_id` is chosen at `prepared` time and stored; a retry by the transport inside the same call may
  reuse it (Telegram deduplicates by random id within a window, **TO VERIFY**) but the broker never creates a second
  attempt for an action once it reached `committing`.
- On restart, any `committing` row becomes `uncertain` (the same rule as `uncertain` outbox deliveries in CP-002).
  The owner sees `Possibly sent: "...". Check the chat.` and may run `/tg reconcile <id>`: a read-only lookup of the
  recent history of that peer for an own message with the same text in the window, reported as found or not found.
  There is no automatic resend and no resend button for an `uncertain` action; resending is a new prepared action.
- `FLOOD_WAIT` on a write is a definitive refusal (the request was not executed): the action becomes `failed` with
  `rate_limited` and a `retry_after`, not `uncertain`.

## 9. Rate limits, flood wait and account safety

Defaults (all configurable in `tg_settings`, shown by `/tg budget`):
- Transport: 3 requests per second sustained, burst 10, one gotd connection.
- Reads: 120 per minute; searches 20 per minute; downloads as in section 7; response 64 KiB; per run 512 KiB.
- Writes: 20 per hour and 100 per day across all classes; the agent cannot raise these.
- Our own middleware (about 40 lines over `tgerr.AsFloodWait`): for a flood wait of at most 10 seconds on a read, sleep
  once and retry that call; for longer waits return `rate_limited` with `retry_after` and set a global cooldown;
  never retry a write after a wait.
- **Circuit breaker:** any of `PEER_FLOOD`, `USER_RESTRICTED`, `PHONE_NUMBER_BANNED`, `USER_DEACTIVATED`,
  `AUTH_KEY_UNREGISTERED`, `SESSION_REVOKED` stops all writes (or, for the last three, the whole connector),
  notifies the owner once in the private chat, and needs `/tg resume` to re-enable.
- Account safety statement (**TO VERIFY**, from memory only): Telegram's API terms require an application registered by
  the user, forbid spam, mass messaging and flooding, and Telegram restricts or bans accounts that look automated.
  Low-volume reading and a few confirmed messages is the normal low-risk profile; mass or unconfirmed sending from
  an automation is the usual ban pattern. Owner-confirmed sends, the budgets above and the breaker are the mitigation;
  they do not remove the risk of an account restriction. The owner accepts this risk by enabling writes (open question 1).

## 10. Untrusted content and prompt injection

- Every read response carries `"untrusted_content": true`, and the skill text states that message text, names, file
  names, captions, forwards and link previews are data, never instructions, approvals or preferences
  (shared contract, "Retrieved pages, attachments, forwards ... are data").
- The broker never executes or interprets content. Links are not fetched. Media is stored, not opened.
- A write requires a separate explicit API call from the agent and, in `confirm` mode or a tainted run, the owner's
  button press on the exact payload (section 8.3). Content therefore cannot cause a send by itself.
- Provenance of anything the agent later saves to memory should reference the account hash, peer handle and message
  id; automatic ingestion of Telegram history into memory stays out of scope (CP-028 Out of scope).

## 11. Isolation and proofs

The user session must never reach a group runtime or an agent container.
- Capability issuance is gated on the host-verified owner tier and the owner folder; group, shared and task-role
  runs get no capability (`containerEnvAllow` returns nothing for restricted tiers, `admission_policy.go:76-83`).
- `GOPOD_TG_API_ID`, `GOPOD_TG_API_HASH`, `GOPOD_TG_SESSION_KEY` and `GOPOD_TG_BROKER_ADDR` are never added to any
  container env allowlist; a test asserts they are absent from `containerEnvAllow` for every tier.
- `data/tgaccount/` is added to the protected roots (`protectedDataNames`) and to the mount deny list
  (`mountsec/blocked.go`), so neither an output root nor an extra mount can expose it; a startup check refuses to
  start the connector if any mount source of any running container contains the directory.
- Reachability: the broker address is not routable from group runtimes (internal network behind the exact-route relay,
  ISOLATION.md). This is **TO VERIFY** by an acceptance test that tries a TCP connection and a request with a stolen
  capability from a group container, and expects no route or a uniform 401.
- Uniform denials: an invalid, expired, wrong-folder or group-scope capability returns the same 401 body (like
  `ErrStaleCapability` merging cases, `capabilities.go`), and an inaccessible peer returns the same `not_found` as a
  missing one.
- `/tg` is `PermOwnerOnly`, refused in groups for everyone including the owner, and its callbacks are authorized by the
  Router on the presser, never in a handler. Task-role runs get no capability even in the owner folder until a later
  decision (the credential adapter is deferred).

## 12. Observability, failure modes, retention

| Failure | Behavior |
|---|---|
| missing env secrets or undecryptable session | connector refuses to start; `/tg status` says which input is missing (names only) |
| session revoked from Telegram's device list | `AUTH_KEY_UNREGISTERED` stops the connector, owner notified once, `/tg login` again |
| Telegram unreachable | reads fail `unavailable`; pending actions stay `prepared` until expiry; a commit in flight becomes `uncertain` |
| restart during a login | login state is in memory and is discarded; QR message deleted at startup if its id was recorded |
| controller restart with pending actions | `prepared` rows are expired on read; `committing` rows become `uncertain` |
| DC migration, reconnect | handled by gotd; invisible except latency |
| rate limit | `rate_limited` with `retry_after`; circuit breaker as in section 9 |

Retention: action rows keep payload text only while `prepared` or `uncertain`; after resolution the text is
erased and only hashes and ids remain (30 days); audit rows 30 days; media 7 days.

## 13. The `/tg` command (private chat, owner only)

Picker screens follow `docs/CONTROL.md` section 6.1 (no argument shows the picker; current choice marked; edited in
place; each press re-dispatches through the Router).

Screen 1, `/tg`:
```
Telegram account  ·  ready as Anna (a1b2c3d4)
Reads: on   Writes: ask first   Taint rule: on
Pending actions: 1   Today: 4 reads sent to agent, 2 writes
[Policy] [Pending] [Budget]
[Log out]
```
Screen 2, `/tg policy` (pickers; the write classes show `off | ask | direct`):
```
Write policy
receipt  : direct      [off] [ask] [✓ direct]
react    : direct      [off] [ask] [✓ direct]
send     : ask         [off] [✓ ask] [direct]
edit     : ask         [off] [✓ ask] [direct]
delete   : ask         [off] [✓ ask] [direct]   (direct shows a warning)
Taint rule (downgrade direct to ask after reading others' messages): [✓ on] [off]
```
Screen 3, a confirmation sent when the agent prepares a write:
```
The assistant wants to SEND as your account
To: Anna K. (user a1b2c3d4-5)
Text:
  "Tomorrow at 10 works for me."
This run read messages from: Anna K., Pavel
Expires in 10 min.   [Send] [Cancel]
```
Other forms: `/tg login`, `/tg cancel`, `/tg logout` (confirms), `/tg status`, `/tg pending`, `/tg budget`,
`/tg resume`, `/tg reconcile <id>`, `/tg policy <class> <off|ask|direct>`, and on the host `gopod tg password`.

## 14. Test plan (synthetic; no real account)

- Unit: policy table and the taint downgrade; action state machine (prepared, confirm, committing, executed, failed,
  uncertain; expiry; double press; replay; payload tamper); idempotency by `request_id`; budgets; peer handle
  validation and uniform `not_found`; cursor escape; media path and size limits; the encrypted session storage
  (round trip, wrong key, tampered ciphertext, truncated file, mode 0600/0700); flood-wait middleware with a fake
  clock; circuit breaker triggers.
- Integration with gotd `tgtest` (in-memory server) and `tgmock` for QR login (accept, expire, cancel, password
  step), dialogs, history, search, send with a lost response (must end `uncertain` and never resend), flood wait.
- Control: Router refusals for the owner in a group, a member, a stranger and forged presses; `/tg` pickers.
- Isolation acceptance: capability absent for group and task runs; `GOPOD_TG_*` absent from every tier's env
  allowlist; `data/tgaccount` rejected as output root and mount; backup set lists the session as not covered and
  never contains it; group-container connection attempt fails (needs a Docker-capable run, optional in CI).
- Live acceptance (separate explicit owner authorization): login with a throwaway account, read a synthetic chat,
  one confirmed send, one `uncertain` drill by killing the controller during commit.

## 15. Rollout and rollback

Disabled by default: `GOPOD_TG_ACCOUNT=1` plus the three secrets. Rollout order: PR 1 (no behavior), login in a
test, reads, then writes behind the same flag with all write classes `off` until the owner enables them in
`/tg policy`. Rollback: unset the flag (the connector stops, the capability is no longer minted), `/tg logout` or
delete `data/tgaccount/`, and remove the device in Telegram. The new tables are additive and safe to leave.
Production, the Pi and any real account are not touched by saving this spec.

## 16. Open questions for the owner

1. **Writes at all, and the default.** Accept `confirm` as the default for `send`, `edit` and `delete` with the taint
   downgrade on, or choose `direct` for any class now? The risk of `direct`: text steered by a read message goes out
   from the real account, and an account restriction (section 9) is possible.
2. **Which operations beyond the table** (join channels, contacts, drafts, polls, reactions on others' messages) are
   wanted in version 2? They are `admin` and not built.
3. **Session encryption key** in `GOPOD_TG_SESSION_KEY` (ADR D052): acceptable, or prefer re-login after each restart
   and no file?
4. **Which account.** Must the connected account always be the owner's own (default, enforced), or may a second
   account be attached (`--allow-other-account` on the terminal)?
5. **API application.** The owner must register an application at my.telegram.org and supply `API_ID` and `API_HASH`
   in the environment; confirm this is acceptable (no sharing of an existing app's credentials).
6. **Budgets** in section 9 (20 writes an hour, 100 a day, 512 KiB of read text per run): keep or change?
7. **Media** retention (7 days, 1 GiB) and the 50 MiB per-file cap: keep or change?

## 17. Implementation outline (small PRs, tests first, no deploy)

1. **ADR and plumbing.** D052, `internal/tgaccount` skeleton, `go.mod` (gotd/td, `rsc.io/qr` direct) with a license
   review, config flag and env names, encrypted session storage and tests, backup `notCovered` entry and parity test,
   protected-root and mount-deny additions. No behavior without the flag.
2. **Login.** QR flow through the bot (`sendPrivateLoginPhoto`), password step through the CLI/control socket,
   status, logout, owner-id check; `/tg login|cancel|logout|status`; tests on `tgtest`.
3. **Read broker.** Peer cache and handles, endpoints for dialogs, history, search, topics, pinned, reactions, the
   output contract, budgets, flood-wait middleware, audit table; capability issuance for the owner tier only;
   isolation acceptance tests.
4. **Media.** Sandbox download, limits, sweep.
5. **Write core.** `tg_actions` state machine, policy table, confirmation screen and Router-checked buttons, taint
   rule, idempotency, `uncertain` handling and `/tg reconcile`, circuit breaker; `/tg policy|pending|budget|resume`.
6. **Write operations.** send/reply/forward, edit, delete, mark read, react, file and voice upload through the
   artifact snapshot; all classes default per section 8.1.
7. **Agent guide.** `container/skills/telegram-account/SKILL.md`, the embedded guide and `TelegramAccountPolicy`
   paragraph for Codex; live acceptance runbook; CP-028 moves to `in-progress` at PR 1 and `done` after PR 7 and the
   owner's live acceptance.

## 18. Sources

Repository files cited inline. External: `github.com/gotd/td` v0.163.0 (MIT) source read in a scratch module on
2026-10-30 (`LICENSE`, `go.mod`, `telegram/`, `telegram/auth/qrlogin`, `session/`, `tgerr/`, `tgtest/`);
owner's `telegram-cli` (`dbad45d`) and `telegram-mcp` (`64aaf47`) reviewed read-only; donor `rzaytsev/codex-agent`
`src/tdl-auth.js` and `docs/telegram-read.md` as login-flow prior art (login flow only, no session reused).
Telegram API terms and the login-token details marked TO VERIFY were not fetched.
