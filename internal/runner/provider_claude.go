package runner

import (
	"encoding/json"
	"strings"
)

// ClaudeProvider implements AgentProvider for Claude Code CLI.
type ClaudeProvider struct {
	image string
}

// NewClaudeProvider creates a provider for Claude Code CLI.
// image defaults to "gopod-agent:latest" if empty.
func NewClaudeProvider(image string) *ClaudeProvider {
	if image == "" {
		image = "gopod-agent:latest"
	}
	return &ClaudeProvider{image: image}
}

func (p *ClaudeProvider) Name() string  { return "claude" }
func (p *ClaudeProvider) Image() string { return p.image }
func (p *ClaudeProvider) HomeDir() string { return "/home/node" }

func (p *ClaudeProvider) RequiredEnvVars() []string {
	return []string{"ANTHROPIC_API_KEY"}
}

const claudeAllowedTools = "Bash,Read,Write,Edit,Grep,Glob,WebSearch,WebFetch"

func (p *ClaudeProvider) RunCmd(prompt, systemPrompt string) []string {
	cmd := []string{"claude", "--continue", "--allowedTools", claudeAllowedTools}
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}
	cmd = append(cmd, "-p", prompt)
	return cmd
}

func (p *ClaudeProvider) RunFreshCmd(prompt string) []string {
	// --no-session-persistence prevents RunFresh from creating session
	// files that would confuse --continue in the user's conversation.
	return []string{"claude", "--no-session-persistence", "--allowedTools", claudeAllowedTools, "-p", prompt}
}

func (p *ClaudeProvider) RestoreConfigCmd() []string {
	return []string{"sh", "-c",
		`if [ ! -f "$HOME/.claude.json" ]; then ` +
			`b=$(ls -t "$HOME/.claude/backups/.claude.json.backup."* 2>/dev/null | head -1); ` +
			`[ -n "$b" ] && cp "$b" "$HOME/.claude.json"; fi`}
}

func (p *ClaudeProvider) AuthStatusCmd() []string {
	return []string{"claude", "auth", "status", "--json"}
}

func (p *ClaudeProvider) ParseAuthStatus(stdout string) (AuthStatus, error) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return AuthStatus{}, nil
	}
	var status AuthStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		return AuthStatus{}, err
	}
	return status, nil
}

func (p *ClaudeProvider) IsNotLoggedInError(stderr, stdout string) bool {
	const needle = "not logged in"
	return strings.Contains(strings.ToLower(stderr), needle) ||
		strings.Contains(strings.ToLower(stdout), needle)
}

func (p *ClaudeProvider) LoginCmd() []string {
	return []string{"claude", "auth", "login"}
}

func (p *ClaudeProvider) ExtractLoginURL(line string) string {
	const prefix = "visit: "
	idx := strings.Index(strings.ToLower(line), prefix)
	if idx >= 0 {
		return strings.TrimSpace(line[idx+len(prefix):])
	}
	if i := strings.Index(line, "https://"); i >= 0 {
		return strings.TrimRight(line[i:], " \t\r\n")
	}
	return ""
}

func (p *ClaudeProvider) ClearSessionCmd() []string {
	return []string{"sh", "-c",
		`rm -rf "$HOME/.claude/projects" "$HOME/.claude/.active_session"`}
}

// Compile-time check.
var _ AgentProvider = (*ClaudeProvider)(nil)
