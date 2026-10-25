package runner

import (
	"encoding/json"
	"errors"
	"strings"
)

// CodexProvider implements AgentProvider for OpenAI Codex CLI.
type CodexProvider struct {
	image string
}

func NewCodexProvider(image string) *CodexProvider {
	if image == "" {
		image = "gopod-agent-codex:latest"
	}
	return &CodexProvider{image: image}
}

func (p *CodexProvider) Name() string    { return "codex" }
func (p *CodexProvider) Image() string   { return p.image }
func (p *CodexProvider) HomeDir() string { return "/home/node" }

func (p *CodexProvider) RequiredEnvVars() []string {
	return []string{"OPENAI_API_KEY"}
}

// codexExecFlags are passed to both `codex exec` and `codex exec resume`.
//
// codex-cli 0.160 removed --full-auto. The container is the sandbox
// (read-only rootfs, dropped caps, non-root uid, allowlisted mounts —
// ISOLATION.md), so Codex's own sandbox and approval prompts are
// bypassed, the same stance as `claude --dangerously-skip-permissions`.
// --skip-git-repo-check keeps Codex from refusing a workspace that is
// not a trusted git repo (RestoreConfigCmd still inits one for the
// agent's own benefit).
var codexExecFlags = []string{
	"--dangerously-bypass-approvals-and-sandbox",
	"--skip-git-repo-check",
}

// RunCmd resumes with `exec resume --last` (zero ref) or `exec resume
// <id>`, or starts a new conversation with plain `exec` when the ref
// has an id but Resume is false: Codex picks its own thread id, which
// the caller reads from the output with ExtractSessionID.
func (p *CodexProvider) RunCmd(prompt, systemPrompt string, s SessionRef) []string {
	var cmd []string
	switch {
	case s.ID == "":
		// `codex exec resume --last` continues the most recently
		// updated session; on an empty ~/.codex (0.160) it starts a new
		// one instead of failing.
		cmd = []string{"codex", "exec", "resume", "--last"}
	case s.Resume:
		// Fails ("no rollout found for thread id") if the rollout is gone.
		cmd = []string{"codex", "exec", "resume", s.ID}
	default:
		cmd = []string{"codex", "exec"}
	}
	cmd = append(cmd, codexExecFlags...)
	if systemPrompt != "" {
		// Codex doesn't have --append-system-prompt, so prepend to prompt.
		prompt = systemPrompt + "\n\n---\n\n" + prompt
	}
	cmd = append(cmd, prompt)
	return cmd
}

func (p *CodexProvider) RunFreshCmd(prompt string) []string {
	// No resume = fresh session. --ephemeral (present in 0.160) keeps
	// the run from writing a rollout, so system tasks (compact summary,
	// memory extraction) never become what `resume --last` or
	// LatestSessionIDCmd picks.
	cmd := []string{"codex", "exec", "--ephemeral"}
	cmd = append(cmd, codexExecFlags...)
	return append(cmd, prompt)
}

func (p *CodexProvider) RestoreConfigCmd() []string {
	// Codex requires a trusted git repo in the working directory.
	// Init one if it doesn't exist. Also skip git repo check via config.
	return []string{"sh", "-c",
		`cd /workspace/chat && [ -d .git ] || git init -q && git config user.email "agent@gopod" && git config user.name "gopod"`}
}

func (p *CodexProvider) ModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"-m", model}
}

func (p *CodexProvider) VersionCmd() []string {
	return []string{"codex", "--version"}
}

func (p *CodexProvider) AuthStatusCmd() []string {
	return []string{"codex", "login", "status"}
}

func (p *CodexProvider) ParseAuthStatus(stdout string) (AuthStatus, error) {
	stdout = strings.TrimSpace(stdout)
	// Codex login status outputs plain text, not JSON:
	//   "Logged in using ChatGPT"
	//   "Logged in using API key"
	//   "Not logged in"
	if strings.Contains(strings.ToLower(stdout), "not logged in") {
		return AuthStatus{LoggedIn: false, AuthMethod: "none"}, nil
	}
	if strings.Contains(strings.ToLower(stdout), "logged in") {
		method := "chatgpt"
		if strings.Contains(strings.ToLower(stdout), "api key") {
			method = "api_key"
		}
		return AuthStatus{LoggedIn: true, AuthMethod: method}, nil
	}
	// Try JSON fallback (future versions might switch to JSON).
	var status AuthStatus
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(stdout), &fields) == nil && fields["loggedIn"] != nil {
		if err := json.Unmarshal([]byte(stdout), &status); err == nil {
			return status, nil
		}
	}
	return AuthStatus{}, errors.New("codex: unrecognized auth status")
}

