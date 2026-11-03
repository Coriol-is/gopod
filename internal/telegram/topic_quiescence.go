package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

var ErrTopicRestartReconciliation = errors.New("topic intent predates this process; remote outcome requires reconciliation")

type topicFence struct {
	intent                            store.TopicDeletion
	ingress, transport, native, queue uint64
	restored                          bool
}

func topicTarget(d store.TopicDeletion) (chatThread, error) {
	chat, err := strconv.ParseInt(strings.TrimPrefix(d.Scope.JID, "tg:"), 10, 64)
	if err != nil || chat <= 0 || d.ThreadID <= 0 || d.Scope.JID != buildChatJID(chat) {
		return chatThread{}, store.ErrInvalidConversation
	}
	return chatThread{chat, d.ThreadID}, nil
}
func (b *Bot) installTopicFence(d store.TopicDeletion, restored bool) (topicFence, error) {
	key, err := topicTarget(d)
	if err != nil {
		return topicFence{}, err
	}
	b.topicMu.Lock()
	old, exists := b.topicFences[key]
	b.topicMu.Unlock()
	if exists {
		if old.intent.IntentID != d.IntentID || old.intent.Scope != d.Scope {
			return old, store.ErrTopicDeletionState
		}
		return old, nil
	}
	f := topicFence{intent: d, restored: restored}
	// Close admission first. Transport is sealed, not canceled: requests already
	// issued retain their actual result and can settle their outbox attempts.
	f.ingress = b.topicIngress.Block(d.Scope.JID, d.ThreadID)
	b.topicTransport.Seal(strconv.FormatInt(key.chat, 10), -1)
	f.transport = b.topicTransport.Seal(strconv.FormatInt(key.chat, 10), d.ThreadID)
	if b.runner != nil {
		f.native = b.runner.BeginConversationQuiescence(d.Scope.Folder, d.ThreadID)
	}
	if b.queue != nil {
		f.queue = b.queue.BeginConversationQuiescence(d.Scope.Folder, d.ThreadID)
	}
	b.topicMu.Lock()
	if b.topicFences == nil {
		b.topicFences = make(map[chatThread]topicFence)
	}
	b.topicFences[key] = f
	b.topicMu.Unlock()
	return f, nil
}

// RestoreTopicFences runs before queue recovery and polling. An old intent is a
// durable hold, never proof that pre-crash HTTP/native work did not happen. It
// restores admission without replaying Telegram deletion or marking it complete.
func (b *Bot) RestoreTopicFences(ctx context.Context) error {
	b.topicQuiesceMu.Lock()
	defer b.topicQuiesceMu.Unlock()
	if b.store == nil {
		return errors.New("topic store unavailable")
	}
	intents, err := b.store.PendingTopicDeletions(ctx)
	if err != nil {
		return err
	}
	for _, d := range intents {
		if _, err := b.installTopicFence(d, true); err != nil {
			return err
		}
	}
	return nil
}

