# gopod — Control plane

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [SKILLS.md](SKILLS.md) · [MEMORY.md](MEMORY.md) · [DECISIONS.md](DECISIONS.md)
> Decision: [D011](DECISIONS.md)

gopod needs a control surface — admin commands, diagnostics, scheduling
ops, skill management — that is **separate** from the data plane (user
message → agent → reply). Without a unified control plane, slash commands
end up scattered, authorization gets duplicated in every handler, and the
CLI and Telegram surfaces drift apart.

This document defines `internal/control`: a single Router, a single Command
type, a single Permission model, and two thin frontends (Telegram and CLI).

---

## 1. Goal

One package owns "what can be done with gopod, by whom, from where". Every
admin slash command, every CLI subcommand, and (later) every external RPC
call goes through it. Adding a new command means writing one handler and
declaring its permission level. Adding a new frontend means writing one
translator. The two never mix.

---

## 2. Problem statement

Without `internal/control`, gopod would grow these failure modes:

1. **Auth duplication.** Every slash-command handler would re-implement
   "is this the owner chat?" in its own way. One missed check = privilege
   escalation. Authorization should be an invariant, not discipline.
2. **Telegram/CLI drift.** `gopod skills list` (CLI) and `/skills list`
   (Telegram) would be two separate code paths with two separate output
   formats and two separate sets of bugs. Already half-built into different
   docs ([SKILLS.md §5](SKILLS.md), [INTEGRATIONS.md #8](INTEGRATIONS.md)).
3. **Untested admin paths.** Slash-command handlers tied to `go-telegram/bot`
   are awkward to test. A `Router.Dispatch(Command{...})` call is trivial.
4. **No room to grow.** Future surfaces (web UI, gopod-as-MCP-server,
   HTTP API for status checks) all need the same dispatch. Building them
   without a Router means rewriting handlers each time.

---

## 3. Core types

```go
package control

// A Command is a frontend-agnostic request.
type Command struct {
    Name   string            // e.g. "chat.register", "tasks.pause"
    Args   map[string]string // parsed arguments
    Caller Caller            // who is making this request
}

// Caller describes the origin of a Command. Frontends populate this.
type Caller struct {
    ChatID   int64  // Telegram chat ID; 0 for CLI/socket
    ThreadID int    // private-chat topic the command came from; 0 = main chat / CLI. Routing only, never authorization
    UserID   int64  // Telegram user ID; 0 for non-Telegram
    Username string // Telegram username if available
    Source   Source // "telegram" | "cli" | "socket"
    IsOwner  bool   // computed: Source==cli OR ChatID==GOPOD_OWNER_CHAT_ID
}

type Source string
const (
    SourceTelegram Source = "telegram"
    SourceCLI      Source = "cli"
)

// Perm declares the minimum privilege a handler requires.
type Perm int
const (
    PermPublic     Perm = iota // any chat, any user
    PermChatLocal              // any chat, but must affect only the calling chat
    PermOwnerOnly              // only the owner chat / CLI / authorized socket
)

// A Handler implements one Command. It receives a fully-validated Command:
// the Router has already checked permissions before calling.
type Handler func(ctx context.Context, cmd Command) (Response, error)

// A Response carries structured data plus a textual rendering. Frontends
// pick whichever fits their medium.
type Response struct {
    Text     string         // pre-rendered text suitable for any frontend
    Markdown string         // optional richer rendering
    Data     map[string]any // structured payload (used by CLI --json, future HTTP)
    Code     int            // exit-code-style: 0 = OK, CodeConfirm = asks to confirm, other non-zero = error
    Buttons  [][]Button     // optional follow-up commands, in rows (D028)
}

// A Button offers a follow-up command. Pressing it dispatches Command
// with Args through the Router as a new Command from the same caller,
// so Perm applies exactly as to a typed command. Command "" dismisses
// (the Cancel of a confirmation). Args are names and ids, never secrets.
type Button struct {
    Label     string
    Command   string   // canonical name, e.g. "model"; "" = dismiss
    Args      []string
    Style     string   // "", "primary", "danger", "success" (a hint)
    Confirmed bool     // dispatch with Command.Confirmed: the Yes of a confirmation
}

const CodeConfirm = 2 // the command asks to confirm; nothing changed yet

// Command.Confirmed bool: set by a confirmation's Yes button or the CLI
// --yes flag. A confirmable handler without it returns
// Confirm(cmd, question) instead of acting. It skips the question only:
// the Router checks Perm on every Dispatch.

// Router is the central dispatch table. One per process.
type Router struct { ... }

func New(deps Deps) *Router

func (r *Router) Register(name string, perm Perm, h Handler)
func (r *Router) Dispatch(ctx context.Context, cmd Command) (Response, error)
func (r *Router) List() []CommandInfo            // for /help, gopod help
```

The code is the source of truth where this sketch differs (`Args` is a
`[]string`, `Register` also takes the slash name and description, there is
no `Markdown` field). Frontends without buttons print them with
`control.FormatText(resp)`: the text, then `[label]  command args` per
button, or for `CodeConfirm` a hint to re-run with `--yes`
(`control.ParseConfirmFlag` strips `--yes`/`-y` from CLI args).

`Deps` carries the handles every handler needs: store, queue, runner,
scheduler, skills loader, memory store, telegram sender (for replies that
include side-channel notifications), config snapshot. Handlers receive `Deps`
via closure, not via a global, so testing is `control.New(testDeps)`.

---

## 4. Permission model

The Router enforces `Perm` **before** calling the handler. There is exactly
one place auth lives.

| Perm | Allowed when |
|---|---|
| `PermPublic` | always |
| `PermChatLocal` | always, but the handler MUST scope its effects to `cmd.Caller.ChatID` (Router cannot enforce this — it's a handler discipline backed by tests) |
| `PermOwnerOnly` | `cmd.Caller.IsOwner == true` |

`IsOwner` is computed once by the frontend when constructing `Caller`:

- **Telegram frontend:** `IsOwner = (ChatID == GOPOD_OWNER_CHAT_ID)`. Per
  [D006](DECISIONS.md), there is exactly one owner chat.
- **CLI frontend:** `IsOwner = true` always. Anyone with shell access to the
  gopod box is implicitly owner. Document this trust assumption.

If a handler with `PermOwnerOnly` is invoked from a non-owner chat, the
Router returns a `Response{Text: "Not authorized.", Code: 1}` and **never**
calls the handler. This is logged at `warn` level with the caller details.

---

## 5. Built-in command catalog

Commands are grouped by first slash word so we can scale to ~50 commands
with only a handful of top-level Telegram handlers. The handler for a top
group dispatches by sub-verb (e.g. `/chats list` → `chats.list`).

### 5.1 Public

| Telegram | CLI | Perm | What |
|---|---|---|---|
| `/ping` | `gopod ping` | Public | Liveness check, replies "pong" |
| `/whoami` | — | Public | Returns calling chat ID, user ID, owner status |
| `/help` | `gopod help` | Public | Lists commands the caller can run |
| `/version` | `gopod version` | Public | Build version, git sha, Go version |

### 5.2 Chat-local

These run in any chat but affect only the calling chat. Handler must scope
all DB queries by `cmd.Caller.ChatID`.

| Telegram | CLI | Perm | What |
|---|---|---|---|
| `/compact` | — | ChatLocal | Force a context compact + summarize this chat |
| `/clear` | — | ChatLocal | Clear this conversation's context; asks to confirm (§6.1) |
| `/model [name\|default]` | — | ChatLocal | Show (picker) or set this chat's model |
| `/provider [claude\|codex]` | — | ChatLocal | Show (picker) or switch this chat's provider; a switch asks to confirm |
| `/remember <text>` | — | ChatLocal | `memory_add` convenience for the calling chat |
| `/recall <query>` | — | ChatLocal | `memory_search` over the calling chat's memories |
| `/tasks list` | — | ChatLocal | List scheduled tasks for the calling chat |
| `/tasks add <cron> <prompt>` | — | ChatLocal | Schedule a task for the calling chat |
| `/tasks pause <id>` | — | ChatLocal | Pause own task |
| `/tasks resume <id>` | — | ChatLocal | Resume own task |
| `/tasks cancel <id>` | — | ChatLocal | Cancel own task |
| `/skills list` | — | ChatLocal | List skills enabled in the calling chat |
| `/skills enable <name>` | — | ChatLocal | Enable a skill for this chat |
| `/skills disable <name>` | — | ChatLocal | Disable a skill for this chat |

### 5.3 Owner-only

Cross-chat operations and global state.

| Telegram (in owner chat) | CLI | Perm | What |
|---|---|---|---|
| `/chats list` | `gopod chats list` | OwnerOnly | List all registered chats |
| `/chats register <folder> [--here]` | `gopod chats register <folder>` | OwnerOnly | Register a chat. `--here` uses the current chat. |
| `/chats unregister <folder>` | `gopod chats unregister <folder>` | OwnerOnly | Remove a chat |
| `/chats trigger <folder> <pattern>` | `gopod chats trigger ...` | OwnerOnly | Change a chat's trigger word |
| `/container list` | `gopod container list` | OwnerOnly | Show running agent containers |
| `/container restart <folder>` | `gopod container restart <folder>` | OwnerOnly | Restart a chat's container |
| `/container stop <folder>` | `gopod container stop <folder>` | OwnerOnly | Stop a chat's container |
| `/queue status` | `gopod queue status` | OwnerOnly | Show GroupQueue state across all chats |
| `/tasks list <folder>` | `gopod tasks list <folder>` | OwnerOnly | Tasks for any chat |
| `/tasks pause <id>` (cross-chat) | `gopod tasks pause <id>` | OwnerOnly | Pause any task |
| `/skills install <url>` | `gopod skills install <url>` | OwnerOnly | Install a skill (clones git repo into `container/skills/` or `skills/mcp/`) |
| `/skills test <name>` | `gopod skills test <name>` | OwnerOnly | Smoke-test an MCP skill |
| `/memory search <query> [--chat <folder>]` | `gopod memory search ...` | OwnerOnly | Cross-chat memory search |
| `/logs [folder] [--tail N] [--level X]` | `gopod logs ...` | OwnerOnly | Tail logs by chat and/or level |
| `/model set <folder> <model>` | `gopod model set ...` | OwnerOnly | Switch model for one chat |
| `/system health` | `gopod system health` | OwnerOnly | Container daemon, DB, embedder, scheduler, queue health |
| `/system uptime` | `gopod system uptime` | OwnerOnly | Process uptime, last restart cause |
| `/debug ...` | `gopod debug ...` | OwnerOnly | Free-form diagnostics (per-subcommand) |

### 5.4 Notes on the table

- Commands that exist on **both** rows of a table (e.g. `/tasks list` chat-
  local vs `/tasks list <folder>` owner-only) dispatch by argument count: no
  args = the calling chat, with `<folder>` = cross-chat. The Router handles
  this by registering two handlers under different command names
  (`tasks.list.local` vs `tasks.list.owner`) and the Telegram top-level
  handler picks which one based on argv.
- Where the CLI column is empty, the command is **Telegram-only** by design
  (e.g. `/whoami` makes no sense on a shell). The reverse is rare.
- The skills CLI subcommands defined in [SKILLS.md §5](SKILLS.md) are
  reabsorbed here — they go through `internal/control` like everything else.

---

## 6. Telegram frontend

```go
// internal/control/frontend_telegram.go

func RegisterTelegramHandlers(b *bot.Bot, r *Router, ownerChatID int64) {
    b.RegisterHandler(bot.HandlerTypeMessageText, "/", bot.MatchTypePrefix,
        makeDispatcher(r, ownerChatID))
}

func makeDispatcher(r *Router, ownerChatID int64) bot.HandlerFunc {
    return func(ctx context.Context, b *bot.Bot, update *models.Update) {
        msg := update.Message
        if msg == nil || !strings.HasPrefix(msg.Text, "/") {
            return
        }
        name, args := parseSlash(msg.Text)
        cmd := control.Command{
            Name: name,
            Args: args,
            Caller: control.Caller{
                ChatID:   msg.Chat.ID,
                UserID:   msg.From.ID,
                Username: msg.From.Username,
                Source:   control.SourceTelegram,
                IsOwner:  msg.Chat.ID == ownerChatID,
            },
        }
        resp, err := r.Dispatch(ctx, cmd)
        // … reply via SendMessage / SendChunked / reactions
    }
}
```

`parseSlash` turns `"/chats register foo --here"` into
`name="chats.register"`, `args={"folder":"foo","here":"true"}`. Top-level
slash word and first sub-verb are joined with `.`; the rest is parsed as
positional + flags. The parser is one file, ~80 LOC, fully tested.

**Important:** the Telegram default handler (which stores all messages in
the data plane) and the Telegram control dispatcher are **two different
handlers** registered with `go-telegram/bot`. The control dispatcher matches
on `/` prefix and runs first; if it consumes the update, the default handler
still saves the message (because users may want to see "Alice ran /whoami"
in the chat history). Storage is independent of dispatch.

### 6.1 Buttons, pickers and confirmations ([D028](DECISIONS.md))

As implemented (`internal/telegram/cmdbuttons.go`), a Response's `Buttons`
are sent as inline buttons under the reply (last chunk). Each button's
callback data is a random token, never the command:

| Data | Button | Valid | On press |
|---|---|---|---|
| `cc:<token>` | a follow-up command (picker choice) | 10 min | `Router.Dispatch(Command{Name, Args})`; the message is edited in place to the result (its text and buttons) |
| `cf:<token>` | Yes of a confirmation | 5 min, single use | the same with `Confirmed = true`; spends the Yes/Cancel pair, so Yes runs at most once |
| `cx:<token>` | Cancel (a Button with no Command) | 5 min, single use | spends the pair; the message becomes «Отменено.» without buttons |
| `pg:<token>:<n>` | `‹ n/N ›` of a long reply | 10 min | edits the same message to page n |

Long replies: pages are budgeted at 3800 rendered HTML bytes (`/logs`, `/recall`,
`/tasks list`, `/help`, any command) is one message with a `‹ n/N ›` row
instead of several chunks, split on line boundaries; a code block cut by a
page boundary is closed and reopened. The Response's own buttons stay under
every page. Handlers need nothing for this: paging is the frontend's.

The token is bound to the chat its message is in. The press is authorized
like a typed command: the chat of the message carrying the button must be
registered, the `Caller` gets that chat, its topic, the pressing user, and
`IsOwner = chat == GOPOD_OWNER_CHAT_ID`; the Router checks `Perm` on every
press, including a confirmed one. A refusal ("Not authorized.") is a toast
and the message is not changed. A restart forgets every token: the press
toasts «Кнопка устарела».

Pickers: `/model` with no args offers the provider's model names (Claude:
`fable`, `opus`, `sonnet`, `haiku`; Codex: none, any id is typed) plus
`default`; `/provider` with no args offers `claude`, `codex`. The current
one is marked `✓` and styled `primary`.

Confirmations (owner Q6): `/clear` and `/provider <name>` answer `CodeConfirm` with Yes (`danger`) / Cancel; a
provider picked from the picker is confirmed the same way. `/chats
unregister` and `/container restart|stop` are on the Q6 list but do not
exist yet: they must use `control.Confirm` when they are added. The CLI
(C3, not built yet) confirms with `--yes` (`control.ParseConfirmFlag`).

---

## 7. CLI frontend

```go
// internal/control/frontend_cli.go (called from cmd/gopod)

func RunCLI(r *Router, argv []string) int {
    name, args := parseArgv(argv)
    cmd := control.Command{
        Name: name,
        Args: args,
        Caller: control.Caller{Source: control.SourceCLI, IsOwner: true},
    }
    resp, err := r.Dispatch(context.Background(), cmd)
    if err != nil { … }
    if jsonFlag { fmt.Println(toJSON(resp.Data)) } else { fmt.Println(resp.Text) }
    return resp.Code
}
```

CLI structure:

```
gopod serve              # the main daemon (replaces today's bare `gopod`)
gopod migrate            # run pending DB migrations and exit
gopod version
gopod <group> <verb> ... # everything else routes through control.Router
```

`serve` and `migrate` are reserved top-level subcommands handled before the
Router is consulted. Everything else (`gopod chats list`,
`gopod skills install ...`) is rewritten to a `Command` and dispatched.
There is no `gopod ctl` prefix — it would be noise.

`--json` is a global flag that prints `Response.Data` as JSON instead of
`Response.Text`. Useful for scripting.

---

## 8. No socket / HTTP frontend

Per user decision (2026-04-09): gopod does **not** ship a Unix-socket or
HTTP control frontend. The Router's frontend abstraction stays minimal —
two implementations (Telegram, CLI) — and the package layout reflects that.
A third frontend can be added later without disturbing the Router itself,
but it is **not in scope** for v0 and there is no stub file.

---

## 9. Logs subsystem

`/logs` and `gopod logs` need a backing store. We have two options:

**Option A — File + lumberjack rotation.** Standard, fast, but querying
("show me errors from chat foo in the last hour") means shelling out to
grep/tail or reimplementing it.

**Option B — SQLite table.** One more table in `data/store.sqlite`,
queryable with normal SQL, no new dependency, retention via cron-style
delete. Slightly more write overhead.

**Decision: B.** gopod is personal scale (~100s of log lines/min peak).
SQLite WAL handles it without buffering. The control plane gains structured
queries for free.

```sql
CREATE TABLE logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          INTEGER NOT NULL,        -- unix ms
  chat_folder TEXT,                    -- nullable for system logs
  source      TEXT NOT NULL,           -- 'host' | 'container' | 'scheduler' | 'queue' | 'control'
  level       TEXT NOT NULL,           -- 'debug' | 'info' | 'warn' | 'error'
  msg         TEXT NOT NULL,
  attrs_json  TEXT                     -- structured slog attrs as JSON
);
CREATE INDEX idx_logs_chat_ts   ON logs(chat_folder, ts DESC);
CREATE INDEX idx_logs_level_ts  ON logs(level, ts DESC);
CREATE INDEX idx_logs_source_ts ON logs(source, ts DESC);
```

`internal/log/sqlite_handler.go` implements `slog.Handler`, fans out to both
stderr (for live tail during dev) and the SQLite table (for `/logs`
queries). Retention: `GOPOD_LOG_RETENTION_DAYS=30` (configurable, 0 =
keep forever). A daily cleanup task running through the scheduler deletes
rows older than the cutoff.

`/logs` query patterns:

```
/logs                       → last 50 lines, all chats, all levels
/logs my-chat               → last 50 for that chat
/logs --tail 200            → last 200 lines
/logs --level warn          → only warn+error
/logs my-chat --level error → combination
```

---

## 10. Module layout

```
internal/control/
├── control.go              # Router, Command, Caller, Perm, Response, Deps
├── parse.go                # parseSlash + parseArgv
├── help.go                 # /help and `gopod help` rendering
├── frontend_telegram.go    # registers Telegram handler, builds Caller
├── frontend_cli.go         # runs from cmd/gopod, builds Caller
├── handlers/
│   ├── system.go           # ping, whoami, help, version, system.*
│   ├── chats.go            # chats.list/register/unregister/trigger
│   ├── container.go        # container.list/restart/stop
│   ├── queue.go            # queue.status
│   ├── tasks.go            # tasks.list/add/pause/resume/cancel
│   ├── skills.go           # skills.list/enable/disable/install/test
│   ├── memory.go           # memory.search/remember/recall/compact
│   ├── logs.go             # logs.show
│   ├── model.go            # model.set
│   └── debug.go            # debug.*
└── control_test.go

internal/log/
├── log.go                  # init slog with multi-handler
├── sqlite_handler.go       # slog.Handler writing to logs table
└── retention.go            # daily cleanup task
```

`cmd/gopod/main.go` wires everything:

```go
func main() {
    cfg := config.Load()
    store := store.Open(cfg)
    log.Init(store, cfg)                // SQLite handler now active
    router := control.New(control.Deps{
        Store: store, Queue: q, Runner: r, Scheduler: s,
        Skills: skl, Memory: mem, Config: cfg,
    })
    handlers.RegisterAll(router)        // handlers/*.go self-register
    if len(os.Args) > 1 && os.Args[1] != "serve" {
        os.Exit(control.RunCLI(router, os.Args[1:]))
    }
    // serve mode:
    bot := telegram.New(cfg, store)
    control.RegisterTelegramHandlers(bot, router, cfg.OwnerChatID)
    // … start scheduler, queue, ipc watcher, message poller …
    bot.Start(ctx)
}
```

---

## 11. Configuration

```
GOPOD_OWNER_CHAT_ID=123456789      # already defined; the only auth boundary
GOPOD_LOG_LEVEL=info               # debug|info|warn|error
GOPOD_LOG_RETENTION_DAYS=30        # 0 = keep forever
GOPOD_HELP_ALLOW_LIST_OWNER_ONLY=0 # if 1, /help only shows commands the caller can run
```

**Secrets policy** ([D012](DECISIONS.md)): nothing in this list, in
`chats/<folder>/skills.json`, or in any other committed config file may
contain a secret value. Secrets only ever live in process environment
variables. The control plane uses the same identity machinery as everything
else and introduces no new secrets surface.

---

## 12. Implementation milestones

Slot into [ROADMAP.md](../ROADMAP.md) Phase 1 as **M3.5**, between M3
(GroupQueue) and M4 (Scheduler), because the Scheduler will register
`/tasks *` handlers with the Router as part of its own setup.

| ID | Step |
|----|------|
| **C1** | Router scaffold: `Command`, `Caller`, `Perm`, `Response`, `Router.Register/Dispatch`, basic auth enforcement, unit tests |
| **C2** | Telegram frontend: `RegisterTelegramHandlers`, `parseSlash`, dispatch wiring. Add `/ping`, `/whoami`, `/version`, `/help` |
| **C3** | CLI frontend: argv parser, `serve`/`migrate` reserved subcommands, dispatch path, `--json` flag. Mirror `/ping` etc. as `gopod ping` |
| **C4** | First batch of real handlers: chats, queue, container, system. Replaces ad-hoc M3 outputs with `/queue status` |
| **C5** | Logs subsystem: `internal/log/sqlite_handler.go`, retention task, `/logs` and `gopod logs` handler |

After M3.5, every later milestone (M4 scheduler, M9 memory, S1–S5 skills,
I-series integrations) registers its commands through the Router instead of
inventing local plumbing.

---

## 13. Open questions

1. **Should chat-local commands be allowed in non-registered chats?** A
   user typing `/recall foo` in a chat gopod doesn't know about — do we
   silently ignore, or reply "register this chat first"? Current proposal:
   reply with a one-liner.

2. **`/help` rendering for ChatLocal vs OwnerOnly.** In owner chat we show
   everything. In a registered non-owner chat we show Public + ChatLocal.
   In an unregistered chat we show only `/help` and `/whoami`. Confirm.

3. **Rate-limiting.** Do we need per-caller rate limits on the control
   plane? For personal scale, probably not. Note in HANDOFF if it ever
   becomes a real issue.

4. ~~**Telegram inline keyboards for confirmations.**~~ Resolved by
   [D028](DECISIONS.md) (§6.1): the handler decides what needs a
   confirmation (`control.Confirm`), so the CLI asks the same questions;
   the Yes re-dispatches through the Router.

---

## 14. Sources

- [go-telegram/bot — RegisterHandler / MatchType](https://pkg.go.dev/github.com/go-telegram/bot)
- NanoClaw `claw` CLI as prior art for the CLI surface
- NanoClaw `src/ipc.ts` task ops as prior art for cross-chat dispatch


### Provider continuity (D030)

A confirmed `/provider` switch waits for the chat's active agent, preserves
both harnesses' native sessions, resets the model override, and persists the
new provider. Each thread receives only its own bounded recent transcript on
its first turn with a different provider; main is a separate conversation.
`/clear` invalidates all providers' session mappings and the neutral transcript
for that conversation only. Shared memory and workspace are retained. Full
native tool history is not migrated. Automatic quota fallback is deferred.


### Quota switch confirmation (D031)

A known provider allowance rejection offers a single-use switch-and-retry
confirmation in the failed turn's own topic. It never changes a provider
unattended. The hidden `provider.fallback` command has no slash/menu/help entry;
its Yes rechecks registration, chat/folder/thread, a durable quota offer, and
whether the expected provider is still selected. The provider changes for the
whole chat, then the original request is retried once in its original topic
with durable identity `fallback:<turn id>`. Voice/file input metadata is kept.

Cancel/expiry do nothing. If the target is also quota-limited, report it without
another switch offer. A switch failure does not enqueue; a queue failure after
a successful switch explicitly asks the user to resend. Restart forgets button
tokens; a crash between provider change and retry insertion can require a manual
resend. Existing native sessions and bounded context remain as in D030.

## Telegram capability diagnostics (CP-017 initial release)

In registered private chats, `/telegram` returns a cache-only availability summary.
The configured owner chat can use `/telegram doctor` for details and
`/telegram doctor refresh` for a bounded getMe refresh (5-second deadline,
30-second cooldown; failures back off up to five minutes). The report separates
Telegram settings, local implementation and subscriptions; unknown/stale is not
disabled. It neither changes BotFather nor probes models/containers.

`/help guest`, `/help inline`, `/help topics` are deterministic private-chat help.
All these new routes silently ignore group/channel/unknown contexts until CP-019
private service delivery exists. Doctor uses hidden `telegram.doctor` through
owner-only Router dispatch; a human owner in another chat has no privilege.
Menus advertise `/telegram` in private chats only. No guest/inline/business
handler or additional update subscription is enabled.
