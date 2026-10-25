# runner testdata

## Claude Code stream-json (`claude_stream_*.jsonl`)

Recorded 2026-10-25 with **Claude Code 2.1.310 on the developer's Mac**
(not the then-current `gopod-agent` image, which pinned 2.1.289 and was not available
locally):

    claude --no-session-persistence --model haiku \
      --output-format stream-json --verbose --include-partial-messages \
      -p "<prompt>"

- `claude_stream_tools.jsonl`: a successful run with three tool calls
  (Bash `echo hi`, Read `note.txt`, Glob `*.txt`) and a one-sentence
  answer. Prompt: "First run the shell command: echo hi. Then read
  note.txt. Then glob for *.txt. Then answer in one short sentence what
  the file says."
- `claude_stream_auth_error.jsonl`: the same CLI with an empty `HOME`
  and an invalid `ANTHROPIC_API_KEY`: exit 1, empty stderr, a `result`
  with `is_error: true` and the message in `result`.

Sanitised after recording (scrubbed again in review, fix round 1):
the local scratch path is `/workspace/chat` everywhere, including the
`input_json_delta` fragments, which were rebuilt from the joined, fixed
JSON; session ids are `sess-0001`, other UUIDs `uuid-NNNN`, message,
tool-use and request ids `msg_NNNN`, `toolu_NNNN`, `req_NNNN`;
thinking/signature blobs are `sig-placeholder`; timestamps are fixed;
`tool_result_meta` is dropped and `permissionMode` is `default`;
`system/hook_*` lines (the developer's own hooks) are dropped; the
`init` line is cut to a few fields with a fixed tool list; and
`rate_limit_event` is cut to its status. `TestTestdataHasNoPersonalData`
fails on local paths or real-looking UUIDs in any testdata file.

**Cross-checked against 2.1.289** (the agent image version at that time) on
2026-10-25 by event kinds only: a run in the real image produced the
same kinds (system/init with session_id, system/status,
system/thinking_tokens, stream_event message_start/content_block_*/
message_delta/message_stop with input_json, thinking, signature and text
deltas, separate assistant lines for tool_use, thinking and text,
rate_limit_event, user tool results, result/success with `result`,
`is_error`, `session_id`). The fixtures themselves are still 2.1.310
recordings, not byte-identical to 2.1.289 output. Tool inputs of Edit,
Write, Grep, WebSearch and WebFetch in the tests are constructed from
Claude Code's tool schemas, not recorded.

## Codex

`codex_exec_*`: see `provider_session_test.go`.
