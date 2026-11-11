package telegram

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

const topicDeleteCapacity = 64

// RequestTopicDeletion reserves capacity before persisting/enqueueing. Execution
// runs outside the command ingress lease; receipt storage is never dropped.
func (b *Bot) RequestTopicDeletion(ctx context.Context, scope store.ScopeRef, thread int) error {
	b.topicMu.Lock()
	for _, r := range b.topicDeleteReceipts {
		if r.intent.Scope == scope && r.intent.ThreadID == thread {
			b.topicMu.Unlock()
			return errors.New("topic settlement still pending")
		}
	}
	if b.topicDeleteAllocated >= topicDeleteCapacity {
		b.topicMu.Unlock()
		return errors.New("topic deletion capacity reached")
	}
	b.topicDeleteAllocated++
	b.topicMu.Unlock()
	d, created, err := b.store.BeginTopicDeletion(ctx, scope, thread)
	if err != nil || !created {
		b.topicMu.Lock()
		b.topicDeleteAllocated--
		b.topicMu.Unlock()
		if err != nil {
			return err
		}
		return store.ErrTopicDeletionState
	}
	b.topicMu.Lock()
	if b.topicDeleteReservations == nil {
		b.topicDeleteReservations = make(map[string]bool)
	}
	b.topicDeleteReservations[d.IntentID] = true
	b.topicMu.Unlock()
	select {
	case b.topicDeleteJobs <- d:
		return nil
	default:
		// No worker saw this intent. Keep failure settlement reserved if SQLite fails.
		r := topicDeleteReceipt{intent: d, outcome: "refused", unissued: true}
		b.retainTopicDeletionReceipt(r)
		return errors.Join(errors.New("topic deletion queue unavailable"), b.settleTopicDeletionReceipt(ctx, r))
	}
}

func (b *Bot) runTopicDeletions(ctx context.Context) {
	b.recoverDeletedTopics(ctx)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return // persisted jobs remain fenced, never replayed
		case <-tick.C:
			b.reconcileTopicDeletionReceipts(ctx)
			b.recoverDeletedTopics(ctx)
		case d := <-b.topicDeleteJobs:
			jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			err := b.deleteTopic(jobCtx, d)
			cancel()
			if err != nil {
				b.log.Warn("topic deletion held or refused", "thread", d.ThreadID, "err", err)
			}
		}
	}
}

// deleteTopic is only for a freshly queued intent. Claim dispatch atomically
// before the one remote call. A restored intent never enters this queue.
func (b *Bot) deleteTopic(ctx context.Context, d store.TopicDeletion) error {
	if observed, err := b.settleObservedTopic(ctx, d); observed {
		return err
	}
	target, err := topicTarget(d)
	if err != nil {
		return err
	}
	if err = b.QuiesceTopic(ctx, d); err != nil {
		r := topicDeleteReceipt{intent: d, outcome: "refused", unissued: true, notice: "Удаление не выполнено; работа темы требует сверки."}
		b.retainTopicDeletionReceipt(r)
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return errors.Join(err, b.settleTopicDeletionReceipt(settle, r))
	}
	if err = ctx.Err(); err != nil {
		return err
	} // no recovery dispatch authority
	if err = b.store.ClaimTopicDeletionDispatch(ctx, d); err != nil {
		if observed, settled := b.settleObservedTopic(ctx, d); observed {
			return settled
		}
		return err
	}
	ok, callErr := b.api.DeleteForumTopic(ctx, &bot.DeleteForumTopicParams{ChatID: target.chat, MessageThreadID: target.thread})
	r := topicDeleteReceipt{intent: d, outcome: "uncertain", notice: "Исход удаления неизвестен. Тема заблокирована до сверки; повторный запрос не отправляется."}
	if callErr == nil && ok {
		r.outcome = "deleted"
		r.notice = "Тема удалена, контекст сессий закрыт. Общие файлы и память сохранены."
	} else if callErr != nil && definiteTopicRefusal(callErr) {
		r.outcome = "refused"
		r.notice = "Telegram отклонил удаление. Контекст темы сохранён."
	}
	b.retainTopicDeletionReceipt(r)
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = b.settleTopicDeletionReceipt(settle, r); err != nil {
		return errors.Join(callErr, err)
	}
	if r.outcome == "uncertain" {
		return errors.Join(callErr, ErrTopicTransportUncertain)
	}
	return callErr
}

func (b *Bot) topicDeleteNotice(ctx context.Context, chat int64, thread int, text string) {
	// Worker context belongs to main, not the now-retired command topic. Fixed
	// status text discloses no conversation content. Ownership handles ambiguity.
	_, err := b.ownedTextSend(ctx, chat, 0, &bot.SendMessageParams{ChatID: chat, Text: fmt.Sprintf("Тема %d: %s", thread, text)})
	if err != nil {
		b.log.Warn("topic deletion notice failed", "thread", thread, "err", err)
	}
}

type topicDeleteReceipt struct {
	intent   store.TopicDeletion
	outcome  string
	unissued bool
	notice   string
}

func (b *Bot) retainTopicDeletionReceipt(r topicDeleteReceipt) {
	b.topicMu.Lock()
	defer b.topicMu.Unlock()
	if b.topicDeleteReceipts == nil {
		b.topicDeleteReceipts = make(map[string]topicDeleteReceipt)
	}
	// A late/preflight error may never overwrite an exact captured API result.
	if _, exists := b.topicDeleteReceipts[r.intent.IntentID]; !exists {
		b.topicDeleteReceipts[r.intent.IntentID] = r
	}
}

