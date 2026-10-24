package telegram

// sendMessageDraft semantics, read from https://core.telegram.org/bots/api
// (Bot API 10.3, 2026-08-24) on 2026-10-24:
//
//   - How a draft becomes a real message: it never does. "The streamed
//     draft is ephemeral and acts as a temporary 30-second preview - once
//     the output is finalized, you must call sendMessage with the complete
//     message to persist it in the user's chat." The draft disappears when
//     the bot sends a message.
//   - Final message id: the id returned by that sendMessage. A draft has
//     no message id; the method returns only True. So the reply id D020
//     memoizes is the sendMessage result, and there is no placeholder id
//     to memoize while drafting.
//   - Lifetime: about 30 seconds after the last update. A turn that goes
//     quiet (long tool call) must re-send the draft before then.
//   - Identity: draft_id is a non-zero integer. Updates with the same id
//     animate; a different id replaces the draft without animation.
//   - Target: chat_id is "Unique identifier for the target private
//     chat". Groups are not supported, so group chats (negative ids)
//     never try drafts.
//   - Length: text is 0-4096 characters after entities parsing. Empty
//     text shows a "Thinking…" placeholder (allowed since Bot API 10.0).
//   - Parse mode: parse_mode and entities are accepted (HTML allowed).
//     gopod still streams partials as plain text, because a half-written
//     markdown reply converts to unbalanced HTML; only the final
//     sendMessage carries HTML.
//     gopod's opening draft still sends the "▍" cursor rather than empty
//     text, so it does not depend on that relaxation.
//   - If gopod dies mid-turn: nothing is left behind. The draft expires
//     on its own within ~30 seconds; there is no dangling placeholder to
//     clean up. A resumed turn (D020) sends a fresh final message.
//   - can_stop shows the user a stop button; pressing it delivers an
//     Update.stopped_message_generation (chat, message_thread_id,
//     draft_id). keep_on_stop keeps the draft visible after the press,
//     but it "will still disappear after a short time or if the bot sends
//     a message"; to keep the partial text the bot must send it as a new
//     message. Wired in stop.go and streamReply via draftOpts.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
)

// draftOpts are the optional sendMessageDraft flags.
type draftOpts struct {
	CanStop    bool
	KeepOnStop bool
}

// newDraftID returns a fresh draft id in (0, MaxInt32]. The sequence is
// seeded from the clock on first use, not from 1, so a Stop pressed on a
// draft from before a restart does not name a draft of the new process.
func (b *Bot) newDraftID() int {
	for {
		old := b.draftSeq.Load()
		base := old
		if base == 0 {
			base = draftSeed()
		}
		next := base + 1
		if next > math.MaxInt32 {
			next = 1
		}
		if b.draftSeq.CompareAndSwap(old, next) {
			return int(next)
		}
	}
}

// draftSeed is the clock in milliseconds folded into int32 range.
// Update.stopped_message_generation carries draft_id as an int.
func draftSeed() int64 {
	if s := time.Now().UnixMilli() & math.MaxInt32; s != 0 {
		return s
	}
	return 1
}

// draftsSupported reports whether chatID may use sendMessageDraft:
// private chats only, and not after the Bot API refused the method for
// this chat.
func (b *Bot) draftsSupported(chatID int64) bool {
	if chatID <= 0 || b.api == nil {
		return false
	}
	b.draftsMu.Lock()
	defer b.draftsMu.Unlock()
	return !b.draftsOff[chatID]
}

// markDraftsUnsupported turns drafts off for chatID until restart.
func (b *Bot) markDraftsUnsupported(chatID int64) {
	b.draftsMu.Lock()
	defer b.draftsMu.Unlock()
	if b.draftsOff == nil {
		b.draftsOff = make(map[int64]bool)
	}
	b.draftsOff[chatID] = true
}

// sendDraft streams text as a plain-text draft. threadID is 0 for the
// main conversation.
func (b *Bot) sendDraft(ctx context.Context, chatID int64, threadID int, draftID int, text string) error {
	return b.sendDraftOpts(ctx, chatID, threadID, draftID, text, draftOpts{})
}

// draftBadRequestLimit is how many consecutive unrecognized 400s on a
// chat turn drafts off for it, as a failsafe.
const draftBadRequestLimit = 3

// draftCapabilityHints are substrings of a 400 description that mean
// the Bot API does not offer drafts here, as opposed to a bad call.
var draftCapabilityHints = []string{"method", "not supported", "not available", "unknown", "private chat"}

// isDraftCapabilityError reports whether err says drafts are not
// available for the chat: a 404, or a 400 naming the method or chat type.
func isDraftCapabilityError(err error) bool {
	if errors.Is(err, bot.ErrorNotFound) {
		return true
	}
	if !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, h := range draftCapabilityHints {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

// sendDraftOpts is sendDraft with the stop-button flags. A capability
// error (see isDraftCapabilityError) marks drafts unsupported for the
// chat; any other 400 is a per-call failure, and draftBadRequestLimit
// of them in a row also mark it. Rate limits, 5xx and network errors
// are transient and leave the flag alone. A topic that no longer exists
// (threadgone.go) is none of these: it neither counts nor marks the
// chat, and later drafts into it are skipped.
func (b *Bot) sendDraftOpts(ctx context.Context, chatID int64, threadID int, draftID int, text string, opts draftOpts) error {
	if b.threadGone(chatID, threadID) {
		return errThreadGone
	}
	ok, err := b.api.SendMessageDraft(ctx, &bot.SendMessageDraftParams{
		ChatID:          chatID,
		MessageThreadID: threadID,
		DraftID:         strconv.Itoa(draftID),
		Text:            text,
		CanStop:         opts.CanStop,
		KeepOnStop:      opts.KeepOnStop,
	})
	if err != nil {
		switch {
		case b.checkThreadGone(chatID, threadID, err):
			return fmt.Errorf("sendMessageDraft: %w: %w", errThreadGone, err)
		case isDraftCapabilityError(err):
			b.markDraftsUnsupported(chatID)
			b.log.Info("sendMessageDraft refused, falling back to edits for this chat",
				slog.Int64("chat_id", chatID),
				slog.Any("err", err))
		case errors.Is(err, bot.ErrorBadRequest):
			if b.countDraftBadRequest(chatID) >= draftBadRequestLimit {
				b.markDraftsUnsupported(chatID)
				b.log.Info("sendMessageDraft kept failing, falling back to edits for this chat",
					slog.Int64("chat_id", chatID),
					slog.Any("err", err))
			}
		}
		return fmt.Errorf("sendMessageDraft: %w", err)
	}
	if !ok {
		return errors.New("sendMessageDraft: api returned false")
	}
	b.resetDraftBadRequests(chatID)
	return nil
}

// countDraftBadRequest records one more consecutive unrecognized 400
// for chatID and returns the run length.
func (b *Bot) countDraftBadRequest(chatID int64) int {
	b.draftsMu.Lock()
	defer b.draftsMu.Unlock()
	if b.draftBadReqs == nil {
		b.draftBadReqs = make(map[int64]int)
	}
	b.draftBadReqs[chatID]++
	return b.draftBadReqs[chatID]
}

// resetDraftBadRequests clears the 400 run after a successful draft.
func (b *Bot) resetDraftBadRequests(chatID int64) {
	b.draftsMu.Lock()
	defer b.draftsMu.Unlock()
	delete(b.draftBadReqs, chatID)
}
