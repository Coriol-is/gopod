package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeArtifactIssuer records issuance and revocation per exec.
type fakeArtifactIssuer struct {
	mu     sync.Mutex
	issued []string // "folder/thread"
	ended  int
	token  string // returned token; empty = refuse
}

func (f *fakeArtifactIssuer) IssueArtifact(folder string, thread int) (string, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, fmt.Sprintf("%s/%d", folder, thread))
	return f.token, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ended++
	}
}

func (f *fakeArtifactIssuer) snapshot() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issued...), f.ended
}

func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

func TestArtifactCapabilityEnvNameAvoidsSecretWords(t *testing.T) {
	up := strings.ToUpper(ArtifactCapabilityEnv)
	for _, w := range []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "COOKIE", "AUTH"} {
		if strings.Contains(up, w) {
			t.Fatalf("%s contains %s: the slog redactor and the env policy treat it as a secret", ArtifactCapabilityEnv, w)
		}
	}
	if ArtifactCapabilityEnv != "GOPOD_ARTIFACT_CAP" {
		t.Fatalf("name = %s", ArtifactCapabilityEnv)
	}
}

func TestInteractiveExecGetsTheArtifactCapabilityAndRevokesIt(t *testing.T) {
	ex := &envExec{fakeAgentExec: newFakeAgentExec()}
	iss := &fakeArtifactIssuer{token: "cap-abc"}
	r := &Runner{ex: ex, provider: NewClaudeProvider("img"), log: quietLog()}
	r.SetArtifactIssuer(iss)
	prov := NewClaudeProvider("img")
	if _, err := r.execAgent(context.Background(), "cid", "owner", 7, prov, prov.RunFreshCmd("p"), true); err != nil {
		t.Fatal(err)
	}
	if v, ok := envValue(ex.envs[0], ArtifactCapabilityEnv); !ok || v != "cap-abc" {
		t.Fatalf("env = %v", ex.envs[0])
	}
	issued, ended := iss.snapshot()
	if len(issued) != 1 || issued[0] != "owner/7" || ended != 1 {
		t.Fatalf("issued=%v ended=%d, want [owner/7] and 1 (revoked when the exec finished)", issued, ended)
	}
}