// QuiesceTopic joins one previously persisted/confirmed deletion intent. The
// caller must be outside an ingress/callback lease for that topic. This function
// does not call Telegram deletion, erase context, or convert uncertainty to proof.
func (b *Bot) QuiesceTopic(ctx context.Context, expected store.TopicDeletion) error {
	b.topicQuiesceMu.Lock()
	defer b.topicQuiesceMu.Unlock()
	if b.store == nil {
		return errors.New("topic store unavailable")
	}
	current, found, err := b.store.GetTopicDeletion(ctx, expected.Scope, expected.ThreadID)
	if err != nil {
		return err
	}
	if !found || current.IntentID != expected.IntentID || current.State == "refused" || current.State == "retired" {
		return store.ErrTopicDeletionState
	}
	f, err := b.installTopicFence(current, false)
	if err != nil {
		return err
	}
	key, _ := topicTarget(current)
	if err = b.topicIngress.Wait(ctx, current.Scope.JID, current.ThreadID); err != nil {
		return err
	}
	// The queue handler can return early while a native stream drains. Join the
	// native lease and uncertainty evidence before terminal queue settlement.
	if b.runner != nil {
		if err = b.runner.WaitConversationQuiescent(ctx, current.Scope.Folder, current.ThreadID); err != nil {
			return err
		}
	}
	if b.queue != nil {
		if err = b.queue.WaitConversationQuiescent(ctx, current.Scope.Folder, current.ThreadID); err != nil {
			return err
		}
	}
	if err = b.topicTransport.Wait(ctx, strconv.FormatInt(key.chat, 10), key.thread); err != nil {
		return err
	}
	if err = b.topicTransport.Wait(ctx, strconv.FormatInt(key.chat, 10), -1); err != nil {
		return err
	}
	b.topicMu.Lock()
	uncertain := b.topicUncertain[key] || b.topicUncertain[chatThread{key.chat, -1}]
	b.topicMu.Unlock()
	if uncertain {
		return ErrTopicTransportUncertain
	}
	if err = b.store.TopicDeletionWorkIdle(ctx, current); err != nil {
		return err
	}
	if err = b.store.TopicDeletionTransportIdle(ctx, current); err != nil {
		return err
	}
	if f.restored {
		return ErrTopicRestartReconciliation
	}
	// Catch registration changes while cancellation/join was in progress.
	check, found, err := b.store.GetTopicDeletion(ctx, expected.Scope, expected.ThreadID)
	if err != nil {
		return err
	}
	if !found || check.IntentID != expected.IntentID || check.State == "refused" {
		return store.ErrTopicDeletionState
	}
	return nil
}

// ReopenRefusedTopic releases only this intent's generations after a durable
// definite refusal. A newer Block cannot be reopened by a stale waiter.
func (b *Bot) ReopenRefusedTopic(ctx context.Context, expected store.TopicDeletion) error {
	b.topicQuiesceMu.Lock()
	defer b.topicQuiesceMu.Unlock()
	current, found, err := b.store.GetTopicDeletion(ctx, expected.Scope, expected.ThreadID)
	if err != nil {
		return err
	}
	if !found || current.IntentID != expected.IntentID || current.State != "refused" {
		return store.ErrTopicDeletionState
	}
	key, err := topicTarget(current)
	if err != nil {
		return err
	}
	b.topicMu.Lock()
	f, ok := b.topicFences[key]
	b.topicMu.Unlock()
	if !ok {
		return nil
	}
	if f.intent.IntentID != current.IntentID {
		return store.ErrTopicDeletionState
	}
	if err = b.topicIngress.Wait(ctx, current.Scope.JID, key.thread); err != nil {
		return err
	}
	if b.runner != nil {
		if err = b.runner.WaitConversationQuiescent(ctx, current.Scope.Folder, key.thread); err != nil {
			return err
		}
	}
	if b.queue != nil {
		if err = b.queue.WaitConversationQuiescent(ctx, current.Scope.Folder, key.thread); err != nil {
			return err
		}
	}
	if err = b.topicTransport.Wait(ctx, strconv.FormatInt(key.chat, 10), key.thread); err != nil {
		return err
	}
	release := func() error {
		return b.store.WithRefusedTopicFence(ctx, current, func() error {
			if b.runner != nil {
				if err = b.runner.ReopenConversation(current.Scope.Folder, key.thread, f.native); err != nil {
					return err
				}
			}

			if err = b.topicTransport.Reopen(strconv.FormatInt(key.chat, 10), key.thread, f.transport); err != nil {
				return err
			}
			if err = b.topicIngress.Reopen(current.Scope.JID, key.thread, f.ingress); err != nil {
				return err
			}
			b.topicMu.Lock()
			delete(b.topicFences, key)
			b.topicMu.Unlock()
			return nil
		})
	}
	if b.queue != nil {
		return b.queue.ReopenConversationAfter(current.Scope.Folder, key.thread, f.queue, release)
	}
	return release()
}

// TopicFenceSummary reports blockers without exposing conversation content.
func (b *Bot) TopicFenceSummary() []string {
	b.topicMu.Lock()
	defer b.topicMu.Unlock()
	var out []string
	for key, f := range b.topicFences {
		out = append(out, fmt.Sprintf("thread %d: %s (restart_reconciliation=%t)", key.thread, f.intent.State, f.restored))
	}
	return out
}
