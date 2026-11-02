package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

type freezeFunc func(context.Context, store.ExecutionRef, string) (store.TurnResult, error)

func (f freezeFunc) FreezeTurnResult(ctx context.Context, ref store.ExecutionRef, text string) (store.TurnResult, error) {
	return f(ctx, ref, text)
}

func freezeFixture(t *testing.T) (*store.Store, store.ExecutionRef) {
	t.Helper()
	s := continuityTestStore(t)
	if err := s.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: convChat, IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	conv, err := s.SnapshotConversation(t.Context(), "tg:1", 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	ref := store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive}
	for _, sourceID := range []string{"1:1", "1:2"} {
		id, _, err := s.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: sourceID, ChatFolder: convChat, ThreadID: 7, Text: "question"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkTurnRunning(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		ref.Members = append(ref.Members, store.ResultMember{TurnID: id, Attempt: 1})
	}
	return s, ref
}

func TestRunnerFreezesCoalescedResultBeforeReturning(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "stream"}[stream], func(t *testing.T) {
			s, ref := freezeFixture(t)
			h := newConvHarness(t, NewClaudeProvider("img"))
			h.r.SetStore(s)
			ctx := WithResultFreeze(t.Context(), ref, freezeFunc(func(ctx context.Context, ref store.ExecutionRef, text string) (store.TurnResult, error) {
				wait, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
				if release, err := h.r.acquireAgentSlot(wait, convChat); err == nil {
					release()
					t.Error("runtime slot released before result commit")
				}
				return s.FreezeTurnResult(ctx, ref, text)
			}))
			// Caller mutations must not alter membership after admission.
			ref.Members[0].Attempt = 999
			var text string
			if stream {
				st, err := h.r.RunStream(ctx, convChat, 7, TierRegistered, nil, "question")
				if err != nil {
					t.Fatal(err)
				}
				res := readResult(t, st)
				if res.Err != nil {
					t.Fatal(res.Err)
				}
				text = res.FullText
			} else {
				var err error
				text, err = h.r.Run(ctx, convChat, 7, TierRegistered, nil, "question")
				if err != nil {
					t.Fatal(err)
				}
			}
			a, ok, err := s.TurnResultForAttempt(t.Context(), ref.Members[0].TurnID, 1)
			if err != nil || !ok {
				t.Fatalf("missing freeze: %v", err)
			}
			b, ok, err := s.TurnResultForAttempt(t.Context(), ref.Members[1].TurnID, 1)
			if err != nil || !ok || a.ID != b.ID || a.Text != text || a.Provider != "claude" {
				t.Fatalf("result mismatch: %+v %+v %v", a, b, err)
			}
			history, err := s.ConversationHandoff(t.Context(), convChat, 7, "codex")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(history, `"role":"assistant"`) != 1 {
				t.Fatalf("duplicate continuity: %s", history)
			}
		})
	}
}

func TestRunnerFreezeFailureIsNonReplayable(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "stream"}[stream], func(t *testing.T) {
			s, ref := freezeFixture(t)
			h := newConvHarness(t, NewClaudeProvider("img"))
			h.r.SetStore(s)
			failure := errors.New("commit failed")
			calls := 0
			ctx := WithResultFreeze(t.Context(), ref, freezeFunc(func(ctx context.Context, got store.ExecutionRef, text string) (store.TurnResult, error) {
				calls++
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				return store.TurnResult{}, failure
			}))
			var err error
			if stream {
				var st *AgentStream
				st, err = h.r.RunStream(ctx, convChat, 7, TierRegistered, nil, "question")
				if err == nil {
					err = readResult(t, st).Err
				}
			} else {
				_, err = h.r.Run(ctx, convChat, 7, TierRegistered, nil, "question")
			}
			var frozen *ResultFreezeError
			if !errors.As(err, &frozen) || !errors.Is(err, failure) || !frozen.NoReplay() || calls != 1 {
				t.Fatalf("failure lost: %v calls=%d", err, calls)
			}
			history, _ := s.ConversationHandoff(t.Context(), convChat, 7, "codex")
			if strings.Contains(history, `"role":"assistant"`) {
				t.Fatalf("failed freeze wrote continuity: %s", history)
			}
		})
	}
}

func TestInvalidFreezeBindingPreventsNativeExecution(t *testing.T) {
	s, ref := freezeFixture(t)
	for _, stream := range []bool{false, true} {
		h := newConvHarness(t, NewClaudeProvider("img"))
		for _, ctx := range []context.Context{WithResultFreeze(t.Context(), ref, nil), WithResultFreeze(t.Context(), store.ExecutionRef{}, s)} {
			var err error
			if stream {
				_, err = h.r.RunStream(ctx, convChat, 7, TierRegistered, nil, "q")
			} else {
				_, err = h.r.Run(ctx, convChat, 7, TierRegistered, nil, "q")
			}
			if !errors.Is(err, ErrInvalidResultFreeze) || h.ensures != 0 {
				t.Fatalf("invalid binding executed: %v ensures=%d", err, h.ensures)
			}
		}
	}
}

func TestGroupFreezeFailureKeepsNoReplayWithoutDetails(t *testing.T) {
	err := groupPublicError(&ResultFreezeError{Err: errors.New("private detail")})
	var failure *ResultFreezeError
	if !errors.As(err, &failure) || !failure.NoReplay() || strings.Contains(err.Error(), "private detail") {
		t.Fatalf("unsafe group error: %v", err)
	}
}

type modelChangingExec struct {
	*fakeAgentHome
	r *Runner
}

func (e modelChangingExec) Exec(ctx context.Context, id string, cmd, env []string) (*ExecResult, error) {
	res, err := e.fakeAgentHome.Exec(ctx, id, cmd, env)
	if len(cmd) > 4 && cmd[0] == "sh" && strings.Contains(cmd[2], "echo $$ >") {
		e.r.SetChatModel(convChat, "next-model")
	}
	return res, err
}
func (e modelChangingExec) ExecStream(ctx context.Context, id string, cmd, env []string) (*StreamHandle, error) {
	res, err := e.fakeAgentHome.ExecStream(ctx, id, cmd, env)
	e.r.SetChatModel(convChat, "next-model")
	return res, err
}
func TestFrozenModelMatchesExecutedCommand(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "stream"}[stream], func(t *testing.T) {
			s, ref := freezeFixture(t)
			h := newConvHarness(t, NewClaudeProvider("img"))
			h.r.SetStore(s)
			h.r.SetChatModel(convChat, "selected-model")
			h.r.ex = modelChangingExec{h.home, h.r}
			ctx := WithResultFreeze(t.Context(), ref, s)
			if stream {
				st, err := h.r.RunStream(ctx, convChat, 7, TierRegistered, nil, "q")
				if err != nil {
					t.Fatal(err)
				}
				if res := readResult(t, st); res.Err != nil {
					t.Fatal(res.Err)
				}
			} else {
				if _, err := h.r.Run(ctx, convChat, 7, TierRegistered, nil, "q"); err != nil {
					t.Fatal(err)
				}
			}
			result, ok, err := s.TurnResultForAttempt(t.Context(), ref.Members[0].TurnID, 1)
			model, _ := flagValue(h.home.lastRun(), "--model")
			if err != nil || !ok || model != "selected-model" || result.Model != model || h.r.ModelForChat(convChat) != "next-model" {
				t.Fatalf("command=%q result=%q current=%q err=%v", model, result.Model, h.r.ModelForChat(convChat), err)
			}
		})
	}
}
