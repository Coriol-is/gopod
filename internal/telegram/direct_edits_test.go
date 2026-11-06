package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

func TestDirectUnknownBlocksReplacementAndRestart(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	fake.mu.Lock()
	fake.respond = func(string, map[string]string) (int, string) { return apiError(500, "upstream lost") }
	fake.mu.Unlock()
	before := len(fake.callsTo("editMessageText"))
	err := b.editMessageHTMLKB(t.Context(), streamChat, 77, "<b>final</b>", replyKeyboard(id, "final"))
	if !noReplayError(err) || len(fake.callsTo("editMessageText")) != before+1 {
		t.Fatal(err, "unexpected fallback")
	}
	if b.finalEditLost(queue.Item{ChatID: streamChat, ThreadID: 7}, err) {
		t.Fatal("unknown chose replacement")
	}
	if err = b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := b.store.Path()
	b.store.Close()
	st, err := store.Open(t.Context(), path, b.log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b = newFakeBot(t, fake)
	b.store = st
	if err = b.editMessage(t.Context(), streamChat, 77, "later"); !noReplayError(err) {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before+1 {
		t.Fatal("restarted HTTP")
	}
}
func TestDirectHistoricalTargetCannotUseCacheAuthority(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	// No cache entry: this must exercise the unknown transport lane.
	before := len(fake.callsTo("editMessageText"))
	if err := b.editMessage(t.Context(), streamChat, 999, "unproven"); !noReplayError(err) {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("cache invented provenance")
	}
	b.topicMu.Lock()
	uncertain := len(b.topicUncertain) != 0
	b.topicMu.Unlock()
	if uncertain {
		t.Fatal("known unsent latched generic uncertainty")
	}
	if err := b.topicRequest(t.Context(), streamChat, 8, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestDirectTextAckFailureReconcilesWithoutHTTP(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_direct_ack BEFORE UPDATE OF state ON telegram_edit_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := b.editMessage(t.Context(), streamChat, 77, "final"); !noReplayError(err) {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if _, err := b.store.DB().Exec(`DROP TRIGGER fail_direct_ack`); err != nil {
		t.Fatal(err)
	}
	if err := b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("DB receipt replayed HTTP")
	}
	c, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err != nil || !c.Terminal || c.Acknowledged != c.Revision {
		t.Fatal(c, err)
	}
}
func TestDirectMarkupDoesNotAcknowledgeText(t *testing.T) {
	b, _, _ := recoveryFixture(t)
	if err := b.store.CheckpointControlMessage(t.Context(), streamChat, 77, "final", false, true); err != nil {
		t.Fatal(err)
	}
	release, err := b.lockMessageEdit(t.Context(), streamChat, 77)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = b.topicEditRequest(t.Context(), streamChat, 77, func(ctx context.Context) error {
		return b.directMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: int64(streamChat), MessageID: 77, ReplyMarkup: noKeyboard()}, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if c.Acknowledged == c.Revision {
		t.Fatal("markup claimed text ACK")
	}
}
func TestDirectSilentSummaryUnknownStopsFinalAndTTS(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fake")
	SetReplyMode(streamChat, "voice+text")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := editPathFake()
	b, item := newStreamFixture(t, fake)
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := b.store.SetState(t.Context(), control.SilentStateKey+item.Folder, "1"); err != nil {
		t.Fatal(err)
	}
	// Empty chunks force the silent final summary branch, without a timer race.
	fake.mu.Lock()
	inner := fake.respond
	fake.respond = func(method string, p map[string]string) (int, string) {
		if method == "editMessageText" {
			return apiError(500, "unknown edit")
		}
		return inner(method, p)
	}
	fake.mu.Unlock()
	paid := 0
	b.tts = func(context.Context, string, string) ([]byte, error) { paid++; return nil, nil }
	chunks := make(chan string)
	close(chunks)
	result := make(chan runner.RunResult, 1)
	result <- runner.RunResult{FullText: strings.Repeat("x", 5000)}
	err := b.streamReply(t.Context(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	if !noReplayError(err) || paid != 0 {
		t.Fatal(err, paid)
	}
	if len(fake.callsTo("sendMessage")) != 1 || len(fake.callsTo("sendVoice")) != 0 {
		t.Fatal("unknown summary sent replacement")
	}
}
func TestDirectVoicePlaceholderUnknownStopsPaidGeneration(t *testing.T) {
	voiceOnly(t)
	t.Setenv("OPENAI_API_KEY", "fake")
	fake := editPathFake()
	inner := fake.respond
	fake.respond = func(method string, p map[string]string) (int, string) {
		if method == "deleteMessage" {
			return apiError(400, "can't delete")
		}
		if method == "editMessageText" && p["text"] == "Finished." {
			return apiError(500, "unknown final")
		}
		return inner(method, p)
	}
	b, item := newStreamFixture(t, fake)
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	paid := 0
	b.tts = func(context.Context, string, string) ([]byte, error) { paid++; return []byte("audio"), nil }
	b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) { return "voice answer", nil }
	err := b.runAgentSync(t.Context(), item)
	if !noReplayError(err) || paid != 0 || len(fake.callsTo("sendVoice")) != 0 {
		t.Fatal(err, paid)
	}
}
func TestDirectExplicitParseFallbackHasDistinctAttempts(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	fake.mu.Lock()
	fake.respond = func(_ string, p map[string]string) (int, string) {
		if p["parse_mode"] == "HTML" {
			return apiError(400, "can't parse entities")
		}
		return sentMessage(p["message_id"])
	}
	fake.mu.Unlock()
	if err := b.editMessageHTMLKB(t.Context(), streamChat, 77, "<b>final</b>", replyKeyboard(id, "final")); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.store.DB().QueryRow(`SELECT count(*) FROM telegram_edit_attempts WHERE state='refused'`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
func TestDirectWrongReceiptAndNotModifiedAttribution(t *testing.T) {
	for _, mode := range []string{"wrong", "notmodified", "server"} {
		t.Run(mode, func(t *testing.T) {
			b, fake, _ := recoveryFixture(t)
			fake.mu.Lock()
			fake.respond = func(string, map[string]string) (int, string) {
				if mode == "wrong" {
					return sentMessage("78")
				}
				status := 400
				if mode == "server" {
					status = 500
				}
				return apiError(status, "message is not modified")
			}
			fake.mu.Unlock()
			err := b.editMessage(t.Context(), streamChat, 77, "final")
			if mode == "notmodified" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !noReplayError(err) {
				t.Fatal(err)
			}
		})
	}
}

func TestDirectCheckpointFailureStopsBeforeHTTP(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_control_checkpoint BEFORE UPDATE OF text ON telegram_control_messages BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if err := b.editMessage(t.Context(), streamChat, 77, "final"); !noReplayError(err) {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("HTTP followed failed checkpoint")
	}
}
