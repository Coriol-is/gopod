package runner

import "regexp"

// SessionRef names the agent conversation a command acts on.
//
// The zero value is the legacy implicit mode: resume whatever session
// the CLI considers most recent (`claude --continue`, `codex exec
// resume --last`) and, for ClearSessionCmd, wipe every session. It
// exists so callers that have no stored id yet keep today's behaviour.
//
// With an ID, Resume selects between continuing that session and
// starting a new one. Claude accepts a caller-chosen UUID for a new
// session (`--session-id`); Codex assigns its own thread id, so a new
// Codex conversation is a plain `codex exec` and the caller reads the
// real id back with ExtractSessionID. Resume without an ID is treated
// as the zero value.
type SessionRef struct {
	ID     string
	Resume bool
}

// sessionIDPattern is what an ID must look like before it is put on a
// shell command line or into a glob. Claude ids are UUIDs and Codex
// thread ids are UUIDs (v7) too; the pattern is deliberately narrower
// than "anything the CLI accepts" so a path separator, glob character
// or shell metacharacter can never reach `rm`.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

// validSessionID reports whether id is safe to use in a file path.
func validSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// invalidSessionIDCmd is returned instead of a delete command when the
// id fails validSessionID: it removes nothing and exits non-zero, so
// the caller's Exec reports the failure.
func invalidSessionIDCmd() []string {
	return []string{"sh", "-c", `echo "gopod: refusing invalid session id" >&2; exit 2`}
}

// AgentProvider abstracts the CLI agent runtime (Claude Code, Codex,
// etc.) so gopod can manage different agent backends through the
// same Runner interface.
//
// Each provider knows how to build the right CLI commands for its
// agent, parse auth status, detect login errors, and manage session
// state. The Runner calls provider methods instead of hardcoding
// "claude" everywhere.
//
// Providers are stateless — all state (containers, sessions, memory)
// lives in the Runner and Docker layers.
type AgentProvider interface {
	// Name returns the provider identifier ("claude", "codex").
	Name() string

	// Image returns the Docker image tag for this provider's agent
	// container (e.g. "gopod-agent-claude:latest").
	Image() string

	// HomeDir returns the HOME path inside the container.
	HomeDir() string

	// RequiredEnvVars returns env var names to forward into the
	// container (e.g. ["ANTHROPIC_API_KEY"] for claude).
	RequiredEnvVars() []string

	// RunCmd builds the command for a conversational prompt in the
	// session s (see SessionRef; the zero value resumes the most
	// recent session). systemPrompt may be empty.
	RunCmd(prompt, systemPrompt string, s SessionRef) []string

	// RunFreshCmd builds the command for a one-shot prompt (no
	// session continuity). Used for system tasks: compact summarize,
	// memory extraction, task scheduling.
	RunFreshCmd(prompt string) []string

	// RestoreConfigCmd returns the shell command to restore the
	// agent's config file from backup (e.g. .claude.json from
	// .claude/backups/). May return nil if not needed.
	RestoreConfigCmd() []string

	// AuthStatusCmd returns the command to check auth status.
	// Stdout should be JSON parseable by ParseAuthStatus.
	AuthStatusCmd() []string

	// VersionCmd returns the command that prints the CLI version
	// (e.g. ["claude", "--version"]). Run once per container spawn so
	// the harness version baked into the image shows up in gopod logs.
	VersionCmd() []string

	// ParseAuthStatus parses the stdout of AuthStatusCmd.
	ParseAuthStatus(stdout string) (AuthStatus, error)

	// IsNotLoggedInError checks stderr/stdout for "not logged in".
	IsNotLoggedInError(stderr, stdout string) bool

	// LoginCmd returns the command for interactive login (spawned
	// via ExecInteractive with TTY+stdin for the OAuth flow).
	LoginCmd() []string

	// ExtractLoginURL extracts the OAuth URL from a login stdout line.
	ExtractLoginURL(line string) string

	// ClearSessionCmd returns the shell command that deletes the
	// session s's files (for /clear and compact). The zero value wipes
	// every session in the container, as before explicit ids existed.
	// An id that fails validSessionID yields a command that deletes
	// nothing and exits non-zero.
	ClearSessionCmd(s SessionRef) []string

	// LatestSessionIDCmd returns the command that prints the id of the
	// session the zero SessionRef would resume (the most recently
	// modified one), or nothing if there is none. Used once to adopt
	// the legacy session when a conversation gets an explicit id.
	LatestSessionIDCmd() []string

	// ExtractSessionID returns the session id announced by one line of
	// RunCmd output, or "". Only Codex announces one (Claude's id is
	// chosen by the caller).
	ExtractSessionID(line string) string

	// ModelArgs returns the CLI flag that selects model, or nil for ""
	// (provider default). Inserted before the prompt by withModel.
	ModelArgs(model string) []string
}
