package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AuthStatus is picoclaw's parsed view of `claude auth status --json`
// inside an agent container.
//
// Mirror of the JSON keys Claude Code emits as of CLI 2.1.x:
//
//	{
//	  "loggedIn":   false,
//	  "authMethod": "none",
//	  "apiProvider": "firstParty"
//	}
//
// Stable enough to depend on for picoclaw's auth gating; if a future
// Claude Code release renames the keys, this struct gets a small
// patch and the M6 code path keeps working.
type AuthStatus struct {
	LoggedIn    bool   `json:"loggedIn"`
	AuthMethod  string `json:"authMethod"`
	APIProvider string `json:"apiProvider"`
}

// CheckAuth runs `claude auth status --json` inside the given
// container and returns the parsed AuthStatus.
//
// `claude auth status` exits non-zero when the user is not logged in,
// so a non-zero exit code is NOT treated as an error here — the JSON
// body still tells us what we need. Only an actual JSON parse failure
// or a Docker exec failure escapes as an error.
func (d *Docker) CheckAuth(ctx context.Context, containerID string) (AuthStatus, error) {
	res, err := d.Exec(ctx, containerID, []string{"claude", "auth", "status", "--json"}, nil)
	if err != nil {
		return AuthStatus{}, fmt.Errorf("docker: claude auth status: %w", err)
	}

	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return AuthStatus{}, fmt.Errorf("docker: claude auth status: empty stdout (stderr=%q exit=%d)",
			res.Stderr, res.ExitCode)
	}

	var status AuthStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return AuthStatus{}, fmt.Errorf("docker: claude auth status: parse %q: %w", out, err)
	}
	return status, nil
}

// ErrNotLoggedIn is returned by RunPrompt when the agent container has
// no Claude credentials and has never been authenticated.
var ErrNotLoggedIn = errors.New("runner: agent container is not logged in")

// ErrSessionExpired is returned by RunPrompt when the agent container
// WAS authenticated but the session/token has expired. Callers show a
// different message ("session expired, /login to re-authenticate")
// instead of the cold-start "not authenticated" prompt.
var ErrSessionExpired = errors.New("runner: authentication session expired")

// RunPrompt runs `claude -p <prompt>` inside the container and returns
// the assistant's reply text from stdout. Synchronous: blocks until
// the agent finishes.
//
// Each call is a fresh Claude Code session. Multi-turn conversation
// memory across Telegram messages is intentionally NOT plumbed in M6 —
// session continuity comes later via the sessions table and a
// `--resume <id>` flag, which needs the JSON output format to recover
// session ids from. M6 ships the simplest possible call shape that
// proves the round trip works.
//
// If the container is not authenticated, claude exits non-zero with
// "Not logged in" on stderr; this is mapped to ErrNotLoggedIn so
// callers can distinguish it from real failures and prompt the user
// to run /login.
func (d *Docker) RunPrompt(ctx context.Context, containerID, prompt string) (string, error) {
	if prompt == "" {
		return "", errors.New("docker: RunPrompt: empty prompt")
	}

	res, err := d.Exec(ctx, containerID, []string{"claude", "-p", prompt}, nil)
	if err != nil {
		return "", fmt.Errorf("docker: RunPrompt: %w", err)
	}

	if res.ExitCode != 0 {
		stderr := strings.TrimSpace(res.Stderr)
		if isNotLoggedInError(stderr, res.Stdout) {
			// Distinguish "never authenticated" from "session expired"
			// by checking auth status. If authMethod != "none" it means
			// credentials existed at some point but are now invalid.
			return "", d.classifyAuthError(ctx, containerID)
		}
		return "", fmt.Errorf("docker: RunPrompt: claude exited %d (stderr=%q)",
			res.ExitCode, stderr)
	}

	return strings.TrimSpace(res.Stdout), nil
}

// classifyAuthError runs `claude auth status --json` and returns
// ErrSessionExpired if the container was previously authenticated
// (authMethod != "none"), or ErrNotLoggedIn if never authenticated.
func (d *Docker) classifyAuthError(ctx context.Context, containerID string) error {
	status, err := d.CheckAuth(ctx, containerID)
	if err != nil {
		// Can't determine — fall back to generic.
		return ErrNotLoggedIn
	}
	// authMethod "none" means never logged in. Anything else (e.g.
	// "oauth", "api_key", etc) means credentials existed but are now
	// invalid — i.e. session expired.
	if status.AuthMethod != "" && status.AuthMethod != "none" {
		return ErrSessionExpired
	}
	return ErrNotLoggedIn
}

// isNotLoggedInError detects Claude Code's "Not logged in" failure mode
// from stderr or stdout. Verified against Claude Code 2.1.x against
// the picoclaw-agent:latest image: a non-authenticated invocation of
// `claude -p hello` exits non-zero and prints "Not logged in · Please
// run /login" to stderr.
//
// Both stdout and stderr are checked because the exact stream Claude
// Code chooses for the message has historically varied between minor
// CLI versions. The match is case-insensitive substring on a stable
// fragment ("not logged in") to survive minor wording tweaks.
func isNotLoggedInError(stderr, stdout string) bool {
	const needle = "not logged in"
	return strings.Contains(strings.ToLower(stderr), needle) ||
		strings.Contains(strings.ToLower(stdout), needle)
}
