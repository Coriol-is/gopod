package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

type bootStore struct {
	chats    []store.RegisteredChat
	state    map[string]string
	fail     string
	listFail bool
}

func (s bootStore) ListRegistered(context.Context) ([]store.RegisteredChat, error) {
	if s.listFail {
		return nil, errors.New("unavailable")
	}
	return s.chats, nil
}
func (s bootStore) GetState(_ context.Context, key string) (string, error) {
	if key == s.fail {
		return "", errors.New("unavailable")
	}
	return s.state[key], nil
}

type bootAgent struct {
	blocked   []string
	providers map[string]string
	models    map[string]string
	checks    []string
	check     func(context.Context, string, runner.Tier)
}

func (a *bootAgent) BlockChatSelection(f string) { a.blocked = append(a.blocked, f) }
func (a *bootAgent) RestoreChatProvider(f string, p runner.AgentProvider) {
	if a.providers == nil {
		a.providers = map[string]string{}
	}
	a.providers[f] = p.Name()
}
func (a *bootAgent) SetChatModel(f, m string) {
	if a.models == nil {
		a.models = map[string]string{}
	}
	a.models[f] = m
}
func (a *bootAgent) StartupCheck(ctx context.Context, f string, tier runner.Tier, _ *mountsec.Allowlist) runner.StartupReport {
	a.checks = append(a.checks, f)
	if a.check != nil {
		a.check(ctx, f, tier)
	}
	return runner.StartupReport{Provider: a.providers[f], Model: a.models[f]}
}
func bootProviders() map[string]runner.AgentProvider {
	return map[string]runner.AgentProvider{"claude": runner.NewClaudeProvider(""), "codex": runner.NewCodexProvider("")}
}
func bootLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func TestStartupRestoresAllBeforeOwnerWarmup(t *testing.T) {
	s := bootStore{chats: []store.RegisteredChat{{JID: "tg:42", Folder: "owner"}, {JID: "tg:99", Folder: "other"}}, state: map[string]string{"provider:owner": "codex", "model:owner": "gpt-5", "provider:other": "claude", "model:other": "sonnet"}}
	a := &bootAgent{}
	a.check = func(ctx context.Context, folder string, tier runner.Tier) {
		if a.providers["owner"] != "codex" || a.models["owner"] != "gpt-5" || a.providers["other"] != "claude" || a.models["other"] != "sonnet" {
			t.Fatal("warmup before restore")
		}
		if folder != "owner" || tier != runner.TierOwner {
			t.Fatal("wrong workspace/tier")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 20*time.Second {
			t.Fatal("unbounded warmup")
		}
	}
	restoreAndWarmOwner(t.Context(), s, a, 42, bootProviders(), nil, bootLog())
	if len(a.checks) != 1 {
		t.Fatal(a.checks)
	}
}
func TestStartupNeverWarmsUntrustedOrUnknownSelection(t *testing.T) {
	for _, test := range []struct {
		name  string
		s     bootStore
		owner int64
	}{
		{"unregistered", bootStore{}, 42},
		{"no_owner", bootStore{chats: []store.RegisteredChat{{JID: "tg:42", Folder: "owner", IsOwner: true}}}, 0},
		{"not_configured_owner", bootStore{chats: []store.RegisteredChat{{JID: "tg:99", Folder: "other", IsOwner: true}}}, 42},
		{"unknown_provider", bootStore{chats: []store.RegisteredChat{{JID: "tg:42", Folder: "owner"}}, state: map[string]string{"provider:owner": "invalid"}}, 42},
		{"provider_read", bootStore{chats: []store.RegisteredChat{{JID: "tg:42", Folder: "owner"}}, fail: "provider:owner"}, 42},
		{"model_read", bootStore{chats: []store.RegisteredChat{{JID: "tg:42", Folder: "owner"}}, fail: "model:owner"}, 42},
		{"list_failure", bootStore{listFail: true}, 42},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := &bootAgent{}
			restoreAndWarmOwner(t.Context(), test.s, a, test.owner, bootProviders(), nil, bootLog())
			if len(a.checks) != 0 {
				t.Fatal(a.checks)
			}
		})
	}
}

func TestStartupRegistrationReadFailureStopsRecovery(t *testing.T) {
	a := &bootAgent{}
	if err := restoreAndWarmOwner(t.Context(), bootStore{listFail: true}, a, 42, bootProviders(), nil, bootLog()); err == nil {
		t.Fatal("unrestored global selections allowed startup")
	}
}
func TestStartupFailedChatIsBlockedWhileOtherChatRestores(t *testing.T) {
	a := &bootAgent{}
	s := bootStore{chats: []store.RegisteredChat{{JID: "tg:42", Folder: "owner"}, {JID: "tg:99", Folder: "other"}}, state: map[string]string{"provider:owner": "codex", "provider:other": "missing"}}
	if err := restoreAndWarmOwner(t.Context(), s, a, 42, bootProviders(), nil, bootLog()); err != nil {
		t.Fatal(err)
	}
	if len(a.blocked) != 1 || a.blocked[0] != "other" || len(a.checks) != 1 || a.checks[0] != "owner" {
		t.Fatal(a)
	}
}

type bootManagementAgent struct {
	*bootAgent
	ready           bool
	managementCalls int
	t               *testing.T
}

func (a *bootManagementAgent) StartupCheck(ctx context.Context, f string, tier runner.Tier, al *mountsec.Allowlist) runner.StartupReport {
	a.bootAgent.StartupCheck(ctx, f, tier, al)
	r := runner.StartupReport{Provider: "codex", Container: "ready", CLI: "ready", Auth: "not_authenticated"}
	if a.ready {
		r.Auth = "authenticated"
	}
	return r
}
func (a *bootManagementAgent) ManagementCheck(ctx context.Context, f string) runner.ManagementReport {
	a.managementCalls++
	if len(a.checks) != 1 || f != "owner" {
		a.t.Fatal("management before warmup or wrong chat")
	}
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > 10*time.Second {
		a.t.Fatal("unbounded management")
	}
	return runner.ManagementReport{Provider: "codex", Auth: "authenticated_chatgpt", Quota: "observed", Inference: "unverified"}
}
func TestStartupManagementOnlyAfterLocalReadiness(t *testing.T) {
	for _, ready := range []bool{false, true} {
		a := &bootManagementAgent{bootAgent: &bootAgent{}, ready: ready, t: t}
		s := bootStore{chats: []store.RegisteredChat{{JID: "tg:42", Folder: "owner"}}, state: map[string]string{"provider:owner": "codex"}}
		if err := restoreAndWarmOwner(t.Context(), s, a, 42, bootProviders(), nil, bootLog()); err != nil {
			t.Fatal(err)
		}
		want := 0
		if ready {
			want = 1
		}
		if a.managementCalls != want {
			t.Fatalf("management calls=%d want%d", a.managementCalls, want)
		}
	}
}
