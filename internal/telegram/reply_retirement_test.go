package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestReplyRetirementFixedWireHash(t *testing.T) {
	p := &bot.EditMessageReplyMarkupParams{ChatID: int64(42), MessageID: 99, ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != store.ReplyRetirementHash(42, 99) {
		t.Fatalf("wire shape changed: %s", raw)
	}
}
func TestReplyRetirementOneEmptyMarkupAttempt(t *testing.T) {
	for _, outcome := range []string{"ack", "unknown", "refused"} {
		t.Run(outcome, func(t *testing.T) {
			fake := seqFake()
			b, item := newStreamFixture(t, fake)
			if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(item.ChatID), Folder: item.Folder, IsOwner: true, AddedAt: 1}); err != nil {
				t.Fatal(err)
			}
			conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(item.ChatID), 0, store.VisibilityConversation)
			if err != nil {
				t.Fatal(err)
			}
			add := func(source string) int {
				id, _, e := b.store.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: source, ChatID: item.ChatID, ChatFolder: item.Folder, Text: "answer"})
				if e != nil {
					t.Fatal(e)
				}
				if e = b.store.ReserveLegacyReplySurface(t.Context(), conv, id); e != nil {
					t.Fatal(e)
				}
				msg, e := b.ownedTextSend(t.Context(), item.ChatID, 0, &bot.SendMessageParams{ChatID: item.ChatID, Text: "answer"})
				if e != nil {
					t.Fatal(e)
				}
				if e = b.store.SetTurnReply(t.Context(), id, msg.ID); e != nil {
					t.Fatal(e)
				}
				if e = b.store.RecordLegacyReplySurface(t.Context(), id, msg.ID); e != nil {
					t.Fatal(e)
				}
				return msg.ID
			}
			first := add("cleanup-first")
			add("cleanup-next")
			rows, err := b.store.ReplyRetirementCandidates(t.Context(), item.ChatID, 0)
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			fake.mu.Lock()
			original := fake.respond
			fake.respond = func(method string, form map[string]string) (int, string) {
				if method == "editMessageReplyMarkup" && outcome != "ack" {
					status := 500
					if outcome == "refused" {
						status = 400
					}
					return apiError(status, "cannot edit")
				}
				return original(method, form)
			}
			fake.mu.Unlock()
			b.cleanupReplyKeyboards(t.Context())
			// Reset all scheduler progress as after restart: the durable journal is
			// the only reason a second dispatch must not happen.
			b.replyCleanupAfter.Store(0)
			b.cleanupReplyKeyboards(t.Context())
			b.clearKeyboard(t.Context(), item.ChatID, first)
			calls := fake.callsTo("editMessageReplyMarkup")
			if len(calls) != 1 || calls[0].Form["reply_markup"] != `{"inline_keyboard":[]}` {
				t.Fatal(first, calls)
			}
			rows, err = b.store.ReplyRetirementCandidates(t.Context(), item.ChatID, 0)
			if err != nil || len(rows) != 0 {
				t.Fatal(rows, err)
			}
		})
	}
}
