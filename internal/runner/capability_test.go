package runner

import (
	"testing"

	"github.com/Coriol-is/gopod/internal/memory"
)

type fakeMinter struct {
	minted  []string
	revoked []string
}

func (f *fakeMinter) Mint(chat string) string { f.minted = append(f.minted, chat); return "tok-" + chat }
func (f *fakeMinter) Revoke(tok string)        { f.revoked = append(f.revoked, tok) }

func TestAgentEnvMintsPerExecAndReleases(t *testing.T) {
	fm := &fakeMinter{}
	r := &Runner{}
	r.SetCapabilities(fm)

	env, release := r.agentEnv("alice")
	want := memory.CapabilityEnv + "=tok-alice"
	if len(env) != 1 || env[0] != want {
		t.Fatalf("env = %v, want [%s]", env, want)
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
	env, release := r.agentEnv("alice")
	if env != nil {
		t.Errorf("env = %v, want nil", env)
	}
	release()
}
