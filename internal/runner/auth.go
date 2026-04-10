package runner

import (
	"context"
	"encoding/json"
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
