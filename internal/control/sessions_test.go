package control

import (
	"context"
	"errors"
	"testing"
)

func sessionsCaller(owner bool) Caller {
	return Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 1, MessageID: 10, IsOwner: owner}
}

func sessionsDeps(mode string, requested bool, requests *int) SessionsDeps {
	return SessionsDeps{
		Mode:    func(context.Context) (string, bool, error) { return mode, requested, nil },
		Request: func(context.Context) error { *requests++; return nil },
		Count:   func(context.Context) (int, int, error) { return 3, 2, nil },
	}
}

func TestSessionsStatusShowsModeAndCounts(t *testing.T) {
	r := New(nil)
	n := 0
	RegisterSessionsCommand(r, sessionsDeps("legacy", false, &n))
	want := "Session mapping: legacy. Adoption requested: no. Durable sessions: 3, native contexts: 2."
	for _, args := range [][]string{nil, {"status"}} {
		resp, err := r.Dispatch(t.Context(), Command{Name: "sessions", Args: args, Caller: sessionsCaller(true)})
		if err != nil || resp.Code != 0 || resp.Text != want {
			t.Fatalf("args %v: %+v, %v", args, resp, err)
		}
	}
	resp, _ := r.Dispatch(t.Context(), Command{Name: "sessions", Caller: sessionsCaller(true)})
	if len(resp.Buttons) != 1 || len(resp.Buttons[0]) != 1 || resp.Buttons[0][0].Label != "Adopt" ||
		resp.Buttons[0][0].Command != "sessions" || len(resp.Buttons[0][0].Args) != 1 || resp.Buttons[0][0].Args[0] != "adopt" {
		t.Fatalf("bare picker buttons: %+v", resp.Buttons)
	}
	resp, _ = r.Dispatch(t.Context(), Command{Name: "sessions", Args: []string{"status"}, Caller: sessionsCaller(true)})
	if len(resp.Buttons) != 0 {
		t.Fatalf("status must not show picker: %+v", resp.Buttons)
	}
	r2 := New(nil)
	RegisterSessionsCommand(r2, sessionsDeps("durable", true, &n))
	resp, _ = r2.Dispatch(t.Context(), Command{Name: "sessions", Caller: sessionsCaller(true)})
	if len(resp.Buttons) != 0 || resp.Text != "Session mapping: durable. Adoption requested: yes. Durable sessions: 3, native contexts: 2." {
		t.Fatalf("%+v", resp)
	}
}

func TestSessionsAdoptAsksForConfirmation(t *testing.T) {
	r := New(nil)
	n := 0
	RegisterSessionsCommand(r, sessionsDeps("legacy", false, &n))
	resp, err := r.Dispatch(t.Context(), Command{Name: "sessions", Args: []string{"adopt"}, Caller: sessionsCaller(true)})
	if err != nil || resp.Code != CodeConfirm || n != 0 {
		t.Fatal(resp, err, n)
	}
	yes := resp.Buttons[0][0]
	if !yes.Confirmed || yes.Command != "sessions" || len(yes.Args) != 1 || yes.Args[0] != "adopt" {
		t.Fatalf("yes button: %+v", yes)
	}
	if resp.Text != "Adopt legacy sessions into durable identity on the next restart? The current mappings are copied once; after that the old table is no longer read. Requires a restart to take effect." {
		t.Fatal(resp.Text)
	}
}

func TestSessionsAdoptRequestsOnConfirm(t *testing.T) {
	r := New(nil)
	n := 0
	RegisterSessionsCommand(r, sessionsDeps("legacy", false, &n))
	resp, err := r.Dispatch(t.Context(), Command{Name: "sessions", Args: []string{"adopt"}, Confirmed: true, Caller: sessionsCaller(true)})
	if err != nil || resp.Code != 0 || n != 1 || resp.Text != "Adoption requested. Run /restart to apply." {
		t.Fatal(resp, err, n)
	}
	d := sessionsDeps("legacy", false, &n)
	d.Request = func(context.Context) error { return errors.New("boom") }
	r = New(nil)
	RegisterSessionsCommand(r, d)
	if _, err = r.Dispatch(t.Context(), Command{Name: "sessions", Args: []string{"adopt"}, Confirmed: true, Caller: sessionsCaller(true)}); err == nil {
		t.Fatal("request error swallowed")
	}
}

func TestSessionsAdoptRefusedWhenDurable(t *testing.T) {
	r := New(nil)
	n := 0
	RegisterSessionsCommand(r, sessionsDeps("durable", false, &n))
	for _, confirmed := range []bool{false, true} {
		resp, err := r.Dispatch(t.Context(), Command{Name: "sessions", Args: []string{"adopt"}, Confirmed: confirmed, Caller: sessionsCaller(true)})
		if err != nil || resp.Code != 1 || resp.Text != "Already adopted." || n != 0 {
			t.Fatal(resp, err, n)
		}
	}
	resp, _ := r.Dispatch(t.Context(), Command{Name: "sessions", Args: []string{"bogus"}, Caller: sessionsCaller(true)})
	if resp.Code != 1 || resp.Text != "Use /sessions [status|adopt]." {
		t.Fatal(resp)
	}
}

func TestSessionsIsOwnerOnly(t *testing.T) {
	r := New(nil)
	n := 0
	called := false
	d := sessionsDeps("legacy", false, &n)
	d.Mode = func(context.Context) (string, bool, error) { called = true; return "legacy", false, nil }
	RegisterSessionsCommand(r, d)
	_, err := r.Dispatch(t.Context(), Command{Name: "sessions", Args: []string{"adopt"}, Confirmed: true, Caller: sessionsCaller(false)})
	if !errors.Is(err, ErrNotAuthorized) || called || n != 0 {
		t.Fatal(err, called, n)
	}
}
