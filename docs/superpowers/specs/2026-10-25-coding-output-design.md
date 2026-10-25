# Coding tasks in Telegram: what the user sees

Status: approved direction by the owner (2026-10-25); details below are the
controller's calls unless marked as the owner's.

## Intent

Coding work in a chat should not dump walls of code into Telegram, which is
a poor place to read code. The user wants to see *progress* while the agent
works and a *short summary* at the end; code only on request.

Today (measured on Claude Code 2.1.289): the agent runs `claude -p` in text
mode, which prints only the final answer, all at once at the end. During the
work the user sees a typing indicator and a bare cursor in the draft. The
final answer goes through `markdownToTelegramHTML` and is chunked at 4000
bytes, so a code-heavy answer becomes a series of messages. gopod cannot
send files.

## Owner decisions

- Do items 1 and 2 first, item 3 as a separate piece of work, in parallel if
  the diffs do not conflict.

## Parts

### 1. Output policy (every chat, every conversation)

A constant in the runner appended to the system prompt on every turn (the
per-chat `CLAUDE.md` is never overwritten, so a template change alone would
not reach existing chats). Content, in short: do not paste code, diffs or
long command output into the answer; end a work turn with 3-6 lines (what
changed, which files, test result, what is left); show code only when asked
or when it is at most ~15 lines; say which file contains the long part.
Also update `internal/runner/chattmpl/CLAUDE.md.tmpl` for new chats.

### 2. Progress instead of code (Claude first)

- Run Claude with `--output-format stream-json --verbose
  --include-partial-messages` and parse the events (system init, assistant
  tool_use blocks, partial text deltas, tool_result, result with the final
  text and `is_error`). The builder confirms exact flags and event shapes
  against `claude --help` and a real recorded run (container image
  `gopod-agent`, Claude Code 2.1.289) and stores recorded fixtures under
  `testdata/`.
- Provider seam: an optional structured-output capability on the provider;
  Claude implements it, Codex stays on plain text for now (non-goal: Codex
  `--json`). `AgentStream` keeps `Chunks` for *answer text* and gains a
  progress channel of tool events (kind, tool name, a short detail).
- Draft content: while no answer text has arrived, show the last five tool
  steps, one line each, at most ~60 characters, never file contents or
  command output: Bash -> command (secrets redacted with the existing
  redaction patterns), Edit/Write -> path, Read -> path, Grep/Glob ->
  pattern, WebSearch/WebFetch -> query or URL, other -> tool name. Once
  answer text deltas arrive, the draft shows the answer text as today. The
  edit path (groups, drafts refused) shows the same lines in the
  placeholder and replaces them with the final answer.
- The final reply is the `result` text of the run; everything downstream is
  unchanged: Stop and process kill (D021), session capture, durable turns,
  thread routing, memory ingestion (uses the result text), auth error
  classification (`IsNotLoggedInError` must still see "Failed to
  authenticate" when it arrives as a JSON result or on stderr).
- Setting `GOPOD_PROGRESS=0` falls back to today's plain text mode.

### 3. Long code is collapsed or attached (separate piece)

- In the final reply, a fenced code block longer than 12 lines becomes
  `<blockquote expandable><pre>...</pre></blockquote>` (Telegram collapses
  expandable quotes by default). The builder checks the Bot API formatting
  rules (nesting of `pre` inside a quote) and keeps the existing
  plain-text fallback when Telegram rejects the markup.
- The HTML chunker must not split inside `pre`/`blockquote`: close and
  reopen the tags across chunks.
- A block over 60 lines (or over ~3000 characters) is sent as a document
  (`sendDocument`, file name from the fence language, e.g. `snippet.py`) and
  replaced in the text by a one-line note with the line count. New
  document-send helper honours threads and the topic-gone fallback
  (`threadgone.go`). Only the final-reply path does this; command replies
  keep `sendFormatted` as is.

## Non-goals

Codex progress parsing, inline keyboards ("show diff" button), `git diff
--stat` summaries, routing coding tasks to soft-ai-factory, rich messages.
These are listed as follow-ups.

## Anti-goals

Leaking command output, file contents or secrets into the draft; a stream
parser that loses the final answer when an event is malformed (fall back to
text); breaking Stop, crash recovery or thread routing.

## Done means

Unit tests with recorded fixtures for the parser and the formatter; build,
vet and tests green; a manual check on a real turn: progress lines appear,
the final answer is short, a long code block is collapsed/attached.
