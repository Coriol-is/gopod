package runner

// TaskCommandProvider builds the command of an ephemeral scheduled task run
// (CP-004 PR 3). It is an optional capability: a provider without it (group
// wrappers, test doubles) cannot run managed tasks, which fails closed.
//
// The command never selects, resumes or continues a native session and leaves
// no transcript behind, so a task before the first foreground request cannot
// become what the legacy adoption pass (`--continue`, `resume --last`, newest
// transcript) picks up.
type TaskCommandProvider interface {
	// RunTaskCmd builds an ephemeral, non-persistent run of prompt. systemPrompt
	// is trusted host-composed run instruction text and may be empty.
	RunTaskCmd(prompt, systemPrompt string) []string
}

// RunTaskCmd runs Claude with --no-session-persistence: no --continue, no
// --resume, no --session-id, no transcript.
func (p *ClaudeProvider) RunTaskCmd(prompt, systemPrompt string) []string {
	cmd := []string{"claude", "--no-session-persistence", "--dangerously-skip-permissions"}
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}
	return append(cmd, "-p", prompt)
}

// RunTaskCmd runs Codex with --ephemeral: no `resume`, no rollout written.
// Codex has no system prompt flag, so trusted instructions are prepended.
func (p *CodexProvider) RunTaskCmd(prompt, systemPrompt string) []string {
	if systemPrompt != "" {
		prompt = systemPrompt + "\n\n---\n\n" + prompt
	}
	cmd := []string{"codex", "exec", "--ephemeral"}
	cmd = append(cmd, codexExecFlags...)
	return append(cmd, prompt)
}

var (
	_ TaskCommandProvider = (*ClaudeProvider)(nil)
	_ TaskCommandProvider = (*CodexProvider)(nil)
)
