package runner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func continuityTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProviderContinuityRoundTripPerThread(t *testing.T) {
	ctx := context.Background()
	h := newConvHarness(t, NewClaudeProvider("img"))
	s := continuityTestStore(t)
	h.r.SetStore(s)
	run := func(thread int, prompt string, stream bool) {
		t.Helper()
		if stream {
			st, err := h.r.RunStream(ctx, convChat, thread, TierRegistered, nil, prompt)
			if err != nil {
				t.Fatal(err)
			}
			if res := readResult(t, st); res.Err != nil {
				t.Fatal(res.Err)
			}
		} else if _, err := h.r.Run(ctx, convChat, thread, TierRegistered, nil, prompt); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []struct {
		thread int
		text   string
	}{{0, "MAIN-UNIQUE"}, {7, "SEVEN-UNIQUE"}, {8, "EIGHT-UNIQUE"}} {
		run(v.thread, v.text, false)
	}
	old, ok, err := s.GetSession(ctx, convChat, 7, "claude")
	if err != nil || !ok {
		t.Fatal("missing native session")
	}
	if err := h.r.SwitchChatProvider(ctx, convChat, NewCodexProvider("img-codex")); err != nil {
		t.Fatal(err)
	}
	if kept, ok, _ := s.GetSession(ctx, convChat, 7, "claude"); !ok || kept != old {
		t.Fatal("switch deleted old session")
	}
	run(7, "codex followup", true)
	cmd := h.home.lastRun()
	prompt := cmd[len(cmd)-1]
	if !strings.Contains(prompt, "SEVEN-UNIQUE") || !strings.Contains(prompt, `"role":"assistant"`) {
		t.Fatalf("bridge missing %q", prompt)
	}
	for _, bad := range []string{"MAIN-UNIQUE", "EIGHT-UNIQUE"} {
		if strings.Contains(prompt, bad) {
			t.Fatalf("thread leaked %s", bad)
		}
	}
	run(7, "same provider", false)
	if cmd = h.home.lastRun(); strings.Contains(cmd[len(cmd)-1], "[gopod conversation handoff]") {
		t.Fatal("redundant bridge")
	}
	// A process restart restores provider state and retains the independent transcript.
	h2 := newConvHarness(t, NewCodexProvider("img-codex"))
	h2.home = h.home
	h2.r.ex = h.home
	h2.r.SetStore(s)
	if err := h2.r.SwitchChatProvider(ctx, convChat, NewClaudeProvider("img")); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.r.Run(ctx, convChat, 7, TierRegistered, nil, "continue back"); err != nil {
		t.Fatal(err)
	}
	cmd = h.home.lastRun()
	if id, ok := flagValue(cmd, "--resume"); !ok || id != old {
		t.Fatalf("native session not resumed: %v", cmd)
	}
	if !strings.Contains(cmd[len(cmd)-1], "codex followup") {
		t.Fatal("return missed Codex work")
	}
	if err := h2.r.ClearSession(ctx, convChat, 7, TierRegistered, nil); err != nil {
		t.Fatal(err)
	}
	if err := h2.r.SwitchChatProvider(ctx, convChat, NewCodexProvider("img-codex")); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.r.Run(ctx, convChat, 7, TierRegistered, nil, "clean start"); err != nil {
		t.Fatal(err)
	}
	cmd = h.home.lastRun()
	if strings.Contains(cmd[len(cmd)-1], "SEVEN-UNIQUE") || strings.Contains(cmd[len(cmd)-1], "codex followup") {
		t.Fatal("clear resurrected history")
	}
}

func TestProviderSwitchWaitsForRunAndHonorsCancellation(t *testing.T) {
	g := newGatedRunner(t, 2)
	s := continuityTestStore(t)
	g.r.SetStore(s)
	pending := g.runStreamAsync(context.Background(), "a")
	p := g.nextProc(t)
	stream := streamOK(t, pending)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := g.r.SwitchChatProvider(ctx, "a", NewCodexProvider("img-codex"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("switch didn't wait: %v", err)
	}
	if g.r.ProviderForChat("a").Name() != "claude" {
		t.Fatal("cancelled switch changed provider")
	}
	finish(t, p, stream)
	if err := g.r.SwitchChatProvider(context.Background(), "a", NewCodexProvider("img-codex")); err != nil {
		t.Fatal(err)
	}
	if value, _ := s.GetState(context.Background(), "provider:a"); value != "codex" {
		t.Fatalf("provider not durable %q", value)
	}
	history, err := s.ConversationHandoff(context.Background(), "a", 0, "codex")
	if err != nil || !strings.Contains(history, "hi a") {
		t.Fatalf("handoff missing before Telegram reply: %s %v", history, err)
	}
}

func TestProviderSwitchPersistenceFailureKeepsCurrent(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	s := continuityTestStore(t)
	h.r.SetStore(s)
	s.Close()
	if err := h.r.SwitchChatProvider(context.Background(), convChat, NewCodexProvider("img-codex")); err == nil {
		t.Fatal("ignored DB failure")
	}
	if h.r.ProviderForChat(convChat).Name() != "claude" {
		t.Fatal("in-memory choice changed on persistence failure")
	}
}

func TestFailedProviderRequestRemainsInHandoff(t *testing.T) {
	ctx := context.Background()
	h := newConvHarness(t, NewClaudeProvider("img"))
	s := continuityTestStore(t)
	h.r.SetStore(s)
	h.home.failNextClaudeNew = true
	if _, err := h.r.Run(ctx, convChat, 7, TierRegistered, nil, "unfinished user task"); err == nil {
		t.Fatal("expected provider failure")
	}
	if err := h.r.SwitchChatProvider(ctx, convChat, NewCodexProvider("img-codex")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.r.Run(ctx, convChat, 7, TierRegistered, nil, "continue"); err != nil {
		t.Fatal(err)
	}
	cmd := h.home.lastRun()
	if !strings.Contains(cmd[len(cmd)-1], "unfinished user task") {
		t.Fatal("failed provider request lost")
	}
}

type completionFailureStore struct{ *store.Store }

func (s completionFailureStore) CompleteConversation(context.Context, string, int, string, string) error {
	return errors.New("disk full")
}

func TestConversationCompletionFailureIsNotReportedAsSuccess(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "stream"}[stream], func(t *testing.T) {
			h := newConvHarness(t, NewClaudeProvider("img"))
			s := continuityTestStore(t)
			h.r.SetStore(completionFailureStore{s})
			ctx := context.Background()
			var err error
			if stream {
				var st *AgentStream
				st, err = h.r.RunStream(ctx, convChat, 7, TierRegistered, nil, "task")
				if err == nil {
					err = readResult(t, st).Err
				}
			} else {
				_, err = h.r.Run(ctx, convChat, 7, TierRegistered, nil, "task")
			}
			if err == nil || !strings.Contains(err.Error(), "saving conversation reply") {
				t.Fatalf("silent completion failure: %v", err)
			}
			bridge, e := s.ConversationHandoff(ctx, convChat, 7, "codex")
			if e != nil || !strings.Contains(bridge, "task") || strings.Contains(bridge, `"role":"assistant"`) {
				t.Fatalf("incorrect atomic result: %s %v", bridge, e)
			}
		})
	}
}

