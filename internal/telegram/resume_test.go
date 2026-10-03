package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
)

func TestResumePromptWrapsUserText(t *testing.T) {
	got := resumePrompt("deploy the thing")
	for _, want := range []string{"[gopod]", "interrupted by a restart", "deploy the thing", "Continue where you left off"} {
		if !strings.Contains(got, want) {
			t.Errorf("resumePrompt missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "[gopod]") {
		t.Error("resumePrompt must start with the [gopod] marker so the agent can tell it from user text")
	}
}

func TestAgentHandlerSkipsResumedWithReply(t *testing.T) {
	// A Bot with nil runner panics if the handler tries to run the
	// agent; a resumed item that already has a reply must return early.
	b := &Bot{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := b.NewAgentHandler()
	err := h(context.Background(), []queue.Item{{ID: 1, Resumed: true, ReplyMsgID: 77, Folder: "owner"}})
	if err != nil {
		t.Errorf("handler err = %v, want nil", err)
	}
}

func TestDoneHookIgnoresNonCrashLoop(t *testing.T) {
	// Without a Telegram API the hook must not touch the network for
	// ordinary completions; only ErrCrashLoop triggers a notice, and
	// that path needs b.api, so assert the early return by using a nil
	// api and a non-crash-loop error.
	b := &Bot{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hook := b.NewDoneHook()
	hook(queue.Item{ID: 1, ChatID: 5, MessageID: 6}, nil)
	hook(queue.Item{ID: 2, ChatID: 5, MessageID: 6}, errors.New("infra"))
	// Reaching here without a nil-pointer panic is the assertion.
}

func TestPickBatchItemCarriesResumeState(t *testing.T) {
	cases := []struct {
		name            string
		items           []queue.Item
		wantID          int64
		wantResumed     bool
		wantPlaceholder int
		wantReply       int
	}{
		{
			name:   "single item unchanged",
			items:  []queue.Item{{ID: 1, PlaceholderMsgID: 5}},
			wantID: 1, wantPlaceholder: 5,
		},
		{
			name: "earlier resumed item marks the pick resumed and lends its placeholder",
			items: []queue.Item{
				{ID: 1, Resumed: true, PlaceholderMsgID: 40},
				{ID: 2},
			},
			wantID: 2, wantResumed: true, wantPlaceholder: 40,
		},
		{
			name: "own placeholder wins over an earlier one",
			items: []queue.Item{
				{ID: 1, Resumed: true, PlaceholderMsgID: 40},
				{ID: 2, Resumed: true, PlaceholderMsgID: 41},
			},
			wantID: 2, wantResumed: true, wantPlaceholder: 41,
		},
		{
			name: "earliest non-zero placeholder is carried",
			items: []queue.Item{
				{ID: 1, Resumed: true},
				{ID: 2, Resumed: true, PlaceholderMsgID: 50},
				{ID: 3, Resumed: true, PlaceholderMsgID: 51},
				{ID: 4},
			},
			wantID: 4, wantResumed: true, wantPlaceholder: 50,
		},
		{
			name: "earlier item that already replied contributes nothing",
			items: []queue.Item{
				{ID: 1, Resumed: true, PlaceholderMsgID: 60, ReplyMsgID: 60},
				{ID: 2},
			},
			wantID: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickBatchItem(tc.items)
			if got.ID != tc.wantID || got.Resumed != tc.wantResumed ||
				got.PlaceholderMsgID != tc.wantPlaceholder || got.ReplyMsgID != tc.wantReply {
				t.Errorf("pickBatchItem = {ID:%d Resumed:%v Placeholder:%d Reply:%d}, want {ID:%d Resumed:%v Placeholder:%d Reply:%d}",
					got.ID, got.Resumed, got.PlaceholderMsgID, got.ReplyMsgID,
					tc.wantID, tc.wantResumed, tc.wantPlaceholder, tc.wantReply)
			}
		})
	}
}