// Settlement is SQLite/runtime only until its single final notice attempt. A
// captured result never authorizes another deletion request. Taking the notice
// claim before HTTP also prevents concurrent settlement from sending it twice.
func (b *Bot) settleTopicDeletionReceipt(ctx context.Context, r topicDeleteReceipt) error {
	current, found, err := b.store.GetTopicDeletion(ctx, r.intent.Scope, r.intent.ThreadID)
	if err != nil {
		return err
	}
	if !found || current.IntentID != r.intent.IntentID {
		return store.ErrTopicDeletionState
	}
	// An exact external deletion observation supersedes an older refusal or
	// unknown receipt for this same intent. Preserve its existing fence identity
	// and settle locally rather than trying to reopen a now-deleted topic.
	if current.ObservationAttemptID != "" && (current.State == "deleted" || current.State == "retired") && r.outcome != "deleted" {
		r.outcome, r.unissued = "deleted", false
		r.notice = "Telegram сообщил об удалённой теме; контекст сессий закрыт."
	}
	if r.unissued {
		if current.State != "refused" {
			if err = b.store.RefuseUnissuedTopicDeletion(ctx, r.intent); err != nil {
				return err
			}
		}
	} else if err = b.store.RecordTopicDeletionOutcome(ctx, r.intent, r.outcome); err != nil {
		return err
	}
	switch r.outcome {
	case "deleted":
		if current.State != "retired" {
			if err = b.QuiesceTopic(ctx, r.intent); err != nil {
				return err
			}
			if err = b.store.RetireDeletedTopic(ctx, r.intent); err != nil {
				return err
			}
		}
	case "refused":
		if err = b.ReopenRefusedTopic(ctx, r.intent); err != nil {
			return err
		}
	}
	b.topicMu.Lock()
	_, send := b.topicDeleteReceipts[r.intent.IntentID]
	delete(b.topicDeleteReceipts, r.intent.IntentID)
	if b.topicDeleteReservations[r.intent.IntentID] {
		delete(b.topicDeleteReservations, r.intent.IntentID)
		b.topicDeleteAllocated--
	}
	b.topicMu.Unlock()
	if send && r.notice != "" {
		target, e := topicTarget(r.intent)
		if e != nil {
			return e
		}
		b.topicDeleteNotice(ctx, target.chat, target.thread, r.notice)
	}
	return nil
}
func (b *Bot) reconcileTopicDeletionReceipts(ctx context.Context) {
	b.topicMu.Lock()
	pending := make([]topicDeleteReceipt, 0, len(b.topicDeleteReceipts))
	for _, r := range b.topicDeleteReceipts {
		pending = append(pending, r)
	}
	b.topicMu.Unlock()

	sort.Slice(pending, func(i, j int) bool { return pending[i].intent.IntentID < pending[j].intent.IntentID })
	if len(pending) == 0 {
		return
	}
	pass, cancelPass := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPass()
	for range min(len(pending), 5) {
		if pass.Err() != nil {
			return
		}
		b.topicMu.Lock()
		r := pending[b.topicReceiptCursor%len(pending)]
		b.topicReceiptCursor++
		b.topicMu.Unlock()
		settle, cancel := context.WithTimeout(pass, time.Second)
		err := b.settleTopicDeletionReceipt(settle, r)
		cancel()
		if err != nil {
			b.log.Warn("topic deletion receipt settlement held", "thread", r.intent.ThreadID, "err", err)
		}
	}

}

// Confirmed remote success may resume local cleanup without replaying HTTP or a
// notice. The cursor advances even on a blocked intent; each pass is bounded.
func (b *Bot) recoverDeletedTopics(ctx context.Context) {
	if b.store == nil {
		return
	}
	intents, err := b.store.PendingTopicDeletions(ctx)
	if err != nil {
		b.log.Warn("topic cleanup recovery unavailable", "err", err)
		return
	}
	var deleted []store.TopicDeletion
	for _, d := range intents {
		if d.State == "deleted" {
			deleted = append(deleted, d)
		}
	}
	if len(deleted) == 0 {
		return
	}
	pass, cancelPass := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPass()
	count := min(len(deleted), 5)
	for range count {
		if pass.Err() != nil {
			return
		}
		d := deleted[b.topicRecoveryCursor%len(deleted)]
		b.topicRecoveryCursor++
		b.topicMu.Lock()
		_, hasReceipt := b.topicDeleteReceipts[d.IntentID]
		b.topicMu.Unlock()
		if hasReceipt {
			continue
		} // receipt settlement owns its promised notice
		recoverCtx, cancel := context.WithTimeout(pass, time.Second)
		err = b.QuiesceTopic(recoverCtx, d)
		if err == nil {
			err = b.store.RetireDeletedTopic(recoverCtx, d)
		}
		cancel()
		if err != nil {
			b.log.Warn("deleted topic local cleanup held", "thread", d.ThreadID, "err", err)
		}
	}
}

// An external observation can arrive while a confirmed command is still queued
// or joining transports. Such a job owns a capacity slot but no API receipt yet.
// Convert it into local settlement without acquiring any new dispatch authority.
func (b *Bot) settleObservedTopic(ctx context.Context, expected store.TopicDeletion) (bool, error) {
	current, found, err := b.store.GetTopicDeletion(ctx, expected.Scope, expected.ThreadID)
	if err != nil || !found || current.IntentID != expected.IntentID || current.ObservationAttemptID == "" || (current.State != "deleted" && current.State != "retired") {
		return false, err
	}
	r := topicDeleteReceipt{intent: expected, outcome: "deleted", notice: "Telegram сообщил об удалённой теме; контекст сессий закрыт."}
	b.retainTopicDeletionReceipt(r)
	return true, b.settleTopicDeletionReceipt(ctx, r)
}
