# Agent CLI hooks: Claude Code and Codex, and what gopod could move onto them

| | |
|---|---|
| Date | 2026-10-30 |
| Status | documentation survey and research plan for [CP-035](../proposals/CP-035-agent-cli-hooks-research.md); nothing built, enabled or live-tested |
| Question | What do the hook systems of Claude Code and Codex offer, and which gopod behaviors inside the agent containers could be moved onto them without weakening isolation? |
| Evidence | Official docs read 2026-10-30 by two research passes: Claude (code.claude.com hooks reference and guide) and Codex (learn.chatgpt.com hooks and config reference, openai/codex issues and PRs). Both passes used a summarizing fetcher, so quoted phrases are not raw page text and fields marked *[summary]* were not seen verbatim. gopod source read at `8bc6b5d`. No CLI was run. |
| Versions | Docs describe current releases. gopod pins Claude Code 2.1.310 and Codex 0.164.0 (`container/README.md`). Every "documented" below is "documented for a newer or unspecified version" until E1/E2 pass on the pinned versions. |

## 1. Conclusion

Both CLIs have a documented, scriptable hook system with a blocking `PreToolUse`, context injection at
session and prompt start, and a `Stop` gate that can force the agent to continue. Neither is a
security boundary by itself: hooks run **inside the container as the agent's user**, and today the
config directories are writable by the agent. A hook is trustworthy only when its configuration and
binary come from a layer the agent cannot write (inline `--settings` on the command line, managed
settings or `requirements.toml` baked into the image, a read-only mount).

Two facts change earlier gopod analysis:

1. The autonomy analysis ([2026-10-30 section 9](2026-10-30-codex-agent-autonomy-prioritization.md))
   called it undocumented whether a Claude `PreToolUse` hook fires under bypass. The hooks reference
   now says a hook `deny` "holds in every mode, including bypassPermissions and dontAsk" because
   `PreToolUse` runs before permission checks *[summary]*. That would give Claude a default-deny tool
   gate that deny rules cannot. It still needs the synthetic test (E1).