func (p *CodexProvider) IsNotLoggedInError(stderr, stdout string) bool {
	combined := strings.ToLower(stderr + " " + stdout)
	return strings.Contains(combined, "not logged in") ||
		strings.Contains(combined, "authentication") ||
		strings.Contains(combined, "unauthorized")
}

func (p *CodexProvider) LoginCmd() []string {
	// --device-auth: prints a URL + one-time code. User opens URL in
	// browser, enters the code, completes auth. gopod's /login
	// handler forwards the URL + code to Telegram.
	return []string{"codex", "login", "--device-auth"}
}

func (p *CodexProvider) ExtractLoginURL(line string) string {
	// Codex device auth prints:
	//   URL: https://auth.openai.com/codex/device
	//   Code: XXXX-XXXXX
	// We capture both — the login handler forwards both to Telegram.
	cleaned := stripTerminalEscapes(line)
	if i := strings.Index(cleaned, "https://"); i >= 0 {
		url := cleaned[i:]
		if j := strings.IndexAny(url, " \t\r\n"); j >= 0 {
			url = url[:j]
		}
		return url
	}
	// Also match the device code line.
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) >= 8 && strings.Contains(cleaned, "-") && !strings.Contains(cleaned, " ") {
		// Looks like a device code (e.g. "ELJK-3JVGQ").
		return "CODE:" + cleaned
	}
	return ""
}

// ClearSessionCmd deletes one thread's rollout. Codex 0.16x writes it
// to ~/.codex/sessions/YYYY/MM/DD/rollout-<local time>-<thread id>.jsonl
// and appends to the same file on resume; with the file gone, `exec
// resume <id>` fails, so the caller must start a new conversation. The
// id is passed as $1, never spliced into the script.
func (p *CodexProvider) ClearSessionCmd(s SessionRef) []string {
	if s.ID == "" {
		return []string{"sh", "-c",
			`rm -rf "$HOME/.codex/sessions" "$HOME/.codex/conversations"`}
	}
	if !validSessionID(s.ID) {
		return invalidSessionIDCmd()
	}
	return []string{"sh", "-c",
		`[ -d "$HOME/.codex/sessions" ] || exit 0; ` +
			`find "$HOME/.codex/sessions" -type f -name "rollout-*-$1.jsonl" -exec rm -f {} +`,
		"sh", s.ID}
}

// LatestSessionIDCmd prints the thread id of the most recently
// modified rollout, which is what `exec resume --last` picks (checked
// against codex-cli 0.164: it follows the last update, not the newest
// file name). find instead of a fixed-depth glob, so a change to the
// YYYY/MM/DD layout cannot silently yield no id; the -nt loop avoids
// splitting the listing into separately sorted batches.
func (p *CodexProvider) LatestSessionIDCmd() []string {
	return []string{"sh", "-c",
		`[ -d "$HOME/.codex/sessions" ] || exit 0; ` +
			`f=$(find "$HOME/.codex/sessions" -type f -name 'rollout-*.jsonl' | ` +
			`{ b=; while IFS= read -r x; do if [ -z "$b" ] || [ "$x" -nt "$b" ]; then b=$x; fi; done; printf '%s' "$b"; }); ` +
			`[ -n "$f" ] || exit 0; ` +
			`basename "$f" .jsonl | sed -nE 's/^rollout-.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$/\1/p'`}
}

// ExtractSessionID reads the thread id from either output mode of
// `codex exec`: the first `--json` event on stdout,
//
//	{"type":"thread.started","thread_id":"<id>"}
//
// or the plain-mode header line on stderr, `session id: <id>`.
func (p *CodexProvider) ExtractSessionID(line string) string {
	line = strings.TrimSpace(stripTerminalEscapes(line))
	var id string
	if strings.HasPrefix(line, "{") {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "thread.started" {
			return ""
		}
		id = ev.ThreadID
	} else if rest, ok := strings.CutPrefix(line, "session id:"); ok {
		id = strings.TrimSpace(rest)
	}
	if !validSessionID(id) {
		return ""
	}
	return id
}

// AnnouncesSessionID is true: a plain `codex exec` picks its own thread
// id and prints it (ExtractSessionID).
func (p *CodexProvider) AnnouncesSessionID() bool { return true }

// SessionExistsCmd checks for the rollout that `exec resume <id>` needs
// (the file ClearSessionCmd deletes).
func (p *CodexProvider) SessionExistsCmd(id string) []string {
	if !validSessionID(id) {
		return invalidSessionIDCmd()
	}
	return []string{"sh", "-c",
		`[ -d "$HOME/.codex/sessions" ] || exit 1; ` +
			`f=$(find "$HOME/.codex/sessions" -type f -name "rollout-*-$1.jsonl" | head -n 1); ` +
			`[ -n "$f" ]`,
		"sh", id}
}

var _ AgentProvider = (*CodexProvider)(nil)
