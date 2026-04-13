# gopod — Easy-port integrations from NanoClaw

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [MEMORY.md](MEMORY.md) · [DECISIONS.md](DECISIONS.md) · [GLOSSARY.md](GLOSSARY.md)
> Status & next steps: [HANDOFF.md](HANDOFF.md) · [../ROADMAP.md](../ROADMAP.md)

A triage of NanoClaw skills/integrations by how cheaply they map to Go + Telegram.
"Easy" = no new heavy deps, < ~200 LOC, no architectural change. "Medium" = real
work but contained. "Skip" = out of scope or already covered by Telegram natively.

Telegram attachment model (used by 1–4 below):

- Photos:    `update.Message.Photo []PhotoSize` → `bot.GetFile(FileID)` → URL → download
- Voice:     `update.Message.Voice` (OGG/Opus, mime `audio/ogg`)
- Audio:     `update.Message.Audio` (mp3/m4a, larger files)
- Documents: `update.Message.Document` (PDF, txt, anything; mime in `Document.MimeType`)
- Video:     `update.Message.Video`
- Stickers:  `update.Message.Sticker`

All of them resolve via the same `bot.GetFile` → `https://api.telegram.org/file/bot<TOKEN>/<file_path>`
download flow, so a single `internal/attachments` package handles them all.

---

## Tier 1 — port immediately (easy wins)

### 1. Image vision  ✅ trivial
**Source:** `add-image-vision`
**NanoClaw model:** download from WhatsApp → resize via `sharp` → base64 → pass as
multimodal content block to Claude.

**Go port:**
- Telegram: pick the largest `PhotoSize`, `bot.GetFile`, download.
- Resize: optional. anthropic-sdk-go accepts images up to 5 MB / 8000×8000. Skip
  resize for v0; add `golang.org/x/image` later if needed.
- anthropic-sdk-go has `anthropic.NewImageBlockBase64(mediaType, data)`.
  Multimodal goes straight into the `Messages.New` call.
- character-ai SDK exposes the same via its tool/message types.

**Effort:** ~80 LOC in `internal/attachments/image.go` + a hook in
`internal/telegram/handler.go`. No new deps.

### 2. Voice transcription (OpenAI Whisper)  ✅ trivial
**Source:** `add-voice-transcription`
**NanoClaw model:** download voice note → POST to
`https://api.openai.com/v1/audio/transcriptions` → prepend `[Voice: ...]` to text.

**Go port:** identical flow.
- Whisper accepts OGG/Opus directly — no FFmpeg needed.
- Pure `net/http` + `mime/multipart`. No SDK required.
- Add `OPENAI_API_KEY` to config.

**Effort:** ~120 LOC in `internal/attachments/voice.go`. No new deps.

### 3. Local Whisper (whisper.cpp / faster-whisper server)  ✅ trivial
**Source:** `use-local-whisper`
**NanoClaw model:** swap OpenAI endpoint for a local HTTP server (whisper.cpp's
`server` binary or `faster-whisper-server`).

**Go port:** same code as #2 with a configurable base URL:
```
GOPOD_WHISPER_URL=http://localhost:8080/inference   # local
# or empty → falls back to https://api.openai.com
```
Single env var unlocks both.

**Effort:** 1 line on top of #2.

### 4. PDF reader  ✅ easy
**Source:** `add-pdf-reader`
**NanoClaw model:** PDFs forwarded as attachments → saved to chat workspace →
agent calls `pdftotext` (poppler-utils) inside the container.

**Go port — two options:**

(a) **Container-side, like NanoClaw:** install `poppler-utils` in the gopod
agent container, drop a small `container/tools/pdf-reader` shim. Agent calls it
when it sees a PDF in the workspace. **Pro:** matches NanoClaw exactly. **Con:**
bigger image, agent has to invoke the tool explicitly.