func TestBackgroundExecGetsNoArtifactCapability(t *testing.T) {
	ex := &envExec{fakeAgentExec: newFakeAgentExec()}
	iss := &fakeArtifactIssuer{token: "cap-abc"}
	r := &Runner{ex: ex, provider: NewClaudeProvider("img"), log: quietLog()}
	r.SetArtifactIssuer(iss)
	prov := NewClaudeProvider("img")
	if _, err := r.execAgent(context.Background(), "cid", "owner", 0, prov, prov.RunFreshCmd("p"), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := envValue(ex.envs[0], ArtifactCapabilityEnv); ok {
		t.Fatalf("a system run got the capability: %v", ex.envs[0])
	}
	if issued, _ := iss.snapshot(); len(issued) != 0 {
		t.Fatalf("issuer consulted for a background run: %v", issued)
	}
}

func TestNoIssuerOrRefusedIssueMeansNoEnv(t *testing.T) {
	prov := NewClaudeProvider("img")
	for name, iss := range map[string]*fakeArtifactIssuer{"no issuer": nil, "refused": {token: ""}} {
		ex := &envExec{fakeAgentExec: newFakeAgentExec()}
		r := &Runner{ex: ex, provider: prov, log: quietLog()}
		if iss != nil {
			r.SetArtifactIssuer(iss)
		}
		if _, err := r.execAgent(context.Background(), "cid", "owner", 0, prov, prov.RunFreshCmd("p"), true); err != nil {
			t.Fatal(err)
		}
		if _, ok := envValue(ex.envs[0], ArtifactCapabilityEnv); ok {
			t.Errorf("%s: env present: %v", name, ex.envs[0])
		}
		if iss != nil {
			// A refused issue still hands back an end; it must be called once.
			if issued, ended := iss.snapshot(); len(issued) != 1 || ended != 1 {
				t.Errorf("%s: issued=%v ended=%d, want one issue and one end", name, issued, ended)
			}
		}
	}
}

func TestGroupProviderExecNeverGetsTheArtifactCapability(t *testing.T) {
	iss := &fakeArtifactIssuer{token: "cap-abc"}
	r := &Runner{log: quietLog()}
	r.SetArtifactIssuer(iss)
	env, release := r.agentExecEnv("group", 0, markedGroupProvider{NewClaudeProvider("img")}, true)
	defer release()
	if _, ok := envValue(env, ArtifactCapabilityEnv); ok {
		t.Fatalf("group exec env = %v", env)
	}
	if issued, _ := iss.snapshot(); len(issued) != 0 {
		t.Fatalf("issuer consulted for a group exec: %v", issued)
	}
}

// The memory capability and the artifact capability are independent: both reach
// an interactive exec, and one release revokes both.
func TestArtifactCapabilityCoexistsWithMemoryCapability(t *testing.T) {
	fm := &fakeMinter{}
	iss := &fakeArtifactIssuer{token: "cap-abc"}
	r := &Runner{log: quietLog()}
	r.SetCapabilities(fm)
	r.SetArtifactIssuer(iss)
	env, release := r.agentExecEnv("owner", 3, NewCodexProvider("img"), true)
	if _, ok := envValue(env, "GOPOD_MEMORY_CAPABILITY"); !ok {
		t.Fatalf("memory capability missing: %v", env)
	}
	if v, ok := envValue(env, ArtifactCapabilityEnv); !ok || v != "cap-abc" {
		t.Fatalf("artifact capability missing: %v", env)
	}
	release()
	release()
	if _, ended := iss.snapshot(); ended != 1 || len(fm.revoked) != 1 {
		t.Fatalf("ended=%d memory revoked=%v", ended, fm.revoked)
	}
}

// Streamed and one-shot interactive turns both carry the capability bound to
// their thread; a system run does not; every one is revoked when its exec ends.
func TestTurnsIssueAndRevokeTheArtifactCapability(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			iss := &fakeArtifactIssuer{token: "cap-abc"}
			h.r.SetArtifactIssuer(iss)
			h.run(t, thread7)
			if res := h.runStream(t, thread9); res.Err != nil {
				t.Fatalf("RunStream: %v", res.Err)
			}
			sys, _, _ := newStopHarness(t, prov)
			sys.SetArtifactIssuer(iss)
			if _, err := sys.RunFresh(context.Background(), convChat, TierRegistered, nil, "sys"); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprint([]string{convChat + "/7", convChat + "/9"})
			deadline := time.Now().Add(2 * time.Second)
			for {
				issued, ended := iss.snapshot()
				if fmt.Sprint(issued) == want && ended == 2 {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("issued=%v ended=%d, want %s and 2", issued, ended, want)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// SetArtifactIssuer may be called while turns are running (the wiring happens
// after the queue workers start); run with -race.
func TestSetArtifactIssuerIsSafeAgainstConcurrentTurns(t *testing.T) {
	prov := NewClaudeProvider("img")
	r := &Runner{ex: newFakeAgentExec(), provider: prov, log: quietLog()}
	iss := &fakeArtifactIssuer{token: "cap"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, release := r.agentExecEnv("owner", 0, prov, true)
				release()
			}
		}()
	}
	for j := 0; j < 50; j++ {
		r.SetArtifactIssuer(iss)
		r.SetArtifactIssuer(nil)
	}
	wg.Wait()
}

func waitEnded(t *testing.T, iss *fakeArtifactIssuer, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, ended := iss.snapshot()
		if ended == want {
			return
		}
		if ended > want || time.Now().After(deadline) {
			t.Fatalf("ended = %d, want %d", ended, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Q: is the capability revoked when the kill fails? The stream goroutine closes
// the handle after the kill attempt whatever its outcome (runner.go startStream,
// the ctx.Done branch), which ends Done, so the deferred release still runs.
func TestStreamCancelRevokesTheCapabilityEvenWhenTheKillFails(t *testing.T) {
	s := newStreamTestRunner(t)
	s.fx.killDies = false // the kill exec "succeeds" but the process stays alive
	iss := &fakeArtifactIssuer{token: "cap"}
	s.r.SetArtifactIssuer(iss)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, _ := s.start(t, ctx, NewClaudeProvider("img"))
	if _, ended := iss.snapshot(); ended != 0 {
		t.Fatal("revoked while the exec was running")
	}
	cancel()
	readResult(t, stream)
	waitEnded(t, iss, 1)
}

func TestStreamStartErrorsEndTheCapabilityExactlyOnce(t *testing.T) {
	prov := NewClaudeProvider("img")
	t.Run("exec stream error", func(t *testing.T) {
		s := newStreamTestRunner(t)
		s.r.ex = sanitizedExecFixture{agentExecer: s.fx, errorStream: errors.New("daemon down")}
		iss := &fakeArtifactIssuer{token: "cap"}
		s.r.SetArtifactIssuer(iss)
		cmd := prov.RunCmd("hi", "", SessionRef{})
		if _, err := s.r.startStream(context.Background(), "cid", convKey{"chat", 0}, TierRegistered, nil, prov, "hi", fixedCmd(cmd)); err == nil {
			t.Fatal("want an error")
		}
		if issued, ended := iss.snapshot(); len(issued) != 1 || ended != 1 {
			t.Fatalf("issued=%v ended=%d", issued, ended)
		}
	})
	t.Run("record conversation request error", func(t *testing.T) {
		s := newStreamTestRunner(t)
		s.r.store = failingRecordStore{newFakeSessionStore()}
		s.r.containerLocks = make(map[string]*sync.Mutex)
		s.r.provider = prov
		iss := &fakeArtifactIssuer{token: "cap"}
		s.r.SetArtifactIssuer(iss)
		cmd := prov.RunCmd("hi", "", SessionRef{})
		if _, err := s.r.startStream(context.Background(), "cid", convKey{"chat", 0}, TierRegistered, nil, prov, "hi", fixedCmd(cmd)); err == nil {
			t.Fatal("want an error")
		}
		if issued, ended := iss.snapshot(); len(issued) != 1 || ended != 1 {
			t.Fatalf("issued=%v ended=%d", issued, ended)
		}
	})
}

type failingRecordStore struct{ *fakeSessionStore }

func (failingRecordStore) ConversationHandoff(context.Context, string, int, string) (string, error) {
	return "", nil
}
func (failingRecordStore) CompleteConversation(context.Context, string, int, string, string) error {
	return nil
}
func (failingRecordStore) ClearConversationContinuity(context.Context, string, int) error { return nil }
func (failingRecordStore) PersistProviderChoice(context.Context, string, string) error    { return nil }
func (failingRecordStore) RecordConversationRequest(context.Context, string, int, string) error {
	return errors.New("db down")
}

// Pins the owner's decision: Runner.Run is the entry point of every one-shot
// turn, including the scheduled silent turns of telegram.runTaskSilently, and it
// issues the capability like a foreground turn. The capability can only deliver
// files from the owner's own workspace to the owner's own chat.
func TestRunIssuesTheCapabilityForOneShotTurnsIncludingScheduledOnes(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			iss := &fakeArtifactIssuer{token: "cap"}
			h.r.SetArtifactIssuer(iss)
			h.run(t, 0)
			if issued, ended := iss.snapshot(); len(issued) != 1 || issued[0] != convChat+"/0" || ended != 1 {
				t.Fatalf("issued=%v ended=%d", issued, ended)
			}
		})
	}
}
