# gopod — Streaming Output

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [HANDOFF.md](HANDOFF.md)
> Status & next steps: [../ROADMAP.md](../ROADMAP.md) milestone ST1–ST5

Streaming agent output to Telegram in real time. Replaces the current
buffered model where the user waits 30–60 seconds of silence, then
gets a wall of text.

---

## 1. Problem

Current data flow (`docker.Exec` → buffer → send):

```
User sends message
  → 👀 reaction + "typing..." indicator
  → docker exec: claude -p "..."
  → [30-60 seconds of silence]
  → stdcopy.StdCopy into bytes.Buffer (BLOCKS until agent exits)
  → sendFormatted → one or more SendMessage calls
```

The user has no feedback beyond "typing..." for the entire agent run.
This is the #1 UX problem.

---

## 2. Target UX

```
User sends message
  → 👀 reaction
  → bot sends placeholder message: "▍" (cursor)
  → [~2s] placeholder edited to: "The answer is..."
  → [~1s] edited to: "The answer is that Go's io.Pipe..."
  → [~1s] edited to: "The answer is that Go's io.Pipe provides..."
  → ...partial updates every ~1 second...
  → [agent exits] final edit with full formatted text (HTML)
  → 👍 reaction
```

Key properties:
- First visible text within 2–3 seconds of agent start
- Updates every ~1 second (configurable)
- Markdown→HTML conversion on final edit only (partials are plain text)
- If response exceeds Telegram's 4096 char limit, stop editing and
  send continuation messages
