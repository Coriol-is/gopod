package runner

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

// Exercise the actual runner completion seam with the actual store retirement
// fence. The synthetic executor finishes before the freezer introduces a topic
// lifecycle change; no Telegram, Docker or native provider is contacted.
func TestRunnerFreezeHonorsTopicDeletionFence(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, state := range []string{"deleting", "uncertain", "retired", "refused"} {
			t.Run(fmt.Sprintf("%s/stream=%v", state, stream), func(t *testing.T) {
				s, ref := freezeFixture(t)
				h := newConvHarness(t, NewClaudeProvider("img"))
				h.r.SetStore(s)
				ctx := WithResultFreeze(t.Context(), ref, freezeFunc(func(ctx context.Context, got store.ExecutionRef, text string) (store.TurnResult, error) {
					d, created, err := s.BeginTopicDeletion(ctx, got.Conversation.Scope, got.Conversation.ThreadID)
					if err != nil {
						return store.TurnResult{}, err
					}
					if !created {
						return store.TurnResult{}, errors.New("test: intent already exists")
					}
					if state != "deleting" {
						outcome := state
						if state == "retired" {
							outcome = "deleted"
						}
						if err = s.RecordTopicDeletionOutcome(ctx, d, outcome); err != nil {
							return store.TurnResult{}, err
						}
					}
					if state == "retired" {
						// Native execution has finished at the completion seam. Model the
						// future orchestrator's quiesced settlement before local cleanup.
						for _, m := range got.Members {
							if err = s.MarkTurnFinished(ctx, m.TurnID, store.TurnStopped, ""); err != nil {
								return store.TurnResult{}, err
							}
						}
						if err = s.RetireDeletedTopic(ctx, d); err != nil {
							return store.TurnResult{}, err
						}
					}
					return s.FreezeTurnResult(ctx, got, text)
				}))
				var runErr error
				if stream {
					st, err := h.r.RunStream(ctx, convChat, 7, TierRegistered, nil, "question")
					runErr = err
					if err == nil {
						runErr = readResult(t, st).Err
					}
				} else {
					_, runErr = h.r.Run(ctx, convChat, 7, TierRegistered, nil, "question")
				}
				if state == "refused" {
					if runErr != nil {
						t.Fatalf("definite refusal prevented completion: %v", runErr)
					}
				} else {
					want := store.ErrTopicDeleting
					if state == "retired" {
						want = store.ErrConversationRetired
					}
					var frozen *ResultFreezeError
					if !errors.Is(runErr, want) || !errors.As(runErr, &frozen) || !frozen.NoReplay() {
						t.Fatalf("missing nonreplayable fence: %v", runErr)
					}
				}
				for _, m := range ref.Members {
					_, found, err := s.TurnResultForAttempt(t.Context(), m.TurnID, m.Attempt)
					if err != nil || found != (state == "refused") {
						t.Fatalf("partial result for %+v: found=%v err=%v", m, found, err)
					}
				}
				// The same runner/store still completes and freezes another topic.
				next := ref
				next.Conversation.ThreadID = 8
				id, _, err := s.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: "other-topic", ChatFolder: convChat, ThreadID: 8, Text: "other"})
				if err != nil {
					t.Fatal(err)
				}
				if err = s.MarkTurnRunning(t.Context(), id); err != nil {
					t.Fatal(err)
				}
				next.Members = []store.ResultMember{{TurnID: id, Attempt: 1}}
				if _, err = h.r.Run(WithResultFreeze(t.Context(), next, s), convChat, 8, TierRegistered, nil, "other"); err != nil {
					t.Fatalf("other topic blocked: %v", err)
				}
				if _, found, err := s.TurnResultForAttempt(t.Context(), id, 1); err != nil || !found {
					t.Fatalf("other topic freeze: %v %v", found, err)
				}
			})
		}
	}
}
