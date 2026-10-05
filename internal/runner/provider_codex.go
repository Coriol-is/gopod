package runner

import (
	"encoding/json"
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

func (p *CodexProvider) RunCmd(prompt, systemPrompt string) []string {
	// `codex exec resume --last` continues the most recent session.
	// --full-auto = --sandbox workspace-write (auto-approve writes).
	cmd := []string{"codex", "exec", "resume", "--last", "--full-auto"}
	if systemPrompt != "" {
		// Codex doesn't have --append-system-prompt, so prepend to prompt.
		prompt = systemPrompt + "\n\n---\n\n" + prompt
	}
	cmd = append(cmd, prompt)
	return cmd
}

func (p *CodexProvider) RunFreshCmd(prompt string) []string {
	// No resume = fresh session. --full-auto for sandbox.
	return []string{"codex", "exec", "--full-auto", prompt}
}

func (p *CodexProvider) RestoreConfigCmd() []string {
	// Codex requires a trusted git repo in the working directory.
	// Init one if it doesn't exist. Also skip git repo check via config.
	return []string{"sh", "-c",
		`cd /workspace/chat && [ -d .git ] || git init -q && git config user.email "agent@gopod" && git config user.name "gopod"`}
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
	if strings.Contains(strings.ToLower(stdout), "logged in") {
		method := "chatgpt"
		if strings.Contains(strings.ToLower(stdout), "api key") {
			method = "api_key"
		}
		return AuthStatus{LoggedIn: true, AuthMethod: method}, nil
	}
	// Try JSON fallback (future versions might switch to JSON).
	var status AuthStatus
	if err := json.Unmarshal([]byte(stdout), &status); err == nil {
		return status, nil
	}
	return AuthStatus{LoggedIn: false, AuthMethod: "none"}, nil
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

func (p *CodexProvider) ClearSessionCmd() []string {
	return []string{"sh", "-c",
		`rm -rf "$HOME/.codex/sessions" "$HOME/.codex/conversations"`}
}

var _ AgentProvider = (*CodexProvider)(nil)