- Voice/TTS still waits for completion (can't TTS a partial response)
- Graceful fallback: if streaming fails, buffer and send as before

---

## 3. Architecture

### 3.1 Layer changes

```
                      BEFORE                          AFTER
                    ─────────                       ─────────
Docker.Exec()       → ExecResult (buffered)         Docker.ExecStream()  → StreamHandle
Runner.Run()        → (string, error)               Runner.RunStream()   → *AgentStream
handler.runAgentSync()                               handler.runAgentStreaming()
  → waits, sends one reply                            → sends placeholder, edits in loop
```

### 3.2 `Docker.ExecStream` — streaming exec

```go
// StreamHandle provides streaming access to a running docker exec.
// The caller reads stdout/stderr via io.Reader; the demuxer runs
// in a background goroutine.
type StreamHandle struct {
    Stdout io.ReadCloser  // demuxed stdout stream
    Stderr io.ReadCloser  // demuxed stderr stream
    Done   <-chan error    // closed when exec finishes (nil = success)
    cancel func()         // cancels the exec context
}

// ExecStream runs cmd inside the container and returns a StreamHandle
// for reading output as it arrives. The caller MUST call Close() when
// done (or on error) to release the exec resources.
func (d *Docker) ExecStream(
    ctx context.Context,
    containerID string,
    cmd []string,
    env []string,
) (*StreamHandle, error)
```

**Implementation:**

```go
func (d *Docker) ExecStream(ctx context.Context, containerID string, cmd, env []string) (*StreamHandle, error) {
    created, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
        Cmd: cmd, Env: env,
        AttachStdout: true, AttachStderr: true,
    })
    if err != nil {
        return nil, fmt.Errorf("docker: exec create: %w", err)
    }

    resp, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
    if err != nil {
        return nil, fmt.Errorf("docker: exec attach: %w", err)
    }

    stdoutR, stdoutW := io.Pipe()
    stderrR, stderrW := io.Pipe()
    done := make(chan error, 1)

    go func() {
        defer resp.Close()
        defer stdoutW.Close()
        defer stderrW.Close()

        // stdcopy.StdCopy demuxes Docker's multiplexed stream.
        // As Docker flushes frames, data flows to the pipes immediately.
        _, err := stdcopy.StdCopy(stdoutW, stderrW, resp.Reader)
        if err != nil && !errors.Is(err, io.EOF) {
            done <- err
        }

        // Wait for exit code.
        for {
            info, err := d.cli.ContainerExecInspect(ctx, created.ID)
            if err != nil {
                done <- err
                return
            }
            if !info.Running {
                if info.ExitCode != 0 {
                    done <- &ExecError{Code: info.ExitCode}
                }
                close(done)
                return
            }
            time.Sleep(50 * time.Millisecond)
        }
    }()

    return &StreamHandle{
        Stdout: stdoutR, Stderr: stderrR,
        Done: done,
    }, nil
}
```

`stdcopy.StdCopy` writes to `io.PipeWriter` as each Docker frame
arrives (typically 1–8 KB). The pipe bridges the demuxer goroutine
to the caller's read loop without buffering the entire output.

### 3.3 `Runner.RunStream` — streaming agent run

```go
// AgentStream is the handle callers use to consume streaming output.
type AgentStream struct {
    Chunks <-chan string  // text chunks as they arrive
    Result <-chan RunResult // final result (reply, error) after agent exits
}

type RunResult struct {
    FullText string // complete stdout
    Stderr   string
    Err      error
}

// RunStream is the streaming variant of Run. It ensures the container,
// starts the agent, and returns an AgentStream immediately. The caller
// reads from Chunks in a loop, then reads Result for the final outcome.
func (r *Runner) RunStream(
    ctx context.Context,
    chatFolder string,
    tier Tier,
    allowlist *mountsec.Allowlist,
    prompt string,
) (*AgentStream, error)
```

**Implementation sketch:**

```go
func (r *Runner) RunStream(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist, prompt string) (*AgentStream, error) {
    id, err := r.Ensure(ctx, chatFolder, tier, allowlist)
    if err != nil {
        return nil, err
    }
    r.touch(chatFolder)

    // Build command with memory context (same as Run).
    var systemPrompt string
    if r.memory != nil {
        systemPrompt = r.memory.CompileContext(ctx, chatFolder, prompt)
    }
    prov := r.ProviderForChat(chatFolder)
    cmd := prov.RunCmd(prompt, systemPrompt)

    // Restore config.
    if restoreCmd := prov.RestoreConfigCmd(); restoreCmd != nil {
        r.d.Exec(ctx, id, restoreCmd, nil)
    }

    handle, err := r.d.ExecStream(ctx, id, cmd, nil)
    if err != nil {
        return nil, err
    }

    chunks := make(chan string, 32)
    result := make(chan RunResult, 1)

    go func() {
        defer close(chunks)
        defer close(result)
        defer handle.Close()

        var full strings.Builder
        buf := make([]byte, 4096)

        for {
            n, err := handle.Stdout.Read(buf)
            if n > 0 {
                text := string(buf[:n])
                full.WriteString(text)
                chunks <- text
            }
            if err != nil {
                break // EOF or error
            }
        }

        // Drain stderr.
        stderrBytes, _ := io.ReadAll(handle.Stderr)

        // Wait for exit.
        var execErr error
        if err := <-handle.Done; err != nil {
            execErr = err
        }

        r.touch(chatFolder)
        fullText := strings.TrimSpace(full.String())
        stderr := strings.TrimSpace(string(stderrBytes))

        // Classify auth errors.
        if execErr != nil {
            if ee, ok := execErr.(*ExecError); ok && ee.Code != 0 {
                if prov.IsNotLoggedInError(stderr, fullText) {
                    result <- RunResult{Err: r.classifyAuthError(ctx, id)}
                    return
                }
                result <- RunResult{Err: fmt.Errorf("agent exited %d", ee.Code), Stderr: stderr}
                return
            }
        }

        result <- RunResult{FullText: fullText, Stderr: stderr}

        // Post-run: extraction, compact (same as Run).
        if r.memory != nil && r.extractFn != nil && fullText != "" {
            go r.extractAndIngest(context.Background(), chatFolder, prompt, fullText)
        }
        turns := r.incrementTurnCount(chatFolder)
        if r.compactAfter > 0 && turns >= r.compactAfter {
            r.resetTurnCount(chatFolder)
            go r.CompactSession(context.Background(), chatFolder, tier, allowlist)
        }
    }()

    return &AgentStream{Chunks: chunks, Result: result}, nil
}
```

### 3.4 `handler.runAgentStreaming` — Telegram streaming loop

The heart of the UX change. Replaces `runAgentSync`.

```go
func (b *Bot) runAgentStreaming(ctx context.Context, item queue.Item) {
    tier := tierFromItem(item)
    stopTyping := b.startTyping(ctx, item.ChatID)
    defer stopTyping()

    stream, err := b.runner.RunStream(ctx, item.Folder, tier, b.allowlist, item.Text)
    if err != nil {
        b.handleRunError(ctx, item, err)
        return
    }

    // Send placeholder message → get its message_id for editing.
    placeholder := b.sendPlaceholder(ctx, item.ChatID, item.MessageID)
    if placeholder == 0 {
        // Fallback: drain stream, send as buffered.
        b.drainAndSend(ctx, item, stream)
        return
    }

    var accumulated strings.Builder
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()

    dirty := false      // true if accumulated has new data since last edit
    overflow := false    // true if we've exceeded single-message limit
    const editLimit = 4000 // safe limit under Telegram's 4096

    for {
        select {
        case chunk, ok := <-stream.Chunks:
            if !ok {
                goto done // channel closed, agent exited
            }
            accumulated.WriteString(chunk)
            dirty = true

            // If accumulated text exceeds edit limit, flush as
            // a new message and start a fresh accumulator.
            if accumulated.Len() > editLimit {
                b.editMessage(ctx, item.ChatID, placeholder,
                    accumulated.String())
                // Send what we have and continue in a new message.
                overflow = true
                placeholder = b.sendPlaceholder(ctx, item.ChatID, 0)
                accumulated.Reset()
                dirty = false
            }

        case <-ticker.C:
            if dirty && accumulated.Len() > 0 {
                text := accumulated.String() + "▍" // blinking cursor
                b.editMessage(ctx, item.ChatID, placeholder, text)
                dirty = false
            }

        case <-ctx.Done():
            return
        }
    }

done:
    // Final result.
    res := <-stream.Result
    if res.Err != nil {
        b.editMessage(ctx, item.ChatID, placeholder, "")
        b.handleRunError(ctx, item, res.Err)
        return
    }

    // Final edit with properly formatted text.
    finalText := res.FullText
    if finalText == "" {
        finalText = "(empty reply)"
    }

    // Replace the placeholder with the formatted final response.
    // If overflow happened, only format the last chunk. Earlier
    // chunks were already sent as plain text edits.
    html := markdownToTelegramHTML(finalText)
    if !overflow {
        // Simple case: everything fits in one message.
        b.editMessageHTML(ctx, item.ChatID, placeholder, html)
    } else {
        // Overflow: finalize last placeholder, then send formatted
        // version as a new reply (with full text).
        b.editMessage(ctx, item.ChatID, placeholder,
            accumulated.String())
        b.replyTo(ctx, item.ChatID, item.MessageID, finalText)
    }

    if item.MessageID > 0 {
        b.react(ctx, item.ChatID, item.MessageID, emojiDone)
    }

    // Voice reply (must wait for full text).
    b.maybeVoiceReply(ctx, item, finalText)
}
```

### 3.5 Telegram API methods needed

```go
// sendPlaceholder sends a "▍" message and returns its message_id.
// replyTo is the user's message id (0 = no threading).
func (b *Bot) sendPlaceholder(ctx context.Context, chatID int64, replyTo int) int

// editMessage edits a previously sent message (plain text).
func (b *Bot) editMessage(ctx context.Context, chatID int64, msgID int, text string)

// editMessageHTML edits a previously sent message (HTML parse mode).
func (b *Bot) editMessageHTML(ctx context.Context, chatID int64, msgID int, html string)
```

All use `go-telegram/bot`'s `EditMessageText` method. Errors are
logged but not propagated — a failed edit just means the user sees
a slightly stale partial; the next edit or the final message fixes it.

---

## 4. Rate limiting and edge cases

### 4.1 Telegram editMessage rate limits

Telegram allows ~30 `editMessageText` calls per second globally
(across all chats), and recommends no more than 1 edit/second per
message. The 1-second ticker in §3.4 respects this naturally.

If an edit returns 429 (Too Many Requests), back off for the
`retry_after` duration and skip that edit cycle. The next tick will
catch up.

### 4.2 Empty output

If the agent produces no stdout (e.g. crashes immediately), the
placeholder is edited to the error message or deleted.

### 4.3 Very fast responses

If the agent exits before the first tick fires, skip the edit loop
entirely and send the final formatted message. This avoids a
visible "▍" → final text flicker for sub-second responses.

### 4.4 Very long responses

If the response exceeds 4000 chars:
1. Edit the current placeholder to its current content (no cursor)
2. Send a new placeholder message
3. Continue accumulating into the new placeholder
4. Repeat if needed (rare — most LLM responses are under 8K)

On final: if overflow happened, send the full formatted response as
a new message (replacing the plain-text partials with proper HTML).

### 4.5 Concurrent edits

The `runAgentStreaming` goroutine is the only writer for a given
placeholder message. No concurrent edit conflicts. The queue ensures
one agent run per chat at a time.

### 4.6 Claude Code tool output

`claude -p` streams its final text response to stdout. Internal tool
calls (Read, Write, Bash, etc.) happen inside Claude Code and their
output is NOT part of stdout — only the final assistant response is.
This means streaming stdout directly is correct: the user sees the
answer being composed, not tool internals.

Exception: if Claude Code prints progress messages to stdout (e.g.
"Reading file..."), those will appear in the stream. This is
acceptable — it gives the user visibility into what the agent is
doing.

### 4.7 Fallback to buffered mode

If `ExecStream` fails or the Telegram placeholder can't be sent,
fall back to `runAgentSync` (current behavior). Streaming is an
enhancement, not a hard requirement.

### 4.8 Voice replies

TTS requires the full response text. Voice replies always wait for
the stream to complete, then synthesize. Text streaming and voice
are not mutually exclusive — the user sees streaming text AND gets
a voice message at the end (if voice mode is active).

---

## 5. Config

| Env var | Default | Description |
|---------|---------|-------------|
| `GOPOD_STREAM_ENABLED` | `true` | Enable streaming output. Set to `false` to use buffered mode |
| `GOPOD_STREAM_INTERVAL` | `1s` | How often to edit the Telegram message with new content |
| `GOPOD_STREAM_CURSOR` | `▍` | Cursor character appended to partial messages |

---

## 6. Implementation plan

See [ROADMAP.md](../ROADMAP.md) Phase 7 (ST1–ST5).

| Step | What | Depends on | Files |
|------|------|------------|-------|
| ST1 | `Docker.ExecStream` — streaming exec with `io.Pipe` + `stdcopy` demux | — | `internal/runner/docker.go` |
| ST2 | `Runner.RunStream` — streaming run with chunk channel + result channel | ST1 | `internal/runner/runner.go` |
| ST3 | Telegram `editMessage` / `sendPlaceholder` helpers | — | `internal/telegram/handler.go` |
| ST4 | `runAgentStreaming` — streaming handler loop with ticker, overflow, fallback | ST2, ST3 | `internal/telegram/handler.go` |
| ST5 | Wire into queue handler, config flag, integration test | ST4 | `internal/telegram/handler.go`, `cmd/gopod/main.go`, `internal/config/config.go` |

ST1 and ST3 are independent and can be built in parallel.

---

## 7. What this changes in existing code

| File | Change |
|------|--------|
| `internal/runner/docker.go` | Add `ExecStream`, `StreamHandle`, `ExecError` |
| `internal/runner/runner.go` | Add `RunStream`, `AgentStream`, `RunResult` |
| `internal/telegram/handler.go` | Add `runAgentStreaming`, `sendPlaceholder`, `editMessage`, `editMessageHTML`. Modify `NewAgentHandler` to call streaming path. `runAgentSync` preserved as fallback |
| `internal/config/config.go` | Add `StreamEnabled`, `StreamInterval`, `StreamCursor` fields |
| `cmd/gopod/main.go` | Pass stream config to telegram Bot |

`runAgentSync` is NOT removed — it becomes the fallback path for:
- `GOPOD_STREAM_ENABLED=false`
- `ExecStream` failure
- Placeholder send failure
- `RunFresh` calls (system tasks, compact — no Telegram message to edit)
