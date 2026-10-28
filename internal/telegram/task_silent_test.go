package telegram

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// A scheduled run has one delivery owner: the agent's IPC messages. Its stdout
// is never sent, and an agent failure never makes the queue rerun a finished
// execution just because nothing could be delivered.
func TestTaskTurnRunsSilentlyAndNeverSendsStdout(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		err   error
	}{
		{"stdout", "report text the agent printed", nil},
		{"agent error", "", errors.New("agent exploded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
				return http.StatusOK, `{"ok":true,"result":true}`
			}}
			b, item := newStreamFixture(t, fake)
			item.ChatID, item.Source = 0, "task"
			runs := 0
			b.taskRun = func(_ context.Context, folder string, _ int, tier runner.Tier, prompt string) (string, error) {
				runs++
				if folder != "owner" || tier != runner.TierRegistered || prompt != "hi" {
					t.Errorf("run args: %q %v %q", folder, tier, prompt)
				}
				return tc.reply, tc.err
			}
			for _, streaming := range []bool{true, false} {
				b.streamEnabled = streaming
				if err := b.NewAgentHandler()(context.Background(), []queue.Item{item}); err != nil {
					t.Fatalf("handler err = %v, want nil so the queue never reruns the turn", err)
				}
			}
			if runs != 2 {
				t.Fatalf("agent ran %d times, want once per handler call", runs)
			}
			if n := len(fake.calls); n != 0 {
				t.Fatalf("task turn made %d Telegram API calls: %+v", n, fake.calls)
			}
		})
	}
}

// Shutdown during a scheduled run is reported as an interruption, not as done,
// and still makes no Telegram call.
func TestTaskTurnCancellationIsInterruptedWithoutTelegramCalls(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
	b, item := newStreamFixture(t, fake)
	item.ChatID, item.Source = 0, "task"
	ctx, cancel := context.WithCancel(context.Background())
	b.taskRun = func(ctx context.Context, _ string, _ int, _ runner.Tier, _ string) (string, error) {
		cancel()
		return "partial stdout", ctx.Err()
	}
	err := b.NewAgentHandler()(ctx, []queue.Item{item})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := len(fake.calls); n != 0 {
		t.Fatalf("%d Telegram API calls", n)
	}
}
