package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (b *Bot) bindControlMessage(ctx context.Context, it queue.Item, msg int) {
	b.inflightMu.Lock()
	t := b.inflight[it.ID]
	if t != nil {
		t.messages[msg] = true
	}
	var token string
	if t != nil {
		token = t.token
	}
	b.inflightMu.Unlock()
	if token == "" || b.store == nil {
		return
	}
	scope, err := b.store.SnapshotScope(ctx, it.Folder)
	if err == nil {
		b.inflightMu.Lock()
		if current := b.inflight[it.ID]; current == t {
			current.scope = scope
		}
		b.inflightMu.Unlock()
		err = b.store.BindControlMessage(ctx, store.ControlMessage{ChatID: it.ChatID, MessageID: msg, TurnID: it.ID, Token: token, Scope: scope, ThreadID: it.ThreadID})
	}
	if err != nil {
		b.log.Warn("persist control binding failed", "turn", it.ID, "error", err)
	}
}

func stopMarkup(m models.ReplyMarkup) bool {
	kb, ok := m.(*models.InlineKeyboardMarkup)
	if !ok || kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if strings.HasPrefix(button.CallbackData, "st:") {
				return true
			}
		}
	}
	return false
}

// checkpoint precedes the network edit: a failed final edit remains recoverable.
func (b *Bot) checkpointControl(ctx context.Context, chat int64, msg int, text string, html bool, kb models.ReplyMarkup) {
	if b.store == nil {
		return
	}
	terminal := !stopMarkup(kb)
	if c, e := b.store.GetControlMessage(ctx, chat, msg); e == nil && b.executionUncertain(ctx, c.Scope.Folder) {
		terminal = false
	}
	if err := b.store.CheckpointControlMessage(ctx, chat, msg, text, html, terminal); err != nil {
		b.log.Warn("control checkpoint failed", "error", err)
	}
}

func recoveredStatus(t store.Turn) string {
	if strings.Contains(t.Error, "execution_unknown") || strings.Contains(t.Error, "execution status unknown") {
		return "Connection lost. Execution status is unknown; reconciliation is required."
	}
	switch t.Status {
	case store.TurnPending:
		if t.Attempts == 0 {
			return "Queued; not started. Waiting to resume."
		}
	case store.TurnDone:
		if t.ReplyMsgID != 0 {
			return "Completed."
		}
		return "Work completed; delivery status needs verification."
	case store.TurnStopped:
		if strings.Contains(t.Error, "force") {
			return "Stopped by forced restart."
		}
		return "Stopped."
	case store.TurnFailed:
		return "Response interrupted or failed. Check execution state before continuing."
	}
	return "Connection lost. Execution status is unknown; reconciliation is required."
}

func (b *Bot) controlTurnStatus(ctx context.Context, id, chat int64, thread, msg int) string {
	if b.store == nil {
		return "Execution status is unavailable."
	}
	binding, e := b.store.GetControlMessage(ctx, chat, msg)
	if e != nil || binding.TurnID != id || binding.ThreadID != thread || b.store.ValidateScope(ctx, binding.Scope) != nil {
		return cbToastExpired
	}
	t, err := b.store.GetTurn(ctx, id)
	if err != nil || t.ChatID != chat || t.ThreadID != thread {
		return cbToastExpired
	}
	if b.executionUncertain(ctx, binding.Scope.Folder) {
		t.Error = "execution_unknown"
	}
	return recoveredStatus(t)
}

// RepairControls repairs inactive control messages, including terminal turns whose
// last edit failed. It never runs inference or assumes a lost process is dead.
// Call after startup execution recovery; Run also repeats it with bounded batches.
func (b *Bot) RepairControls(ctx context.Context) error {
	if b.store == nil || b.api == nil {
		return nil
	}
	rows, err := b.store.ControlMessages(ctx)
	if err != nil {
		return err
	}
	for _, c := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.RetryAt > time.Now().UnixMilli() {
			continue
		}
		// The lock covers only the live check and the lease claim; the network
		// edit runs outside it so streaming edits of other turns never stall
		// behind repair I/O. A turn that resumes meanwhile re-edits its own
		// placeholder on its next progress, and the revision CAS in
		// FinishControlRepair keeps a newer checkpoint authoritative.
		b.controlMu.Lock()
		b.inflightMu.Lock()
		live := b.inflight[c.TurnID] != nil
		b.inflightMu.Unlock()
		lease := b.store.FinishControlRepair(ctx, c, false, time.Now().Add(30*time.Second).UnixMilli())
		b.controlMu.Unlock()
		if lease != nil {
			b.log.Warn("control repair lease failed", "turn", c.TurnID, "error", lease)
			continue
		}
		if live {
			continue
		}
		err = b.repairControl(ctx, c)
		if err != nil {
			b.log.Warn("control repair pending", "turn", c.TurnID, "error", err)
		}
	}
	return nil
}

