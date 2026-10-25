package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Coriol-is/gopod/internal/memory"
)

type fakeMinter struct {
	mu      sync.Mutex
	minted  []string // "chat/thread"
	revoked []string
}

func (f *fakeMinter) Mint(chat string, thread int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted = append(f.minted, fmt.Sprintf("%s/%d", chat, thread))
	return "tok-" + chat
}

func (f *fakeMinter) Revoke(tok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, tok)
}

func (f *fakeMinter) mintedSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.minted...)
}

func TestAgentEnvMintsPerExecAndReleases(t *testing.T) {
	fm := &fakeMinter{}
	r := &Runner{}
	r.SetCapabilities(fm)

	env, release := r.agentEnv("alice", 7)
	want := memory.CapabilityEnv + "=tok-alice"
	if len(env) != 1 || env[0] != want {
		t.Fatalf("env = %v, want [%s]", env, want)
	}
	if got := fm.mintedSnapshot(); len(got) != 1 || got[0] != "alice/7" {
		t.Errorf("minted = %v, want [alice/7]", got)
	}
	if len(fm.revoked) != 0 {
		t.Fatal("revoked before release")
	}
	release()
	release() // idempotent
	if len(fm.revoked) != 1 || fm.revoked[0] != "tok-alice" {
		t.Errorf("revoked = %v, want [tok-alice]", fm.revoked)
	}
}

func TestAgentEnvWithoutMinterIsNoop(t *testing.T) {
	r := &Runner{}
	env, release := r.agentEnv("alice", 0)
	if env != nil {
		t.Errorf("env = %v, want nil", env)
	}
	release()
}

// The capability of an agent run is bound to the run's conversation, so
// the memory API applies that thread's visibility (D023). System runs
// (RunFresh) get thread 0.
func TestCapabilityCarriesConversation(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			fm := &fakeMinter{}
			h.r.SetCapabilities(fm)
			h.run(t, thread7)
			if res := h.runStream(t, thread9); res.Err != nil {
				t.Fatalf("RunStream: %v", res.Err)
			}
			// System runs: the fake agent home only answers session
			// commands, so RunFresh goes through the stop harness's exec.
			sys, _, _ := newStopHarness(t, prov)
			sys.SetCapabilities(fm)
			if _, err := sys.RunFresh(context.Background(), convChat, TierRegistered, nil, "sys"); err != nil {
				t.Fatalf("RunFresh: %v", err)
			}
			want := fmt.Sprint([]string{convChat + "/7", convChat + "/9", convChat + "/0"})
			if got := fmt.Sprint(fm.mintedSnapshot()); got != want {
				t.Errorf("minted = %s, want %s", got, want)
			}
		})
	}
}