2. Codex hooks need **trust** before they run ("new or changed hooks are marked for review and skipped
   until trusted"). A fresh container `CODEX_HOME` has no persisted trust, and behavior under
   `codex exec` is not documented. This is the main risk for any Codex use (E2).

Recommendation: do not build anything yet. Run the nine experiments in section 6 (all synthetic, no
owner account, no Telegram), then decide per candidate using the criteria in section 5. The likely
first yields are a Claude default-deny `PreToolUse` gate and provider-neutral context injection;
the likely non-starter is anything that needs host authority.

## 2. Comparison

| | Claude Code | Codex |
|---|---|---|
| Events | 33: SessionStart/End, Setup, UserPromptSubmit/Expansion, Stop, StopFailure, PreToolUse, PermissionRequest/Denied, PostToolUse/Failure, PostToolBatch, SubagentStart/Stop, Task*, TeammateIdle, Pre/PostCompact, Pre/PostModelSwitch, Elicitation*, InstructionsLoaded, ConfigChange, CwdChanged, FileChanged, DirectoryAdded, Worktree*, Notification, MessageDisplay | 12: SessionStart/End, UserPromptSubmit, PreToolUse, PermissionRequest, PostToolUse, Pre/PostCompact, Stop, SubagentStart/Stop, Interrupt |
| Handler types | command, http, mcp_tool, prompt, agent (experimental); support varies per event | command, mcp_tool; `prompt` and `agent` parsed but skipped |
| Config | `hooks` in settings JSON: user, project, local, managed, plugin, skill/subagent frontmatter; inline via `--settings '<json>'` | `hooks.json` or `[hooks]` in `config.toml`: user and project layer; plugins; managed via `requirements.toml` (`allow_managed_hooks_only`) |
| Merge | all levels merge; identical handler runs once | all matching hooks from all layers run |
| Trust | headless (`-p`) never shows the workspace trust dialog and treats the folder as trusted; interactive holds hooks back until trusted | per-hook trust hash; untrusted hooks skipped; `--dangerously-bypass-hook-trust` for automation; `codex exec` behavior undocumented |
| Kill switch | `disableAllHooks: true` (also via `--settings`); `allowManagedHooksOnly` | `[features] hooks = false` (`codex_hooks` deprecated alias) |
| Block a tool call | `PreToolUse`: exit 2, or `permissionDecision: deny` (deny > defer > ask > allow across hooks) | `PreToolUse`: exit 2 or `permissionDecision: deny`; `ask`, `continue:false`, `stopReason` unsupported and make the hook *fail open* |
| Rewrite tool input | `updatedInput` *[summary]* (several rewriting hooks are not merged) | `updatedInput` with `allow`; issue #18491 reported it rejected, July comment says fixed |
| Rewrite tool output | `PostToolUse` `updatedToolOutput` *[summary]* | `PostToolUse` `decision:block` replaces the result with feedback |
| Inject context | `additionalContext` on SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, SubagentStart, Stop; plain stdout on some; >10,000 chars spills to a file with a 2,000 char preview *[summary]* | `additionalContext` or plain stdout on SessionStart, UserPromptSubmit, SubagentStart; about 2,500 tokens default, rest saved to disk |
| Stop gate | `Stop` `decision:block` + `reason` forces another turn; `stop_hook_active` input; cap 8 consecutive blocks | `Stop` `decision:block` turns `reason` into a new continuation prompt; JSON required, plain text invalid; `stop_hook_active` input |
| Failure default | fail open; `onFailure: "block"` (v2.1.295+) makes it fail closed | fail open for unsupported output; failure semantics otherwise undocumented |
| Tool coverage | all tools including MCP (`mcp__server__tool`) | Bash, unified exec, `apply_patch`, MCP, local function tools; **hosted tools such as `WebSearch` not covered** |
| Error events | `StopFailure` with typed `error` (rate_limit, authentication_failed, billing_error, ...) | none documented |
| Subagents | `SubagentStart/Stop` | `SubagentStart/Stop`; issue #49736: a `PreToolUse` deny on `spawn_agent` never runs |
| Timeouts | 600 s default; 30 s for UserPromptSubmit; async option | 600 s default; SessionEnd and Interrupt 1 s default, 3 s max; async option, up to 8 concurrent |
| Input | JSON on stdin; `session_id`, `transcript_path`, `cwd`, `permission_mode`, event fields | JSON on stdin; `session_id`, `transcript_path`, `cwd`, `model`, `turn_id`, event fields; transcript format "isn't a stable interface" |
| Known defects | none found in this pass | #27133 project `.codex/hooks.json` ignored in git worktrees (open); #49736 (open); #18491 (open, mostly resolved by comment); desktop-only reports #35863, #25293 unverified |
| Headless | hooks run in `-p`; `defer` works only in `-p`; `terminalSequence` ignored; `--bare` / `disableAllHooks` to skip | not documented for `codex exec` |

## 3. gopod today (source read)

- **Both CLIs run with permissions bypassed**: Claude `--dangerously-skip-permissions`
  (`provider_claude.go:45`), Codex `--dangerously-bypass-approvals-and-sandbox`
  (`provider_codex.go:39`). The container, mounts and env allowlist are the boundary.
- **Config directories are writable by the agent.** `~/.claude` and `~/.codex` are per-chat host
  directories bind-mounted into the container (`mounts.go:212-215`); `/home/node` is tmpfs. Any hook
  written to those files can be edited or removed by the agent in a later turn.
- **Groups already disable hooks on purpose.** The group Claude policy is
  `--settings {"disableClaudeAiConnectors":true,"disableAllHooks":true,"autoMemoryEnabled":false}
  --setting-sources ""` (`provider_group.go:211`); the Codex group profile sets `hooks` off
  (`experiments/subscription-isolation/codex-qualification.md:71`). `ISOLATION.md` lists "inherited
  MCP/plugins/hooks" as disabled. Any change must keep "no inherited hooks" true.
- **Memory** reaches Claude through `--append-system-prompt`; Codex has no such flag, so gopod
  prepends it to the prompt (`provider_codex.go:62`).
- **Progress and replies** come from parsing Claude `stream-json` (`provider_claude_stream.go`),
  which already carries `tool_use`, `tool_result` and system/hook events, and from Codex `--json`.
  Errors such as auth failure or rate limit are recognised from result text and exit status.
- **Images** have no `jq`; the personal and owner Dockerfiles install a fixed package list. A hook
  would be a small static binary or a script using only what the image has.
- **The repo's own `.claude/settings.json`** has one `SessionStart` hook (`handoff-status.sh`). That is
  developer-time tooling, a different scope from container hooks.

## 4. Candidates

"Host-side" means: keep doing it in gopod on the host from the event stream, outside the container.

| # | Candidate | Hook | Replaces / adds | First read |
|---|---|---|---|---|
| C1 | **Default-deny tool gate** for scoped runs (read-only proactive runs, non-owner scopes): allow listed tools by name, deny the rest including connector tools added later | Claude `PreToolUse` | Fills the fail-open gap in deny-by-name ([autonomy analysis](2026-10-30-codex-agent-autonomy-prioritization.md) conclusion 1). Codex already has an allowlist via `apps.<id>.tools` | **Strongest.** Needs E1, E3, E5 |
| C2 | **Path and command guard** in the owner container: block writes outside `/workspace`, `.env`, the agent's own `.claude`/`.codex` config; block destructive git | `PreToolUse` on Bash/Edit/Write/`apply_patch` | Defense in depth over mounts. Mounts remain the real boundary | Medium. Cannot cover Codex hosted tools |
| C3 | **Context injection** instead of `--append-system-prompt` / prompt prepend: memory, chat profile, time, quiet-hours state | Claude `SessionStart`+`UserPromptSubmit` `additionalContext`; Codex same | Gives Codex a real developer-context channel; removes the prompt-prepend hack | Medium. Codex limit about 2,500 tokens; resume and `--no-session-persistence` interplay unknown (E7) |
| C4 | **Provider-neutral tool events**: append every tool call and result summary to a per-run JSONL under `/workspace/ipc` | `PreToolUse` / `PostToolUse` | Same audit and progress source for Claude and Codex; feeds CP-013 provenance and CP-021 checkpoints | Low for Claude (`stream-json` already has it). Possible gain for Codex if `--json` lacks fields |
| C5 | **Completion gate**: a scheduled task may not end until it wrote its outcome file | `Stop` | Enforces CP-021 task outcomes inside the turn instead of failing afterwards | Medium. Loop risk (cap 8 on Claude); Codex needs JSON output |
| C6 | **Structured failure signal**: auth, rate limit, billing errors written as a typed file | Claude `StopFailure` | Replaces text matching of the result line | Low. Codex has no equivalent; stream result already carries `is_error` |
| C7 | **Output scrubber**: remove secret-shaped strings from tool output before the model sees it | Claude `PostToolUse updatedToolOutput` *[summary]*; Codex only by replacing the result | Extra layer for D012; the slog redactor does not cover model context | Low-medium. Regex scrubbing is best effort |
| C8 | **Pre-compaction flush** | `PreCompact` | Not needed: gopod owns compaction ([MEMORY.md:81](../MEMORY.md)) | Drop |
| C9 | **Developer-time repo hooks** for people (and agents) working on gopod: block force-push and push to `main`, `gofmt` after edits, `go test` on `Stop` | repo `.claude/settings.json`, Codex `.codex/hooks.json` | Enforces CLAUDE.md "never do" list mechanically | Separate small track. Codex project hooks broken in worktrees (#27133), which is gopod's normal flow, so use global `~/.codex/hooks.json` or Claude only |

Stay host-side, not hookable: authorization (path-based, Router `Perm`), anything needing a secret or
the Telegram token, outbound messages (outbox IPC), quota policy and provider switching, budgets, scheduling and quiet
hours decisions. A hook runs with the agent's rights and cannot call back into the host safely.

## 5. Decision criteria

A candidate moves onto a hook only if all hold:

1. It must act **inside** the agent loop (block, rewrite or inject before the model continues). If a
   host-side check on the event stream afterwards is enough, it stays host-side.
2. Its configuration and binary are delivered from a layer the agent cannot write, or it is purely
   advisory and the agent editing it away is harmless.
3. Its failure mode is acceptable. Security gates must fail **closed** (Claude `onFailure: "block"`);
   Codex has no documented equivalent, so a Codex hook is advisory unless E5 proves otherwise.
4. It is the same hook contract on both providers or the difference is explicit and small.
5. It adds no new secret to the container and no network path.
6. Groups keep "no inherited hooks": a host-injected hook is allowed only if E8 shows that inherited
   and project hooks stay disabled.

## 6. Experiment plan (all synthetic; needs owner go for E2-E9 because they run containers)

Setup for all: pinned images built locally, no real credentials (use the local synthetic API
advertiser pattern from `experiments/subscription-isolation/`), throwaway workspace, no Telegram.

| # | Question | Method | Pass means |
|---|---|---|---|
| E1 | Does Claude `PreToolUse` deny fire and hold under `--dangerously-skip-permissions` in `-p`, on 2.1.310, for built-in and MCP tools? | One run per tool class with a deny-all hook; model asked to call the tool | Call blocked, reason reaches the model, `stream-json` records it |
| E2 | Do Codex hooks run in `codex exec` with the bypass flag on 0.164.0, with an empty `CODEX_HOME`? What does untrusted-hook handling do (skip, fail, prompt)? Does `--dangerously-bypass-hook-trust` exist on 0.164? | Same deny-all hook; vary trust state and flag | A documented, repeatable way to make a baked hook run non-interactively, or a clear "not possible" |
| E3 | Which delivery channels are agent-proof? Claude: inline `--settings`, managed settings file in the image, `--setting-sources ""` interplay. Codex: `/etc/codex/requirements.toml` and `allow_managed_hooks_only`, project vs global | From inside the container, as uid 1000, try to edit/remove each hook source in a following turn | At least one channel per provider where the edit fails or has no effect |
| E4 | What is the per-call cost and container footprint of a hook (process spawn, binary size, tmpfs HOME, uid override, no `jq`)? | Time 100 tool calls with and without a no-op hook; try shell script vs static Go binary | Overhead small against a turn; chosen runtime documented |
| E5 | Fail-open vs closed: hook crashes, times out, prints junk, exits 1. Claude with and without `onFailure: "block"`; Codex default | Fault-injected hook | Claude closes with `block`; Codex behavior known and recorded |
| E6 | Coverage gaps: does the Claude gate see MCP tools, Task/subagent calls, WebFetch/WebSearch, Skill? Does the Codex gate see `apply_patch`, MCP, shell, and miss WebSearch (documented gap)? | Call each tool once under a deny-all hook | A coverage table per provider |
| E7 | Context injection: size limits, ordering, does it survive `--continue` / `--resume`, `--no-session-persistence`, compaction? Compare with today's `--append-system-prompt` | Inject a marker and ask the model to repeat it across the flows gopod uses | Marker appears exactly once per intended turn |
| E8 | Groups: with `--setting-sources ""` and `--settings` carrying one hook but not `disableAllHooks`, do user/project/plugin hooks stay off and the injected one run? Codex group profile with hooks on only from managed layer | Plant a canary hook in each ignored source | Only the injected hook fires |
| E9 | `Stop` gate: does a forced continuation work in `-p` and `codex exec`, what is the loop behavior at the cap, and what do gopod's stream parser and timeouts see? | Hook blocks until a file exists | Terminates; stream parser handles the extra turns |

Output of the research: a verdict table (move / keep host-side / defer) per candidate C1-C9 with the
experiment evidence, then at most one spec for the winners, then an ADR if a real architectural
choice is made (hooks as part of the container policy fingerprint, D044).

## 7. Risks to keep in view

- Docs track newer releases than the pins. A feature missing on 2.1.310 or 0.164.0 is a pin bump
  decision, not a design assumption.
- A hook that looks protective but fails open is worse than none. Record the failure mode of every
  hook next to it.
- Hooks widen the container policy surface: the effective-policy fingerprint (D044) would have to cover
  hook configuration, or a reused container could run a stale gate.
- Codex hooks "run outside of our sandboxing" (PR #13276 reviewer comment), and Stop hooks see the
  assistant output; a hook must not forward it anywhere.
- The Codex desktop-only reports (#35863, #25293) and #18067 were not read; do not treat them as
  settled either way.

## 8. Sources

Claude: https://code.claude.com/docs/en/hooks, https://code.claude.com/docs/en/hooks-guide.
Not fetched: settings precedence, headless, env-vars, permissions pages.
Codex: https://learn.chatgpt.com/docs/hooks, https://learn.chatgpt.com/docs/config-file/config-reference,
https://github.com/openai/codex/pull/13276, issues #14882, #16732, #18491, #27133, #49736.
Not fetched: config-reference page 2, `codex exec` hook behavior (no documentation found).
gopod: `internal/runner/provider_group.go`, `provider_claude.go`, `provider_codex.go`, `mounts.go`,
`container/README.md`, `docs/ISOLATION.md`, `docs/MEMORY.md`, the 2026-10-30 autonomy analysis.
