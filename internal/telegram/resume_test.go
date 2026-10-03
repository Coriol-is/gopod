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
