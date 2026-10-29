package control

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRegisterPreparesNewGroupOnceWithoutConfirmation(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := New(silentLog())
	var prepared []string
	RegisterChatCommandsWith(r, st, 100, func(_ context.Context, jid string) (string, error) {
		prepared = append(prepared, jid)
		return "ok", nil
	})
	resp, err := r.Dispatch(ctx, ownerCmd("-555", "team"))
	if err != nil || resp.Code != 0 || len(resp.Buttons) != 0 {
		t.Fatalf("register: %+v %v", resp, err)
	}
	if len(prepared) != 1 || prepared[0] != "tg:-555" || strings.Contains(resp.Text, "untime") {
		t.Fatalf("prepared=%v text=%q", prepared, resp.Text)
	}
	// Private chats are never prepared as groups.
	if _, err = r.Dispatch(ctx, ownerCmd("777", "alice")); err != nil || len(prepared) != 1 {
		t.Fatalf("private chat prepared: %v", prepared)
	}
}

func TestRegisterPrepareFailureKeepsRegistrationAndLeaksNothing(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := New(silentLog())
	RegisterChatCommandsWith(r, st, 100, func(context.Context, string) (string, error) {
		return "", errors.New("/private/path docker detail")
	})
	resp, err := r.Dispatch(ctx, ownerCmd("-556", "team2"))
	if err != nil || resp.Code != 0 {
		t.Fatalf("registration must succeed: %+v %v", resp, err)
	}
	if strings.Contains(resp.Text, "private") || strings.Contains(resp.Text, "untime") {
		t.Fatalf("text %q", resp.Text)
	}
	if _, err = st.GetRegistered(ctx, "tg:-556"); err != nil {
		t.Fatal("registration lost")
	}
}

func TestRegisterWithoutPreparerIsUnchanged(t *testing.T) {
	st := testStore(t)
	r := New(silentLog())
	RegisterChatCommands(r, st, 100)
	resp, err := r.Dispatch(context.Background(), ownerCmd("-557", "team3"))
	if err != nil || strings.Contains(resp.Text, "Group runtime") {
		t.Fatalf("%+v %v", resp, err)
	}
}
