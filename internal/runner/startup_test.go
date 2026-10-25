package runner

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
)

// Nil embedded methods panic if startup touches any session/state store method.
type startupNoState struct{ stateStore }

type startupExec struct {
	*fakeAgentExec
	run     func(context.Context, []string) (*ExecResult, error)
	cleanup func(context.Context) (*ExecResult, error)
}

func (e *startupExec) Exec(ctx context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	if isKillCmd(cmd) || (len(cmd) > 0 && cmd[0] == "rm") {
		if e.cleanup != nil {
			return e.cleanup(ctx)
		}
		return &ExecResult{}, nil
	}
	if len(cmd) > 4 && cmd[0] == "sh" && cmd[1] == "-c" {
		cmd = cmd[4:]
	}
	return e.run(ctx, cmd)
}
func startupRunner(t *testing.T, prov AgentProvider, run func(context.Context, []string) (*ExecResult, error)) *Runner {
	t.Helper()
	return &Runner{provider: prov, ex: &startupExec{fakeAgentExec: newFakeAgentExec(), run: run}, log: quietLog(), activity: make(map[string]time.Time), ensureFn: func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) { return "container-id", nil }}
}
func TestStartupCheckUsesRestoredProviderModel(t *testing.T) {
	var calls [][]string
	r := startupRunner(t, NewClaudeProvider("claude-image"), func(ctx context.Context, cmd []string) (*ExecResult, error) {
		calls = append(calls, cmd)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("exec unbounded")
		}
		if cmd[0] != "codex" {
			t.Fatalf("default provider called: %v", cmd)
		}
		if reflect.DeepEqual(cmd, []string{"codex", "--version"}) {
			return &ExecResult{Stdout: "codex-cli 0.164.0\nsecret"}, nil
		}
		if reflect.DeepEqual(cmd, []string{"codex", "login", "status"}) {
			return &ExecResult{Stderr: "Logged in using ChatGPT"}, nil
		}
		t.Fatalf("model/session command called: %v", cmd)
		return nil, nil
	})
	r.SetStore(startupNoState{})
	r.RestoreChatProvider("owner", NewCodexProvider("selected-image"))
	r.SetChatModel("owner", "chosen-model")
	r.ensureFn = func(ctx context.Context, folder string, tier Tier, _ *mountsec.Allowlist) (string, error) {
		if folder != "owner" || tier != TierOwner || r.ProviderForChat(folder).Image() != "selected-image" {
			t.Fatal("wrong spawn")
		}
		return "container-id", nil
	}
	got := r.StartupCheck(t.Context(), "owner", TierOwner, nil)
	if got.Provider != "codex" || got.Model != "chosen-model" || got.Image != "selected-image" || got.Container != "ready" || got.CLI != "ready" || got.Auth != "authenticated" || got.Version != "0.164.0" || len(calls) != 2 {
		t.Fatalf("%+v calls=%v", got, calls)
	}
	if r.LastActivity("owner").IsZero() {
		t.Fatal("warm container absent from idle tracking")
	}
	if r.ModelForChat("owner") != "chosen-model" {
		t.Fatal("model changed")
	}
}
func TestStartupCheckCanceledAndEnsureFailure(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		t.Run(map[bool]string{true: "canceled", false: "ensure-error"}[cancelBefore], func(t *testing.T) {
			r := startupRunner(t, NewClaudeProvider(""), func(context.Context, []string) (*ExecResult, error) { t.Fatal("unexpected exec"); return nil, nil })
			r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
				return "", errors.New("secret")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelBefore {
				cancel()
			}
			got := r.StartupCheck(ctx, "owner", TierOwner, nil)
			want := "error"
			if cancelBefore {
				want = "canceled"
			}
			if got.Container != want || strings.Contains(got.Version, "secret") || got.CLI != "unknown" {
				t.Fatal(got)
			}
		})
	}
}
func TestStartupCheckCanceledExecAndSlot(t *testing.T) {
	r := startupRunner(t, NewClaudeProvider(""), func(ctx context.Context, _ []string) (*ExecResult, error) { <-ctx.Done(); return nil, ctx.Err() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	got := r.StartupCheck(ctx, "owner", TierOwner, nil)
	if got.CLI != "timeout" || got.Auth != "timeout" {
		t.Fatal(got)
	}
	release, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestSelectedProviderAuthStatus(t *testing.T) {
	cases := []struct {
		name, stdout, stderr string
		exit                 int
		logged               bool
		wantErr              bool
	}{
		{"stderr-auth", "", "Logged in using ChatGPT", 0, true, false},
		{"negative", "", "Not logged in", 1, false, false},
		{"misleading-exit", "Logged in using API key", "", 1, false, true},
		{"unknown", "", "credentials secret parse error", 1, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := startupRunner(t, NewClaudeProvider(""), func(_ context.Context, cmd []string) (*ExecResult, error) {
				if cmd[0] != "codex" {
					t.Fatal(cmd)
				}
				return &ExecResult{Stdout: tc.stdout, Stderr: tc.stderr, ExitCode: tc.exit}, nil
			})
			r.RestoreChatProvider("owner", NewCodexProvider(""))
			got, err := r.CheckAuth(t.Context(), "owner", TierOwner, nil)
			if (err != nil) != tc.wantErr || got.LoggedIn != tc.logged {
				t.Fatal(got, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestCodexAuthNegativeAndUnknown(t *testing.T) {
	p := NewCodexProvider("")
	for _, s := range []string{"Not logged in", "NOT LOGGED IN", "warning\nNot logged in"} {
		got, err := p.ParseAuthStatus(s)
		if err != nil || got.LoggedIn {
			t.Fatal(got, err)
		}
	}
	for _, s := range []string{"", `{}`, "unrecognized secret"} {
		_, err := p.ParseAuthStatus(s)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
}

func TestStartupCheckWaitsForChatSlotWithoutMutation(t *testing.T) {
	r := startupRunner(t, NewClaudeProvider(""), func(context.Context, []string) (*ExecResult, error) { t.Fatal("exec while chat busy"); return nil, nil })
	r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
		t.Fatal("ensure while chat busy")
		return "", nil
	}
	release, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	if got := r.StartupCheck(ctx, "owner", TierOwner, nil); got.Container != "timeout" {
		t.Fatal(got)
	}
}
func TestAuthErrorClassificationUsesSelectedProvider(t *testing.T) {
	r := startupRunner(t, NewClaudeProvider(""), func(_ context.Context, cmd []string) (*ExecResult, error) {
		if cmd[0] != "codex" {
			t.Fatal("used default provider", cmd)
		}
		return &ExecResult{Stderr: "Not logged in", ExitCode: 1}, nil
	})
	if err := r.classifyAuthError(t.Context(), "container", NewCodexProvider("")); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal(err)
	}
}
func TestStartupVersionNeverReflectsRawOutput(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{"secret <script>", ""}, {"2.1.310 (Claude Code)", "2.1.310"}, {"codex-cli 0.164.0\nsecret-token", "0.164.0"}, {strings.Repeat("s", 300) + "0.164.0", ""}} {
		if got := safeStartupVersion(tc.raw); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}

func TestBlockedSelectionStopsEnsureAndStartup(t *testing.T) {
	r := startupRunner(t, NewClaudeProvider(""), func(context.Context, []string) (*ExecResult, error) {
		t.Fatal("blocked selection executed CLI")
		return nil, nil
	})
	r.SetStore(startupNoState{})
	r.BlockChatSelection("owner")
	// Real Ensure must refuse before locks, paths or Docker access.
	if _, err := r.Ensure(t.Context(), "owner", TierOwner, nil); err == nil {
		t.Fatal("blocked Ensure allowed")
	}
	if _, err := r.ensure(t.Context(), "owner", TierOwner, nil); err == nil {
		t.Fatal("test seam bypassed block")
	}
	if got := r.StartupCheck(t.Context(), "owner", TierOwner, nil); got.Container != "selection_unavailable" {
		t.Fatal(got)
	}
	r.BlockChatSelection("other")
	r.RestoreChatProvider("owner", NewCodexProvider(""))
	if r.selectionBlocked("owner") || !r.selectionBlocked("other") {
		t.Fatal("selection resolution scope wrong")
	}
}
func TestCanceledLocalProbeHoldsSlotThroughCleanup(t *testing.T) {
	entered := make(chan struct{})
	started := make(chan struct{})
	releaseCleanup := make(chan struct{})
	r := startupRunner(t, NewClaudeProvider(""), func(ctx context.Context, _ []string) (*ExecResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	r.ex.(*startupExec).cleanup = func(ctx context.Context) (*ExecResult, error) {
		close(entered)
		select {
		case <-releaseCleanup:
			return &ExecResult{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan StartupReport, 1)
	go func() { done <- r.StartupCheck(ctx, "owner", TierOwner, nil) }()
	<-started
	cancel()
	<-entered
	waiterCtx, stop := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer stop()
	if release, err := r.acquireAgentSlot(waiterCtx, "owner"); err == nil {
		release()
		t.Fatal("slot released before cleanup")
	}
	close(releaseCleanup)
	got := <-done
	if got.CLI != "canceled" {
		t.Fatal(got)
	}
}

func TestLocalProbeTransportFailureKillsItsPID(t *testing.T) {
	killed := false
	r := startupRunner(t, NewClaudeProvider(""), func(context.Context, []string) (*ExecResult, error) {
		return nil, errors.New("token-bearing connection dropped")
	})
	ex := &transportProbeExec{fakeAgentExec: newFakeAgentExec(), t: t, killed: &killed}
	r.ex = ex
	if _, err := r.execLocalProbe(t.Context(), "id", []string{"claude", "--version"}); err == nil || strings.Contains(err.Error(), "token") {
		t.Fatal(err)
	}
	if !killed {
		t.Fatal("ambiguous exec failure did not clean exact pid")
	}
}

type transportProbeExec struct {
	*fakeAgentExec
	t      *testing.T
	pid    string
	killed *bool
}

func (e *transportProbeExec) Exec(_ context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	if isKillCmd(cmd) {
		if cmd[3] != e.pid {
			e.t.Fatal("killed wrong pid", cmd)
		}
		*e.killed = true
		return &ExecResult{}, nil
	}
	if len(cmd) > 4 && cmd[0] == "sh" {
		e.pid = cmd[3]
		return nil, errors.New("secret connection dropped")
	}
	e.t.Fatal("unexpected cleanup", cmd)
	return nil, nil
}

func TestStartupCleanupFailureStopsFurtherProbes(t *testing.T) {
	calls := 0
	r := startupRunner(t, NewClaudeProvider(""), func(context.Context, []string) (*ExecResult, error) {
		calls++
		return &ExecResult{Stdout: "2.1.310"}, nil
	})
	r.ex.(*startupExec).cleanup = func(context.Context) (*ExecResult, error) { return nil, errors.New("secret cleanup failure") }
	got := r.StartupCheck(t.Context(), "owner", TierOwner, nil)
	if got.CLI != "cleanup_failed" || got.Auth != "unknown" || calls != 1 {
		t.Fatal(got, calls)
	}
}

func cachedStartupRunner(t *testing.T) *Runner {
	t.Helper()
	return startupRunner(t, NewClaudeProvider("selected-image"), func(_ context.Context, cmd []string) (*ExecResult, error) {
		if reflect.DeepEqual(cmd, []string{"claude", "--version"}) {
			return &ExecResult{Stdout: "2.1.310 (Claude Code)"}, nil
		}
		return &ExecResult{Stdout: `{"loggedIn":true,"authMethod":"oauth"}`}, nil
	})
}
func TestCachedStartupObservationCopiedAndInvalidated(t *testing.T) {
	r := cachedStartupRunner(t)
	if _, ok := r.CachedStartupObservation("owner"); ok {
		t.Fatal("fabricated observation before check")
	}
	report := r.StartupCheck(t.Context(), "owner", TierOwner, nil)
	if report.Auth != "authenticated" {
		t.Fatalf("startup %+v", report)
	}
	observed, ok := r.CachedStartupObservation("owner")
	if !ok || observed.CheckedAt.IsZero() || observed.Report.Version != "2.1.310" {
		t.Fatal(observed, ok)
	}
	// Cached reads cannot invoke Docker/exec, and returned values are isolated.
	r.ex = nil
	r.d = nil
	r.ensureFn = nil
	observed.Report.Version = "changed"
	again, ok := r.CachedStartupObservation("owner")
	if !ok || again.Report.Version != "2.1.310" {
		t.Fatal("cache mutated through returned value")
	}
	r.SetChatModel("owner", "different")
	r.SetChatModel("owner", "")
	if _, ok := r.CachedStartupObservation("owner"); ok {
		t.Fatal("model change/revert revived old observation")
	}
}
func TestCachedStartupSelectionChangesDuringProbeCannotRevive(t *testing.T) {
	r := cachedStartupRunner(t)
	r.ex = &startupExec{fakeAgentExec: newFakeAgentExec(), run: func(_ context.Context, cmd []string) (*ExecResult, error) {
		if reflect.DeepEqual(cmd, []string{"claude", "--version"}) {
			r.SetChatModel("owner", "other")
			r.SetChatModel("owner", "")
			return &ExecResult{Stdout: "2.1.310"}, nil
		}
		return &ExecResult{Stdout: `{"loggedIn":true,"authMethod":"oauth"}`}, nil
	}}
	r.StartupCheck(t.Context(), "owner", TierOwner, nil)
	if _, ok := r.CachedStartupObservation("owner"); ok {
		t.Fatal("in-flight old generation cached")
	}
}
func TestCachedStartupObservationProviderAndBlockInvalidation(t *testing.T) {
	for _, change := range []func(*Runner){
		func(r *Runner) {
			r.RestoreChatProvider("owner", NewCodexProvider(""))
			r.RestoreChatProvider("owner", NewClaudeProvider("selected-image"))
		},
		func(r *Runner) { r.BlockChatSelection("owner") },
		func(r *Runner) { r.ClearChatProvider("owner") },
	} {
		r := cachedStartupRunner(t)
		r.StartupCheck(t.Context(), "owner", TierOwner, nil)
		change(r)
		if _, ok := r.CachedStartupObservation("owner"); ok {
			t.Fatal("selection change revived snapshot")
		}
	}
}
func TestCachedStartupObservationConcurrentReadsAndSelection(t *testing.T) {
	r := cachedStartupRunner(t)
	r.StartupCheck(t.Context(), "owner", TierOwner, nil)
	var wg sync.WaitGroup
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				r.CachedStartupObservation("owner")
				r.SetChatModel("owner", "other")
				r.SetChatModel("owner", "")
			}
		}()
	}
	wg.Wait()
	if _, ok := r.CachedStartupObservation("owner"); ok {
		t.Fatal("stale cache retained")
	}
}