type blockedProviderStore struct {
	*store.Store
	entered chan struct{}
	proceed chan struct{}
}

func (s blockedProviderStore) PersistProviderChoice(ctx context.Context, folder, provider string) error {
	close(s.entered)
	select {
	case <-s.proceed:
		return s.Store.PersistProviderChoice(ctx, folder, provider)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestProviderSwitchExcludesEnsureLifecycle(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	s := blockedProviderStore{continuityTestStore(t), make(chan struct{}), make(chan struct{})}
	h.r.SetStore(s)
	done := make(chan error, 1)
	go func() { done <- h.r.SwitchChatProvider(context.Background(), convChat, NewCodexProvider("codex-img")) }()
	select {
	case <-s.entered:
	case <-time.After(time.Second):
		t.Fatal("switch not persisting")
	}
	// Ensure takes this lifecycle lock before choosing an image. It must not
	// recreate the old image between removal and the durable provider change.
	lifecycle := make(chan string, 1)
	go func() {
		h.r.lockChat(convChat)
		defer h.r.unlockChat(convChat)
		lifecycle <- h.r.ProviderForChat(convChat).Name()
	}()
	select {
	case p := <-lifecycle:
		t.Fatalf("Ensure could select %s mid-switch", p)
	case <-time.After(30 * time.Millisecond):
	}
	close(s.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-lifecycle:
		if p != "codex" {
			t.Fatalf("Ensure saw %s", p)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle lock leaked")
	}
}

func TestQuotaSwitchExpectedProviderIsCheckedBeforeMutation(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	s := continuityTestStore(t)
	h.r.SetStore(s)
	h.r.SetChatModel(convChat, "opus")
	err := h.r.SwitchChatProviderIfCurrent(context.Background(), convChat, "codex", NewClaudeProvider("img"))
	if !errors.Is(err, ErrProviderChanged) {
		t.Fatalf("stale offer allowed: %v", err)
	}
	if h.r.ModelForChat(convChat) != "opus" {
		t.Fatal("stale offer reset model")
	}
	saved, _ := s.GetState(context.Background(), "provider:"+convChat)
	if saved != "" {
		t.Fatal("stale offer changed persistent choice")
	}
	if err = h.r.SwitchChatProviderIfCurrent(context.Background(), convChat, "claude", NewCodexProvider("img-codex")); err != nil {
		t.Fatal(err)
	}
	if h.r.ProviderForChat(convChat).Name() != "codex" || h.r.ModelForChat(convChat) != "" {
		t.Fatal("valid offer failed")
	}
}
