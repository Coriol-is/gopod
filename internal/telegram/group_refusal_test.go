package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot/models"
)

const refusalLeak = "claude: stderr: open /home/node/.claude/.credentials.json token=sk-ant-SECRET http://gopod-relay:8787/mcp/abc account=owner@example.org"

func groupItem() queue.Item {
	return queue.Item{ID: 41, ChatID: handoffGroup, MessageID: 10, Folder: "chat-3850302017", Source: "telegram"}
}

func sendsTo(fake *fakeTelegram, chat int64) []apiCall {
	var out []apiCall
	for _, c := range fake.callsTo("sendMessage") {
		if c.Form["chat_id"] == fmt.Sprint(chat) {
			out = append(out, c)
		}
	}
	return out
}

func TestGroupRefusalIsNeutralRateLimitedAndOwnerGetsReason(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	b.groupReason = func(_ context.Context, jid string) string { return "disabled by owner for " + jid }
	now := time.Unix(10000, 0)
	fakeClock(b, &now)
	refusals := []error{
		fmt.Errorf("%s: %w", refusalLeak, runner.ErrProfileUnavailable),
		fmt.Errorf("%s: %w", refusalLeak, &runner.UsageLimitError{Provider: "claude"}),
		fmt.Errorf("%s: %w", refusalLeak, runner.ErrSessionExpired),
		fmt.Errorf("%s: %w", refusalLeak, runner.ErrNotLoggedIn),
	}
	for _, err := range refusals {
		b.replyError(t.Context(), groupItem(), err)
	}
	group := sendsTo(fake, handoffGroup)
	if len(group) != 1 || group[0].Form["text"] != errTextGroupUnavailable || group[0].Form["reply_markup"] != "" {
		t.Fatalf("group notice not one neutral line: %+v", group)
	}
	for _, bad := range []string{"claude", "stderr", "/home", "token", "SECRET", "gopod-relay", "owner@", "limit", "login", "Войти"} {
		if strings.Contains(strings.ToLower(group[0].Form["text"]), strings.ToLower(bad)) {
			t.Fatalf("group text leaks %q", bad)
		}
	}
	owner := sendsTo(fake, streamChat)
	if len(owner) != 1 || !strings.Contains(owner[0].Form["text"], "disabled by owner for tg:-") || !strings.Contains(owner[0].Form["text"], "/isolation status") {
		t.Fatalf("owner notice: %+v", owner)
	}
	for _, bad := range []string{"stderr", "/home", "SECRET", "gopod-relay", "owner@"} {
		if strings.Contains(owner[0].Form["text"], bad) {
			t.Fatalf("owner notice leaks %q", bad)
		}
	}
	now = now.Add(groupNoticeCooldown + time.Second)
	b.replyError(t.Context(), groupItem(), runner.ErrProfileUnavailable)
	if len(sendsTo(fake, handoffGroup)) != 2 || len(sendsTo(fake, streamChat)) != 1 {
		t.Fatal("group and owner cooldowns must be independent")
	}
	now = now.Add(ownerNoticeCooldown)
	b.replyError(t.Context(), groupItem(), runner.ErrProfileUnavailable)
	if len(sendsTo(fake, streamChat)) != 2 {
		t.Fatal("owner notice not resent after cooldown")
	}
}

func TestGroupNonRefusalErrorKeepsOrdinaryReply(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	b.replyError(t.Context(), groupItem(), errors.New("exit status 1"))
	group := sendsTo(fake, handoffGroup)
	if len(group) != 1 || group[0].Form["text"] != errTextAgentFailed || len(sendsTo(fake, streamChat)) != 0 {
		t.Fatalf("unexpected: %+v", group)
	}
}

// A refusal in the group must not start a login; the owner's group /login
// only redirects privately (no prompt, no CLI).
func TestGroupRefusalStartsNoLogin(t *testing.T) {
	b, fake := handoffFixture(t)
	b.groupReason = func(context.Context, string) string { return "" }
	started := make(chan struct{}, 4)
	b.loginRun = func(context.Context, *models.Message, int, store.RegisteredChat) bool {
		started <- struct{}{}
		return false
	}
	b.replyError(t.Context(), groupItem(), runner.ErrNotLoggedIn)
	b.replyError(t.Context(), groupItem(), runner.ErrSessionExpired)
	for _, c := range sendsTo(fake, handoffGroup) {
		if c.Form["reply_markup"] != "" {
			t.Fatal("group refusal offered buttons")
		}
	}
	b.loginHandler(t.Context(), b.api, &models.Update{Message: ownerGroupLogin()})
	if len(started) != 0 {
		t.Fatal("group login started a CLI")
	}
	private := sendsTo(fake, streamChat)
	if len(private) == 0 || strings.Contains(private[len(private)-1].Form["text"], "http") {
		t.Fatal("owner redirect missing or leaks a URL")
	}
}
