package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

// Stopping a streamed agent turn.
//
// Cancelling the ctx of a docker exec does not stop the process inside
// the container: the Docker client uses ctx only to open the
// connection, and the Engine API has no "kill exec" call. So the runner
// wraps every streamed agent command in a tiny shell that records its
// pid, and on cancel runs a second exec that signals it.
//
// A docker exec process is a session and process-group leader (runc
// calls setsid), and `exec "$@"` keeps the shell's pid for the agent,
// so the recorded pid is also the agent's process-group id. Signalling
// the group (-pid) reaches the tools the agent spawned (bash, node,
// codex sandboxes), not just the agent.

// agentPidDir is where pid files live inside the container. /tmp is a
// writable tmpfs (mode 1777) under the read-only rootfs; see
// BuildContainerArgs.
const agentPidDir = "/tmp"

// agentKillGrace is how long the agent has to exit after SIGTERM
// before its process group gets SIGKILL.
const agentKillGrace = 3 * time.Second

// agentKillTimeout bounds the kill exec as a whole: waiting for the pid
// file, the TERM grace, and the Docker round trip.
const agentKillTimeout = 15 * time.Second

// agentCleanupTimeout bounds the pid-file removal after a normal run.
const agentCleanupTimeout = 5 * time.Second

// agentExecer is the slice of *Docker the streaming agent path uses.
// Declared here, at the consumer, so tests can substitute a fake.
type agentExecer interface {
	Exec(ctx context.Context, containerID string, cmd []string, env []string) (*ExecResult, error)
	ExecStream(ctx context.Context, containerID string, cmd []string, env []string) (*StreamHandle, error)
}

// newAgentPidFile returns a pid-file path unique to one agent run.
func newAgentPidFile() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("runner: pid file name: %w", err)
	}
	return agentPidDir + "/gopod-agent-" + hex.EncodeToString(b[:]) + ".pid", nil
}

// wrapWithPidFile wraps cmd so the process writes its pid to pidFile
// and then becomes cmd. Arguments pass through "$@" unquoted by us, so
// a prompt with quotes or newlines reaches the agent byte for byte;
// stdout, stderr and the exit code are the agent's own.
func wrapWithPidFile(pidFile string, cmd []string) []string {
	out := make([]string, 0, len(cmd)+4)
	out = append(out, "sh", "-c", `echo $$ > "$0"; exec "$@"`, pidFile)
	return append(out, cmd...)
}

// killAgentCmd returns the exec that stops the agent recorded in
// pidFile: SIGTERM to its process group, up to grace for the agent to
// exit, then SIGKILL to the group, then the pid file is removed.
//
// The pid file can be missing for a moment when the cancel lands right
// after the exec started, so the script waits for it (up to 2s) before
// giving up. It exits 0 whether or not there was anything to kill.
func killAgentCmd(pidFile string, grace time.Duration) []string {
	ticks := int(grace / (100 * time.Millisecond))
	if ticks < 1 {
		ticks = 1
	}
	script := fmt.Sprintf(`f="$0"
i=0
while [ ! -s "$f" ] && [ $i -lt 20 ]; do sleep 0.1; i=$((i+1)); done
p=$(cat "$f" 2>/dev/null)
if [ -n "$p" ]; then
  kill -TERM "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null
  i=0
  while [ $i -lt %d ] && kill -0 "$p" 2>/dev/null; do sleep 0.1; i=$((i+1)); done
  kill -KILL "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null
fi
rm -f "$f"
exit 0`, ticks)
	return []string{"sh", "-c", script, pidFile}
}

// removePidFileCmd returns the exec that deletes a finished run's pid file.
func removePidFileCmd(pidFile string) []string {
	return []string{"rm", "-f", pidFile}
}

// killAgent stops the agent whose pid is in pidFile. It runs on a
// context detached from the (already cancelled) turn context.
func (r *Runner) killAgent(ctx context.Context, containerID, chatFolder, pidFile string) {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentKillTimeout)
	defer cancel()
	res, err := r.ex.Exec(kctx, containerID, killAgentCmd(pidFile, agentKillGrace), nil)
	if err != nil {
		r.log.Error("stopping cancelled agent failed",
			slog.String("chat", chatFolder), slog.Any("err", err))
		return
	}
	if res.ExitCode != 0 {
		r.log.Error("stopping cancelled agent failed",
			slog.String("chat", chatFolder),
			slog.Int("exit", res.ExitCode),
			slog.String("stderr", tailString(res.Stderr, 300)))
		return
	}
	r.log.Info("cancelled agent stopped", slog.String("chat", chatFolder))
}

// removePidFile deletes a finished run's pid file so they don't pile up
// in the container's /tmp.
func (r *Runner) removePidFile(ctx context.Context, containerID, pidFile string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentCleanupTimeout)
	defer cancel()
	if _, err := r.ex.Exec(cctx, containerID, removePidFileCmd(pidFile), nil); err != nil {
		r.log.Warn("removing agent pid file failed",
			slog.String("pid_file", pidFile), slog.Any("err", err))
	}
}

// acquireAgentSlot blocks until no other streamed agent process is
// alive for chatFolder, then claims the slot. The returned release
// frees it. A stopped turn holds its slot until its process is killed,
// so the next turn never runs a second agent on the same session while
// the old one is still shutting down.
func (r *Runner) acquireAgentSlot(ctx context.Context, chatFolder string) (func(), error) {
	for {
		r.agentSlotsMu.Lock()
		if r.agentSlots == nil {
			r.agentSlots = make(map[string]chan struct{})
		}
		busy, ok := r.agentSlots[chatFolder]
		if !ok {
			done := make(chan struct{})
			r.agentSlots[chatFolder] = done
			r.agentSlotsMu.Unlock()
			return func() {
				r.agentSlotsMu.Lock()
				delete(r.agentSlots, chatFolder)
				r.agentSlotsMu.Unlock()
				close(done)
			}, nil
		}
		r.agentSlotsMu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, fmt.Errorf("runner: waiting for previous agent in %q: %w", chatFolder, ctx.Err())
		}
	}
}