(b) **Host-side extraction at receive time:** when a PDF arrives, extract text
on the host using [`github.com/ledongthuc/pdf`](https://github.com/ledongthuc/pdf)
or shell out to `pdftotext`, then inject `[PDF "<filename>": <first-N-chars>]`
into the prompt. **Pro:** zero agent action needed; works without container.
**Con:** loses page-by-page navigation.

**Recommendation:** ship (b) in v0 for simplicity (no extra deps if we shell
out; one tiny pure-Go dep if we don't), add (a) when we need page navigation.

**Effort:** ~100 LOC.

### 5. Reactions (Telegram-native)  ✅ trivial
**Source:** `add-reactions` (WhatsApp)
**Mapping:** Telegram has a real reaction API. `bot.SetMessageReaction(ctx,
&SetMessageReactionParams{ChatID, MessageID, Reaction: []ReactionType{...}})`.
The agent can react to a user's message with 👀 ("seen, working"), ✅ ("done"),
or ❌ ("error") — exactly the UX NanoClaw uses to acknowledge messages without
spamming the chat.

**Go port:** wire two helpers in `internal/telegram/send.go`:
```go
func (b *Bot) ReactSeen(ctx, chatID, msgID) error    // 👀
func (b *Bot) ReactDone(ctx, chatID, msgID) error    // ✅
func (b *Bot) ReactError(ctx, chatID, msgID) error   // ❌
```
Called automatically by the runner: `ReactSeen` on enqueue, `ReactDone` on
success, `ReactError` on failure.

**Effort:** ~30 LOC. No new deps.

### 6. Typing indicator  ✅ trivial
**Source:** part of `Channel.setTyping?` in NanoClaw's channel interface
**Go port:** `bot.SendChatAction(ctx, &SendChatActionParams{ChatID, Action:
models.ChatActionTyping})`. Telegram clears it automatically after 5s, so a
goroutine refreshes it every 4s while the runner is busy.

**Effort:** ~25 LOC.

### 7. Channel formatting (Markdown → Telegram MarkdownV2)  ✅ easy
**Source:** `channel-formatting`
**NanoClaw model:** Claude emits Markdown; channels convert to native syntax.
**Go port:** Telegram MarkdownV2 needs escaping of `_*[]()~`>#+-=|{}.!`. Two
levels:

(a) **Plain text** (simplest): strip Markdown to plain. Lossy but never breaks.
(b) **MarkdownV2 renderer:** parse with
[`github.com/yuin/goldmark`](https://github.com/yuin/goldmark), walk the AST,
emit MarkdownV2 with proper escaping. ~150 LOC.

Recommendation: ship (a) in v0 (just escape everything), upgrade to (b) when
formatting noise becomes annoying.

**Effort:** ~30 LOC for (a), ~150 LOC + 1 dep for (b).

### 8. Compact command  ✅ trivial
**Source:** `add-compact`
**NanoClaw model:** `/compact` slash command forwards to Claude SDK's built-in
`/compact`, accessible only from the owner chat / trusted senders.

**Go port:** `bot.RegisterHandler(HandlerTypeMessageText, "/compact",
MatchTypeCommand, handler)`. Handler checks `chatID == ownerChatID || isOwnerChat`,
then writes a `compact` task file into the chat's `data/ipc/<folder>/input/`
that the agent runner picks up.

**Effort:** ~40 LOC.

### 9. Karpathy LLM Wiki  ✅ trivial (no code)
**Source:** `add-karpathy-llm-wiki`
**NanoClaw model:** a per-group `wiki/` folder pattern + CLAUDE.md instructions
that teach the agent to maintain a knowledge base over time.

**Go port:** pure convention. Add a `chats/<folder>/wiki/` directory at chat
registration, append the wiki instructions to the templated `CLAUDE.md`. Zero
runtime code.

**Effort:** ~10 LOC at registration time + a markdown template.

### 10. Telegram Agent Swarm (multi-bot teams)  ✅ already half-done
**Source:** `add-telegram-swarm`
**NanoClaw model:** subagents posing as different bots in the same group, each
with its own bot token, so the chat looks like a team conversation.

**Go port:** gopod is already Telegram-only — this is just a bot-pool config:
`GOPOD_BOT_TOKENS=token1,token2,token3` mapped to subagent identities. The
runner picks which token to send through based on the subagent that emitted the
message. No new architecture.

**Effort:** ~80 LOC in `internal/telegram/bot.go` to manage a pool. Pure
configuration after that.

---

## Tier 2 — medium effort, worth doing soon

### 11. Ollama tool  🟡 medium
**Source:** `add-ollama-tool`
**NanoClaw model:** MCP server exposing Ollama's local models as tools.

**Go port options:**

(a) **MCP server in Go:** the character-ai SDK supports MCP servers. Run a
small Go MCP server that proxies to Ollama's `/api/chat`. Agent gets `ollama`
as a callable tool. Cleanest.

(b) **Simple HTTP tool:** define a Claude tool `query_ollama(model, prompt)`
that the host implements via `net/http` POST to `localhost:11434/api/chat`.
Skips MCP entirely. ~60 LOC.

Recommendation: (b) for v0.

**Effort:** ~80 LOC + tool registration.

### 12. Gmail (as a *tool*, not a channel)  🟡 medium
**Source:** `add-gmail`
**NanoClaw model:** can be either a channel (emails trigger the agent) or a
tool (agent reads/sends mail when prompted).

**Go port:** ship the **tool** mode only — channel mode duplicates the message
loop architecture for one extra protocol and isn't worth the complexity for
gopod v0. Use [`google.golang.org/api/gmail/v1`](https://pkg.go.dev/google.golang.org/api/gmail/v1)
+ `golang.org/x/oauth2/google`. Register tools `gmail_search`, `gmail_read`,
`gmail_send` via the agent SDK's tool registry.

**Effort:** ~300 LOC + OAuth setup flow + 2 deps.

### 13. X (Twitter) integration  🟡 medium
**Source:** `x-integration`
**Go port:** [`github.com/g8rswimmer/go-twitter`](https://github.com/g8rswimmer/go-twitter)
or `github.com/dghubble/oauth1` + raw HTTP. Tools: `x_post`, `x_reply`,
`x_like`, `x_retweet`. Same shape as Gmail-as-tool.

**Effort:** ~250 LOC + 1 dep.

### 14. Parallel AI integration  🟡 medium
**Source:** `add-parallel`
**Go port:** Parallel exposes an HTTP API. Wrap it as a Claude tool. ~150 LOC.

---

## Tier 3 — skip for v0

| Skill                       | Reason                                          |
| --------------------------- | ----------------------------------------------- |
| `add-whatsapp`              | Out of scope (Telegram only)                    |
| `add-slack`                 | Out of scope                                    |
| `add-discord`               | Out of scope                                    |
| `add-emacs`                 | Out of scope                                    |
| `add-signal` (implied)      | Out of scope                                    |
| `convert-to-apple-container`| gopod uses Docker SDK directly               |
| `add-macos-statusbar`       | Not wanted                                      |
| `init-onecli`               | Replaced by env-var injection                   |
| `use-native-credential-proxy` | Same — no OneCLI to replace                   |
| `migrate-from-openclaw`     | Operational, NanoClaw-specific                  |
| `migrate-nanoclaw`          | Operational                                     |
| `update-nanoclaw`           | Operational                                     |
| `update-skills`             | No skills system in gopod                    |
| `qodo-pr-resolver`          | Dev workflow, not a runtime feature             |
| `get-qodo-rules`            | Dev workflow                                    |
| `claw` CLI                  | NanoClaw-specific harness                       |
| `setup`, `customize`, `debug` | Operational/instructional                     |

---

## Suggested integration order (after M0–M8 in [ARCHITECTURE.md](ARCHITECTURE.md) / [ROADMAP](../ROADMAP.md))

| Milestone | Adds                                                             |
| --------- | ---------------------------------------------------------------- |
| **I1**    | Reactions (#5) + Typing indicator (#6) — instant UX upgrade      |
| **I2**    | Image vision (#1) — first multimodal capability                  |
| **I3**    | Voice transcription #2 + Local Whisper #3 (one PR, one toggle)   |
| **I4**    | PDF reader (#4)                                                  |
| **I5**    | Channel formatting (#7) + Compact command (#8)                   |
| **I6**    | Karpathy wiki convention (#9)                                    |
| **I7**    | Telegram swarm (#10) — multi-bot teams                           |
| **I8**    | Ollama tool (#11)                                                |
| **I9**    | Gmail tool mode (#12)                                            |
| **I10**   | X tool (#13) + Parallel (#14) as needed                          |

I1–I6 all together are roughly **600–900 LOC** with **1 optional dep**
(`yuin/goldmark` for the MarkdownV2 renderer). They cover ~80% of NanoClaw's
day-to-day "feels alive" features.
