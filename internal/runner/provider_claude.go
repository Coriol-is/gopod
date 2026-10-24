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

// RunCmd selects the session with --continue (zero ref), --session-id
// (new session under the caller's UUID) or --resume (existing session).
// Claude rejects --session-id for an id that already has a transcript
// ("Session ID ... is already in use"), so callers switch to Resume
// after the first turn.
func (p *ClaudeProvider) RunCmd(prompt, systemPrompt string, s SessionRef) []string {
	cmd := []string{"claude"}
	switch {
	case s.ID == "":
		cmd = append(cmd, "--continue")
	case s.Resume:
		cmd = append(cmd, "--resume", s.ID)
	default:
		cmd = append(cmd, "--session-id", s.ID)
	}
	cmd = append(cmd, "--dangerously-skip-permissions")
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}
	cmd = append(cmd, "-p", prompt)
	return cmd
}

func (p *ClaudeProvider) RunFreshCmd(prompt string) []string {
	// --no-session-persistence prevents RunFresh from creating session
	// files that would confuse --continue in the user's conversation.
	return []string{"claude", "--no-session-persistence", "--dangerously-skip-permissions", "-p", prompt}
}

func (p *ClaudeProvider) RestoreConfigCmd() []string {
	return []string{"sh", "-c",
		`if [ ! -f "$HOME/.claude.json" ]; then ` +
			`b=$(ls -t "$HOME/.claude/backups/.claude.json.backup."* 2>/dev/null | head -1); ` +
			`[ -n "$b" ] && cp "$b" "$HOME/.claude.json"; fi`}
}

func (p *ClaudeProvider) ModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", model}
}

func (p *ClaudeProvider) VersionCmd() []string {
	return []string{"claude", "--version"}
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
	// The CLI may wrap the URL in an OSC-8 hyperlink whose parameters
	// duplicate the visible URL — strip escapes before matching, and
	// cut at the first whitespace, not the end of line.
	cleaned := stripTerminalEscapes(line)
	if i := strings.Index(cleaned, "https://"); i >= 0 {
		url := cleaned[i:]
		if j := strings.IndexAny(url, " \t\r\n"); j >= 0 {
			url = url[:j]
		}
		return url
	}
	return ""
}

// ClearSessionCmd deletes one session's transcript. Claude Code keeps
// it at ~/.claude/projects/<cwd with / . _ replaced by ->/<id>.jsonl,
// plus a <id>/ directory for subagent transcripts and tool results;
// the project-dir glob avoids re-deriving the cwd encoding. The id is
// passed as $1, never spliced into the script.
func (p *ClaudeProvider) ClearSessionCmd(s SessionRef) []string {
	if s.ID == "" {
		return []string{"sh", "-c",
			`rm -rf "$HOME/.claude/projects" "$HOME/.claude/.active_session"`}
	}
	if !validSessionID(s.ID) {
		return invalidSessionIDCmd()
	}
	return []string{"sh", "-c",
		`rm -rf "$HOME"/.claude/projects/*/"$1".jsonl "$HOME"/.claude/projects/*/"$1"`,
		"sh", s.ID}
}

// LatestSessionIDCmd prints the id of the most recently modified
// transcript, which is the session `claude --continue` resumes (the
// container has a single cwd, so a single project directory).
func (p *ClaudeProvider) LatestSessionIDCmd() []string {
	return []string{"sh", "-c",
		`f=$(ls -t "$HOME"/.claude/projects/*/*.jsonl 2>/dev/null | head -n 1); ` +
			`if [ -n "$f" ]; then basename "$f" .jsonl; fi`}
}

// ExtractSessionID returns "": the caller chooses Claude's session id.
func (p *ClaudeProvider) ExtractSessionID(string) string { return "" }

// AnnouncesSessionID is false: `--session-id` takes the caller's UUID.
func (p *ClaudeProvider) AnnouncesSessionID() bool { return false }

// SessionExistsCmd checks for the transcript ClearSessionCmd deletes.
// A transcript that was never written (the first turn failed before
// Claude saved anything) counts as missing, which is what makes a
// caller start over instead of resuming an id Claude does not know.
func (p *ClaudeProvider) SessionExistsCmd(id string) []string {
	if !validSessionID(id) {
		return invalidSessionIDCmd()
	}
	return []string{"sh", "-c",
		`for f in "$HOME"/.claude/projects/*/"$1".jsonl; do [ -f "$f" ] && exit 0; done; exit 1`,
		"sh", id}
}

// Compile-time check.
var _ AgentProvider = (*ClaudeProvider)(nil)
