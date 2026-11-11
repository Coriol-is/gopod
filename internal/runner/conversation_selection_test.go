package runner

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func scopedHarness(t *testing.T) (*convHarness, *store.Store) {
	t.Helper()
	h := newConvHarness(t, NewClaudeProvider("img"))
	s := continuityTestStore(t)
	if e := s.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: convChat, IsOwner: true, AddedAt: 1}); e != nil {
		t.Fatal(e)
	}
	h.r.SetStore(s)
	h.r.EnableConversationSelection(map[string]AgentProvider{"claude": NewClaudeProvider("img"), "codex": NewCodexProvider("codex-img")})
	return h, s
}
func setSelection(t *testing.T, r *Runner, thread int, kind, value string) {
	t.Helper()
	sel, e := r.ConversationSelection(t.Context(), convChat, thread)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.ChangeConversationSelection(t.Context(), sel, kind, value); e != nil {
		t.Fatal(e)
	}
}
func TestScopedProviderRuntimeAlternationAndModels(t *testing.T) {
	h, s := scopedHarness(t)
	setSelection(t, h.r, 7, "provider", "codex")
	setSelection(t, h.r, 7, "model", "gpt-5")
	setSelection(t, h.r, 8, "model", "haiku")
	for _, thread := range []int{7, 8, 7, 0} {
		if _, e := h.r.Run(t.Context(), convChat, thread, TierOwner, nil, "isolated prompt"); e != nil {
			t.Fatal(e)
		}
		cmd := h.home.lastRun()
		if thread == 7 {
			if cmd[0] != "codex" || !containsSeq(cmd, "-m", "gpt-5") {
				t.Fatalf("codex routing: %v", cmd)
			}
		} else {
			if cmd[0] != "claude" {
				t.Fatalf("claude routing: %v", cmd)
			}
			if thread == 8 && !containsSeq(cmd, "--model", "haiku") {
				t.Fatal(cmd)
			}
		}
	}
	if _, ok, e := s.GetSession(t.Context(), convChat, 7, "codex"); e != nil || !ok {
		t.Fatalf("session missing: %v", e)
	}
	if h.r.ProviderForChat(convChat).Name() != "claude" {
		t.Fatal("last topic mutated global runner")
	}
}
func TestScopedStreamAndMaintenanceSelection(t *testing.T) {
	h, _ := scopedHarness(t)
	setSelection(t, h.r, 0, "provider", "codex")
	setSelection(t, h.r, 7, "provider", "codex")
	setSelection(t, h.r, 7, "model", "gpt-5")
	stream, e := h.r.RunStream(t.Context(), convChat, 7, TierOwner, nil, "stream")
	if e != nil {
		t.Fatal(e)
	}
	if result := readResult(t, stream); result.Err != nil {
		t.Fatal(result.Err)
	}
	if cmd := h.home.lastRun(); !containsSeq(cmd, "-m", "gpt-5") {
		t.Fatal(cmd)
	}
	h.r.ex = &selectionFreshExec{h.home}
	if _, e = h.r.RunFresh(t.Context(), convChat, TierOwner, nil, "maintenance"); e != nil {
		t.Fatal(e)
	}
	if cmd := h.home.lastRun(); cmd[0] != "claude" {
		t.Fatalf("unscoped inherited main: %v", cmd)
	}
	if _, e = h.r.RunFreshInConversation(t.Context(), convChat, 7, TierOwner, nil, "scoped maintenance"); e != nil {
		t.Fatal(e)
	}
	if cmd := h.home.lastRun(); cmd[0] != "codex" || !containsSeq(cmd, "-m", "gpt-5") {
		t.Fatal(cmd)
	}
}
func TestScopedSelectionAllocationFailureAndLoginFence(t *testing.T) {
	h, s := scopedHarness(t)
	setSelection(t, h.r, 7, "provider", "codex")
	before, _ := h.r.ConversationSelection(t.Context(), convChat, 7)
	h.r.SetConversationSelectionGuard(func(context.Context, string) error { return errors.New("login active") })
	if _, e := h.r.ChangeConversationSelection(t.Context(), before, "provider", "claude"); e == nil {
		t.Fatal("login selection allowed")
	}
	if _, e := h.r.Run(t.Context(), convChat, 7, TierOwner, nil, "blocked"); e == nil || !strings.Contains(e.Error(), "login active") {
		t.Fatalf("login run allowed: %v", e)
	}
	after, _ := s.ResolveConversationSelection(t.Context(), convChat, 7, "claude")
	if before != after {
		t.Fatal("refusal changed settings")
	}
}
func TestScopedAttemptSnapshotUsedAfterSettingsChange(t *testing.T) {
	h, s := scopedHarness(t)
	ctx := t.Context()
	id, _, e := s.InsertTurn(ctx, store.Turn{Source: "telegram", SourceID: "1:1", ChatFolder: convChat, ChatID: 1, ThreadID: 7, Text: "request"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.MarkTurnRunning(ctx, id); e != nil {
		t.Fatal(e)
	}
	initial, _ := h.r.ConversationSelection(ctx, convChat, 7)
	if _, e = s.BindTurnSelection(ctx, id, 1, initial); e != nil {
		t.Fatal(e)
	}
	setSelection(t, h.r, 7, "provider", "codex")
	ctx = WithSelectionAttempts(ctx, []store.ResultMember{{TurnID: id, Attempt: 1}})
	if _, e = h.r.Run(ctx, convChat, 7, TierOwner, nil, "continued original attempt"); e != nil {
		t.Fatal(e)
	}
	if cmd := h.home.lastRun(); cmd[0] != "claude" {
		t.Fatalf("attempt changed provider: %v", cmd)
	}
}

type selectionFreshExec struct{ *fakeAgentHome }

func (f *selectionFreshExec) Exec(ctx context.Context, id string, cmd, env []string) (*ExecResult, error) {
	if len(cmd) > 4 && strings.Contains(cmd[2], `echo $$ >`) {
		f.mu.Lock()
		f.runs = append(f.runs, append([]string(nil), cmd[4:]...))
		f.mu.Unlock()
		return &ExecResult{Stdout: "fresh reply"}, nil
	}
	return f.fakeAgentHome.Exec(ctx, id, cmd, env)
}

func TestScopedSelectionWaitsForExecutingAttempt(t *testing.T) {
	g := newGatedRunner(t, 2)
	st := continuityTestStore(t)
	if e := st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "a", IsOwner: true, AddedAt: 1}); e != nil {
		t.Fatal(e)
	}
	g.r.SetStore(st)
	g.r.EnableConversationSelection(map[string]AgentProvider{"claude": NewClaudeProvider("img"), "codex": NewCodexProvider("codex")})
	before, e := g.r.ConversationSelection(t.Context(), "a", 0)
	if e != nil {
		t.Fatal(e)
	}
	pending := g.runStreamAsync(t.Context(), "a")
	proc := g.nextProc(t)
	stream := streamOK(t, pending)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, e = g.r.ChangeConversationSelection(ctx, before, "provider", "codex"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("selection changed during run: %v", e)
	}
	after, _ := g.r.ConversationSelection(t.Context(), "a", 0)
	if before != after {
		t.Fatal("canceled switch mutated settings")
	}
	finish(t, proc, stream)
	if _, e = g.r.ChangeConversationSelection(t.Context(), before, "provider", "codex"); e != nil {
		t.Fatal(e)
	}
}
func TestScopedAllocationFailurePreservesAttemptAndPreferences(t *testing.T) {
	h, st := scopedHarness(t)
	setSelection(t, h.r, 7, "provider", "codex")
	expected, _ := h.r.ConversationSelection(t.Context(), convChat, 7)
	id, _, e := st.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: "1:allocation", ChatFolder: convChat, ChatID: 1, ThreadID: 7, Text: "request"})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.MarkTurnRunning(t.Context(), id); e != nil {
		t.Fatal(e)
	}
	h.r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
		return "", errors.New("allocation unavailable")
	}
	ctx := WithSelectionAttempts(t.Context(), []store.ResultMember{{TurnID: id, Attempt: 1}})
	if _, e = h.r.Run(ctx, convChat, 7, TierOwner, nil, "request"); e == nil {
		t.Fatal("allocation unexpectedly succeeded")
	}
	got, e := st.TurnSelection(t.Context(), id, 1)
	if e != nil || got != expected {
		t.Fatalf("attempt lost: %+v %v", got, e)
	}
	current, _ := h.r.ConversationSelection(t.Context(), convChat, 7)
	if current != expected {
		t.Fatal("allocation changed preference")
	}
}
