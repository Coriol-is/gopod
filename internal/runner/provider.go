package runner

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

	// RunCmd builds the command for a conversational prompt (with
	// session continuity). systemPrompt may be empty.
	RunCmd(prompt, systemPrompt string) []string

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

	// ClearSessionCmd returns the shell command to wipe session
	// files (for /clear and compact).
	ClearSessionCmd() []string
}