func (b *Bot) repairControl(ctx context.Context, c store.ControlMessage) error {
	if err := b.store.ValidateScope(ctx, c.Scope); err != nil {
		// Revoked scope: suppress edits rather than address a new registration.
		if errors.Is(err, store.ErrStaleScope) {
			return b.store.FinishControlRepair(ctx, c, true, 0)
		}
		return err
	}
	t, err := b.store.GetTurn(ctx, c.TurnID)
	if err != nil {
		return err
	}
	if t.ChatID != c.ChatID || t.ThreadID != c.ThreadID {
		return fmt.Errorf("control ownership mismatch")
	}
	uncertain := b.executionUncertain(ctx, c.Scope.Folder) || strings.Contains(t.Error, "execution_unknown") || strings.Contains(t.Error, "execution status unknown")
	if uncertain {
		c.Terminal = false
		t.Error = "execution_unknown"
	}
	c.Terminal = c.Terminal && (t.Status == store.TurnDone || t.Status == store.TurnStopped || t.Status == store.TurnFailed) && !uncertain
	text := c.Text
	if !c.Terminal {
		if c.HTML {
			text = htmlToPlain(text)
		}
		// Keep the bounded displayed partial and add a truthful status.
		text = strings.TrimSpace(text)
		if len(utf16.Encode([]rune(text))) > 3000 {
			units := 0
			var out []rune
			for _, r := range text {
				n := 1
				if r > 0xffff {
					n = 2
				}
				if units+n > 3000 {
					break
				}
				out = append(out, r)
				units += n
			}
			text = string(out) + "…"
		}
		text = strings.TrimSpace(text + "\n\n" + recoveredStatus(t))
	}
	if text == "" {
		text = recoveredStatus(t)
	}
	parse := models.ParseMode("")
	if c.Terminal && c.HTML {
		parse = models.ParseModeHTML
	}
	_, err = b.api.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: c.ChatID, MessageID: c.MessageID, Text: text, ParseMode: parse, ReplyMarkup: noKeyboard()})
	if err != nil && c.HTML && c.Terminal && strings.Contains(err.Error(), "parse entities") {
		_, err = b.api.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: c.ChatID, MessageID: c.MessageID, Text: htmlToPlain(text), ReplyMarkup: noKeyboard()})
	}
	success := err == nil || isNotModified(err)
	// A terminally unavailable message is no longer retried; no fallback send can
	// accidentally leak into main or duplicate an uncertain send.
	permanent := err != nil && (strings.Contains(err.Error(), "message to edit not found") || strings.Contains(err.Error(), "message can't be edited") || strings.Contains(err.Error(), "bot was blocked"))
	if permanent {
		b.log.Warn("control repair permanently unavailable", "turn", c.TurnID, "message", c.MessageID, "error", err)
	}
	retry := time.Now().Add(30 * time.Second)
	if delay, ok := retryAfter(err); ok {
		retry = time.Now().Add(delay)
	}
	// Unknown/queued statuses stay eligible: later execution evidence can change.
	done := (success && !uncertain && (c.Terminal || t.Status == store.TurnDone || t.Status == store.TurnStopped || t.Status == store.TurnFailed)) || permanent
	if e := b.store.FinishControlRepair(ctx, c, done, retry.UnixMilli()); e != nil {
		return e
	}
	return err
}

func (b *Bot) runControlRecovery(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		run, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := b.RepairControls(run); err != nil && ctx.Err() == nil {
			b.log.Warn("control recovery", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) executionUncertain(ctx context.Context, folder string) bool {
	if b.runner == nil {
		return false
	}
	if r, ok := any(b.runner).(interface {
		ExecutionUncertain(context.Context, string) bool
	}); ok {
		return r.ExecutionUncertain(ctx, folder)
	}
	return false
}

// A failed process kill must not be displayed as successful cancellation.
func (b *Bot) safeControlText(ctx context.Context, chat int64, msg int, text string, kb models.ReplyMarkup) string {
	if b.store != nil && !stopMarkup(kb) {
		if c, e := b.store.GetControlMessage(ctx, chat, msg); e == nil && b.executionUncertain(ctx, c.Scope.Folder) {
			return "Connection lost. Execution status is unknown; reconciliation is required."
		}
	}
	return text
}

// A terminal checkpoint fences later progress even if a stale goroutine still
// holds its old keyboard. Scope validation also fences re-registration.
func (b *Bot) controlEditAllowed(ctx context.Context, chat int64, msg int, kb models.ReplyMarkup) bool {
	if b.store == nil {
		return true
	}
	c, err := b.store.GetControlMessage(ctx, chat, msg)
	if err != nil {
		return errors.Is(err, sql.ErrNoRows)
	} // ordinary non-control messages have no binding
	if b.store.ValidateScope(ctx, c.Scope) != nil {
		return false
	}
	return !(c.Terminal && stopMarkup(kb))
}
