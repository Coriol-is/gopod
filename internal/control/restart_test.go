package control

import (
	"context"
	"errors"
	"testing"
)

func TestRestartRouterPermissionAndIdentity(t *testing.T) {
	r := New(nil)
	calls := 0
	RegisterRestartCommand(r, func(context.Context, Caller) error { calls++; return nil })
	cmd := Command{Name: "restart", Caller: Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 1, MessageID: 10}}
	if _, err := r.Dispatch(t.Context(), cmd); !errors.Is(err, ErrNotAuthorized) {
		t.Fatal(err)
	}
	cmd.Caller.IsOwner = true
	for _, kind := range []string{"group", "supergroup", ""} {
		cmd.Caller.ChatKind = kind
		if resp, err := r.Dispatch(t.Context(), cmd); err != nil || resp.Code != 1 {
			t.Fatal(resp, err)
		}
	}
	cmd.Caller.ChatKind = "private"
	cmd.Caller.Source = SourceCLI
	if resp, _ := r.Dispatch(t.Context(), cmd); resp.Code != 1 {
		t.Fatal(resp)
	}
	if calls != 0 {
		t.Fatal("unauthorized restart")
	}
	cmd.Caller.Source = SourceTelegram
	if resp, err := r.Dispatch(t.Context(), cmd); err != nil || resp.Code != 0 || calls != 1 {
		t.Fatal(resp, err, calls)
	}
}

func TestForceRestartRequiresConfirmationAndRejectsUnknownArgs(t *testing.T) {
	r := New(nil)
	calls := 0
	RegisterRestartLifecycle(r, func(_ context.Context, _ Caller, action string) (string, error) {
		calls++
		if action != "force" {
			t.Fatal(action)
		}
		return "", nil
	})
	cmd := Command{Name: "restart", Args: []string{"force"}, Caller: Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 1, MessageID: 10, IsOwner: true}}
	resp, err := r.Dispatch(t.Context(), cmd)
	if err != nil || resp.Code != CodeConfirm || calls != 0 {
		t.Fatal(resp, err, calls)
	}
	cmd.Confirmed = true
	resp, err = r.Dispatch(t.Context(), cmd)
	if err != nil || resp.Code != 0 || calls != 1 {
		t.Fatal(resp, err, calls)
	}
	cmd.Args = []string{"force", "extra"}
	resp, _ = r.Dispatch(t.Context(), cmd)
	if resp.Code != 1 || calls != 1 {
		t.Fatal(resp, calls)
	}
}
