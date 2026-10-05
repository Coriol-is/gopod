package runner

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// logAgentVersion runs the provider's VersionCmd inside the container
// and logs the result once per container id. The harness version is
// baked into the agent image at build time (container/Dockerfile*),
// so this is the only place the running version becomes visible
// without exec-ing into the container by hand. Best effort: failures
// are logged at debug and never affect the turn.
func (r *Runner) logAgentVersion(ctx context.Context, containerID, chatFolder string, prov AgentProvider) {
	r.versionLoggedMu.Lock()
	if r.versionLogged == nil {
		r.versionLogged = make(map[string]bool)
	}
	seen := r.versionLogged[containerID]
	r.versionLogged[containerID] = true
	r.versionLoggedMu.Unlock()
	if seen {
		return
	}

	cmd := prov.VersionCmd()
	if len(cmd) == 0 {
		return
	}
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := r.d.Exec(vctx, containerID, cmd, nil)
	if err != nil || res.ExitCode != 0 {
		r.log.Debug("agent version check failed",
			slog.String("chat", chatFolder),
			slog.String("provider", prov.Name()),
			slog.Any("err", err))
		return
	}
	r.log.Info("agent container ready",
		slog.String("chat", chatFolder),
		slog.String("provider", prov.Name()),
		slog.String("image", prov.Image()),
		slog.String("cli_version", firstLine(res.Stdout)))
}

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
