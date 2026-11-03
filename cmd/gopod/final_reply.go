package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Coriol-is/gopod/internal/store"
)

// recoverReadyFinalReplies runs before queue recovery/polling. It only publishes
// previously frozen plans: never render, read preferences, synthesize or run a
// model. Failed scope/artifact validation keeps the immutable result held.
func recoverReadyFinalReplies(ctx context.Context, st *store.Store, log *slog.Logger) error {
	after := ""
	for {
		ids, err := st.ReadyFinalReplies(ctx, after, 100)
		if err != nil {
			return fmt.Errorf("list final reply recovery: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			_, err := st.PublishReadyFinalReply(ctx, id)
			if err != nil {
				if errors.Is(err, store.ErrFinalReplyPublishedElsewhere) || errors.Is(err, store.ErrStaleScope) || errors.Is(err, store.ErrTopicDeleting) || errors.Is(err, store.ErrConversationRetired) || errors.Is(err, store.ErrPreparationExpired) || errors.Is(err, store.ErrPreparationState) || errors.Is(err, store.ErrInvalidArtifact) || errors.Is(err, store.ErrInvalidDelivery) {
					log.Warn("ready final reply remains held", slog.String("result", id), slog.Any("err", err))
				} else {
					return fmt.Errorf("recover final reply %s: %w", id, err)
				}
			}
			after = id
		}
	}
}
