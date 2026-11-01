package control

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSilentCommand(t *testing.T) {
	r := New(nil)
	state := map[string]bool{}
	RegisterSilentCommand(r,
		func(folder string) bool { return state[folder] },
		func(folder string, on bool) error { state[folder] = on; return nil },
		func(chatID int64) string {
			if chatID == 1 {
				return "owner"
			}
			return ""
		})
	call := func(args ...string) Response {
		t.Helper()
		resp, err := r.Dispatch(context.Background(), Command{Name: "silent", Args: args, Caller: Caller{ChatID: 1, Source: SourceTelegram}})
		if err != nil {
			t.Fatalf("dispatch %v: %v", args, err)
		}
		return resp
	}
	marked := func(resp Response) string {
		t.Helper()
		if len(resp.Buttons) != 1 || len(resp.Buttons[0]) != 2 {
			t.Fatalf("buttons = %+v, want one row of two", resp.Buttons)
		}
		for _, b := range resp.Buttons[0] {
			if b.Command != "silent" || len(b.Args) != 1 {
				t.Fatalf("button %+v must re-dispatch /silent with one arg", b)
			}
			if strings.HasPrefix(b.Label, "✓ ") {
				return b.Args[0]
			}
		}
		t.Fatal("no marked button")
		return ""
	}

	resp := call()
	if !strings.Contains(resp.Text, "Silent mode: off") {
		t.Errorf("no-arg text = %q", resp.Text)
	}
	if got := marked(resp); got != "off" {
		t.Errorf("marked = %q, want off", got)
	}

	if resp := call("on"); !state["owner"] || !strings.Contains(resp.Text, "Silent mode on") || resp.Code != 0 {
		t.Errorf("on: %q / %v", resp.Text, state)
	}
	resp = call()
	if !strings.Contains(resp.Text, "Silent mode: on") || marked(resp) != "on" {
		t.Errorf("show after on = %q", resp.Text)
	}
	if resp := call("OFF"); state["owner"] || !strings.Contains(resp.Text, "Silent mode off") {
		t.Errorf("off: %q / %v", resp.Text, state)
	}
	call("on")
	if resp := call("maybe"); resp.Code == 0 || !state["owner"] {
		t.Errorf("unknown arg must be refused and keep state: %q / %v", resp.Text, state)
	}

	resp, _ = r.Dispatch(context.Background(), Command{Name: "silent", Args: []string{"on"}, Caller: Caller{ChatID: 99, Source: SourceTelegram}})
	if resp.Code == 0 {
		t.Error("unregistered chat must be refused")
	}
}

// In a group only the owner may toggle silent mode; a member is refused
// before any state is touched. Another chat's flag is never affected.
func TestSilentCommand_GroupScope(t *testing.T) {
	r := New(nil)
	state := map[string]bool{}
	folders := map[int64]string{1: "owner", -500: "grp"}
	RegisterSilentCommand(r,
		func(folder string) bool { return state[folder] },
		func(folder string, on bool) error { state[folder] = on; return nil },
		func(chatID int64) string { return folders[chatID] })

	resp, err := r.Dispatch(context.Background(), Command{Name: "silent", Args: []string{"on"}, Caller: Caller{ChatID: -500, UserID: 7, Source: SourceTelegram}})
	if !errors.Is(err, ErrNotAuthorized) || resp.Code == 0 || state["grp"] {
		t.Fatalf("group member: err=%v resp=%+v state=%v, want refused", err, resp, state)
	}
	resp, err = r.Dispatch(context.Background(), Command{Name: "silent", Args: []string{"on"}, Caller: Caller{ChatID: -500, UserID: 1, Source: SourceTelegram, IsGroupOwner: true}})
	if err != nil || resp.Code != 0 || !state["grp"] {
		t.Fatalf("group owner: err=%v resp=%+v state=%v, want set", err, resp, state)
	}
	if state["owner"] {
		t.Error("another chat's flag changed")
	}
	resp, _ = r.Dispatch(context.Background(), Command{Name: "silent", Caller: Caller{ChatID: 1, Source: SourceTelegram}})
	if !strings.Contains(resp.Text, "Silent mode: off") {
		t.Errorf("private chat shows %q, want off", resp.Text)
	}
}
