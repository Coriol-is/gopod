package control

import (
	"errors"
	"testing"
)

func TestOutboxOwnerAndConfirmation(t *testing.T) {
	r := New(nil)
	RegisterOutboxCommands(r, nil)
	for _, caller := range []Caller{{}, {ChatID: -100, IsGroupOwner: true}} {
		_, err := r.Dispatch(t.Context(), Command{Name: "outbox", Args: []string{"settle", "part", "unsent"}, Caller: caller, Confirmed: true})
		if !errors.Is(err, ErrNotAuthorized) {
			t.Fatalf("unauthorized: %v", err)
		}
	}
	for _, evidence := range []string{"unsent", "sent:42"} {
		resp, err := r.Dispatch(t.Context(), Command{Name: "outbox", Args: []string{"settle", "part", evidence}, Caller: Caller{IsOwner: true}})
		if err != nil || resp.Code != CodeConfirm {
			t.Fatalf("%+v %v", resp, err)
		}
	}
	for _, evidence := range []string{"sent:0", "sent:-1", "sent:no", "maybe"} {
		resp, err := r.Dispatch(t.Context(), Command{Name: "outbox", Args: []string{"settle", "part", evidence}, Caller: Caller{IsOwner: true}, Confirmed: true})
		if err != nil || resp.Code != 1 {
			t.Fatalf("%+v %v", resp, err)
		}
	}
}
