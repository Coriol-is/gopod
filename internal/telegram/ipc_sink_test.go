package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/Coriol-is/gopod/internal/ipc"
)

func TestIPCSink_BadJIDIsPermanent(t *testing.T) {
	// A nil bot is fine — Send must reject bad JIDs before touching
	// the API.
	sink := IPCSink{bot: nil}
	err := sink.Send(context.Background(), "not-a-number", "x")
	if err == nil || !errors.Is(err, ipc.ErrPermanent) {
		t.Errorf("Send(bad jid): err = %v, want ipc.ErrPermanent", err)
	}
}
