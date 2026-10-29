package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

type kickingGroupRuntime struct {
	allowedGroupRuntime
	deny   bool
	kicked []string
}

func (k *kickingGroupRuntime) Authorize(context.Context, store.RegisteredChat) error {
	if k.deny {
		return errors.New("denied")
	}
	return nil
}
func (k *kickingGroupRuntime) KickPrepare(c store.RegisteredChat) { k.kicked = append(k.kicked, c.JID) }

func TestDeniedUnreadyGroupKicksPreparationWithoutAdmitting(t *testing.T) {
	rt := &kickingGroupRuntime{deny: true}
	r := &Runner{}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:-2", Folder: convChat}}})
	r.SetGroupRuntime(rt)
	// IPC polling and other host checks never start preparation.
	for i := 0; i < 5; i++ {
		if err := r.CheckAdmission(context.Background(), convChat); !errors.Is(err, ErrProfileUnavailable) {
			t.Fatalf("denied group admitted: %v", err)
		}
	}
	if len(rt.kicked) != 0 {
		t.Fatalf("host check kicked preparation: %v", rt.kicked)
	}
	if _, err := r.Run(context.Background(), convChat, 0, TierRegistered, nil, "hello"); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("denied turn: %v", err)
	}
	if len(rt.kicked) != 1 || rt.kicked[0] != "tg:-2" {
		t.Fatalf("kicked %v", rt.kicked)
	}
	// A ready group that is denied (disabled, cleanup unknown) is never prepared again.
	ready := &kickingGroupRuntime{deny: true}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:-2", Folder: convChat, GroupEpochReady: true}}})
	r.SetGroupRuntime(ready)
	_, _ = r.Run(context.Background(), convChat, 0, TierRegistered, nil, "hello")
	if len(ready.kicked) != 0 {
		t.Fatal("ready group was kicked")
	}
}
