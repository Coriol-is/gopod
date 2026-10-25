package runner

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// AgentLimits are the admission settings (D026), read by internal/config.
type AgentLimits struct {
	MaxAgents      int // agents running at once; < 1 means 1
	MinAvailableMB int // MemAvailable a start needs; 0 disables the check
	NodeHeapMB     int // V8 heap cap for Claude agents; 0 disables
}

// SetAgentLimits replaces the agent gate and the heap cap. Call it at
// startup, before the first agent runs (like SetCompactAfter).
func (r *Runner) SetAgentLimits(l AgentLimits) {
	r.gate = newGate(l.MaxAgents, int64(max(l.MinAvailableMB, 0)), readMemAvailableMB, r.log)
	r.nodeHeapMB = max(l.NodeHeapMB, 0)
}

const nodeOptionsEnv = "NODE_OPTIONS"

// agentExecEnv is agentEnv plus the soft memory cap: for Claude agents
// (Node) NODE_OPTIONS gets --max-old-space-size=<nodeHeapMB>. Codex is
// not Node and is left alone. The value is not a secret, so it is set
// here rather than forwarded from the host (D012 does not apply). A
// NODE_OPTIONS the container already has (forwarded through the env
// allowlist) is merged, since the exec's value replaces the container's.
func (r *Runner) agentExecEnv(chatFolder string, thread int, prov AgentProvider) ([]string, func()) {
	env, release := r.agentEnv(chatFolder, thread)
	if r.nodeHeapMB <= 0 || prov.Name() != "claude" {
		return env, release
	}
	var container string
	if slices.Contains(prov.RequiredEnvVars(), nodeOptionsEnv) || slices.Contains(r.allow, nodeOptionsEnv) {
		container = os.Getenv(nodeOptionsEnv)
	}
	return withNodeHeap(env, container, r.nodeHeapMB), release
}

// withNodeHeap returns env with NODE_OPTIONS carrying the heap cap. The
// base value is env's own NODE_OPTIONS if set, else the container's.
func withNodeHeap(env []string, container string, heapMB int) []string {
	if heapMB <= 0 {
		return env
	}
	out := make([]string, 0, len(env)+1)
	base := container
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, nodeOptionsEnv+"="); ok {
			base = v
			continue
		}
		out = append(out, kv)
	}
	return append(out, nodeOptionsEnv+"="+mergeNodeOptions(base, heapMB))
}

// mergeNodeOptions appends --max-old-space-size=<heapMB> to existing
// NODE_OPTIONS unless a heap size is already there (either spelling).
func mergeNodeOptions(existing string, heapMB int) string {
	existing = strings.TrimSpace(existing)
	if heapMB <= 0 || strings.Contains(existing, "--max-old-space-size") || strings.Contains(existing, "--max_old_space_size") {
		return existing
	}
	flag := "--max-old-space-size=" + strconv.Itoa(heapMB)
	if existing == "" {
		return flag
	}
	return existing + " " + flag
}
