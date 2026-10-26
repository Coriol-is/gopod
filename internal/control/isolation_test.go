package control

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIsolationActionsRequireOwnerAndConfirmation(t *testing.T) {
	r := New(silentLog())
	calls := 0
	actions := IsolationActions{}
	mutate := func(_ context.Context, target string) (string, error) {
		calls++
		if target != "tg:-200" {
			t.Fatalf("wrong target %q", target)
		}
		return "done", nil
	}
	actions.Prepare, actions.Enable, actions.Disable = mutate, mutate, mutate
	actions.Status = func(_ context.Context, target string) (string, error) { calls++; return "status " + target, nil }
	RegisterIsolationCommands(r, actions)
	for _, op := range []string{"prepare", "enable", "disable"} {
		cmd := Command{Name: "isolation", Args: []string{op, "-200"}, Caller: Caller{Source: SourceTelegram, ChatID: 1, IsOwner: true}}
		before := calls
		response, err := r.Dispatch(t.Context(), cmd)
		if err != nil || response.Code != CodeConfirm || calls != before {
			t.Fatal("unconfirmed mutation")
		}
		cmd.Confirmed = true
		cmd.Caller.IsOwner = false
		if _, err = r.Dispatch(t.Context(), cmd); err == nil || calls != before {
			t.Fatal("nonowner mutation")
		}
		cmd.Caller.IsOwner = true
		response, err = r.Dispatch(t.Context(), cmd)
		if err != nil || response.Code != 0 || calls != before+1 {
			t.Fatal("confirmed operation failed")
		}
	}
	for _, args := range [][]string{nil, {"status"}, {"status", "tg:-200"}} {
		response, err := r.Dispatch(t.Context(), Command{Name: "isolation", Args: args, Caller: Caller{Source: SourceCLI, IsOwner: true}})
		if err != nil || response.Code != 0 {
			t.Fatal("status requires mutation confirmation")
		}
	}
}
func TestIsolationRejectsMalformedTargetsAndSafeErrorText(t *testing.T) {
	for _, args := range [][]string{{"enable"}, {"enable", "200"}, {"prepare", "owner"}, {"enable", "tg:-0200"}, {"enable", "-200", "extra"}, {"unknown", "-200"}} {
		r := New(silentLog())
		called := false
		RegisterIsolationCommands(r, IsolationActions{Enable: func(context.Context, string) (string, error) { called = true; return "", nil }})
		response, err := r.Dispatch(t.Context(), Command{Name: "isolation", Args: args, Confirmed: true, Caller: Caller{Source: SourceCLI, IsOwner: true}})
		if err != nil || response.Code == 0 || called {
			t.Fatalf("malformed %v dispatched", args)
		}
	}
	r := New(silentLog())
	RegisterIsolationCommands(r, IsolationActions{Prepare: func(context.Context, string) (string, error) { return "", errors.New("private-canary") }})
	response, _ := r.Dispatch(t.Context(), Command{Name: "isolation", Args: []string{"prepare", "-200"}, Confirmed: true, Caller: Caller{Source: SourceCLI, IsOwner: true}})
	if response.Code == 0 || strings.Contains(response.Text, "private-canary") {
		t.Fatal("private error leaked")
	}
}
