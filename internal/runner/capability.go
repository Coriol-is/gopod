package runner

import (
	"sync"

	"github.com/Coriol-is/gopod/internal/memory"
)

// CapabilityMinter issues per-exec memory capabilities. Declared here,
// at the consumer, and satisfied by *memory.Tokens.
type CapabilityMinter interface {
	Mint(chat string, thread int) string
	Revoke(tok string)
}

// SetCapabilities wires the token store the memory API checks against.
// Nil (dev, no memory) means agent execs get no capability and the API,
// if running, answers 401.
func (r *Runner) SetCapabilities(m CapabilityMinter) { r.caps = m }

// agentEnv returns the extra environment for one agent exec: a fresh
// memory capability bound to the conversation (chatFolder, thread; 0 for
// system runs), plus a release func that
// revokes it. Call release when the exec has fully finished (after the
// streams close), not when the handler returns; a tool call that is
// still running inside the agent must keep its capability. release is
// idempotent.
func (r *Runner) agentEnv(chatFolder string, thread int) ([]string, func()) {
	if r.caps == nil {
		return nil, func() {}
	}
	tok := r.caps.Mint(chatFolder, thread)
	var once sync.Once
	return []string{memory.CapabilityEnv + "=" + tok}, func() {
		once.Do(func() { r.caps.Revoke(tok) })
	}
}
