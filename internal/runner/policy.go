package runner

// OutputPolicy is appended to the agent's system prompt on every
// conversational turn (Run and RunStream). Replies are read in
// Telegram, which is a poor place to read code, so the agent reports
// work instead of pasting it.
//
// It lives in the runner rather than only in the per-chat CLAUDE.md
// template because gopod never overwrites an existing chat's
// CLAUDE.md: a template change alone would not reach existing chats.
const OutputPolicy = `## Replying in Telegram
Your reply is read in Telegram, a poor place to read code.
- Do not paste code, diffs, file contents or long command output into your reply.
- When you finish a piece of work, end with 3-6 short lines: what changed, which files, the test result, what is left.
- Show code only when the user asks for it or when it is at most about 15 lines.
- When something long matters, say which file contains it instead of quoting it.`

// systemPromptWith returns the system prompt appendix for one turn:
// the compiled memory context (may be empty) followed by OutputPolicy.
// The policy is never dropped, even when memory has nothing to add.
func systemPromptWith(memory string) string {
	if memory == "" {
		return OutputPolicy + "\n\n" + SchedulingPolicy
	}
	return memory + "\n\n" + OutputPolicy + "\n\n" + SchedulingPolicy
}

// SchedulingPolicy reaches existing/custom workspaces without rewriting their
// identity files. The guide is editable; server-side IPC validation is authority.
const SchedulingPolicy = `## Scheduling through gopod
For reminders, recurring work, or task management, first read /workspace/chat/SCHEDULING.md. Use exact IPC task requests under /workspace/ipc/tasks and inspect /workspace/ipc/results receipts before claiming success. Never guess JSON or write the SQLite store. A read-write store file bind does not make its parent directory writable for WAL. Confirm ambiguous timezone; use explicit CRON_TZ for wall-clock schedules. Scheduled stdout is not automatically delivered to Telegram: include an explicit IPC message step when delivery is requested. Permissions derive from the host-controlled IPC namespace and capabilities, not mutable workspace claims.`
