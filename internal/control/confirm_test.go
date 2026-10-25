package control

import (
	"context"
	"strings"
	"testing"
)

func TestConfirm_BuildsYesAndCancel(t *testing.T) {
	cmd := Command{Name: "provider", Args: []string{"codex"}}
	resp := Confirm(cmd, "Switch?")
	if resp.Code != CodeConfirm || resp.Text != "Switch?" {
		t.Fatalf("Confirm = %+v", resp)
	}
	if len(resp.Buttons) != 1 || len(resp.Buttons[0]) != 2 {
		t.Fatalf("buttons = %+v", resp.Buttons)
	}
	yes, cancel := resp.Buttons[0][0], resp.Buttons[0][1]
	if yes.Command != "provider" || len(yes.Args) != 1 || yes.Args[0] != "codex" ||
		!yes.Confirmed || yes.Style != StyleDanger {
		t.Errorf("yes = %+v", yes)
	}
	if cancel.Command != "" || cancel.Confirmed {
		t.Errorf("cancel = %+v", cancel)
	}
	// The Yes button's Args must not alias the command's slice.
	cmd.Args[0] = "x"
	if yes.Args[0] != "codex" {
		t.Error("Yes args alias the command args")
	}
}

func TestConfirm_CLIFlagSkipsPrompt(t *testing.T) {
	var cleared int
	r := New(silentLog())
	RegisterSessionCommands(r,
		func(_ context.Context, _ string, _ int, clear bool) (string, error) {
			if clear {
				cleared++
			}
			return "", nil
		},
		func(int64) string { return "owner" })

	// Without --yes the CLI gets the question and a hint, nothing runs.
	args, yes := ParseConfirmFlag([]string{})
	resp, err := r.Dispatch(context.Background(), Command{
		Name: "clear", Args: args, Confirmed: yes,
		Caller: Caller{Source: SourceCLI, IsOwner: true},
	})
	if err != nil || resp.Code != CodeConfirm || cleared != 0 {
		t.Fatalf("no flag: resp=%+v err=%v cleared=%d", resp, err, cleared)
	}
	if out := FormatText(resp); !strings.Contains(out, "--yes") {
		t.Errorf("confirm prompt lacks the --yes hint:\n%s", out)
	}

	// With --yes it runs at once, and the flag is not passed as an arg.
	args, yes = ParseConfirmFlag([]string{"--yes"})
	if !yes || len(args) != 0 {
		t.Fatalf("ParseConfirmFlag(--yes) = %v, %v", args, yes)
	}
	resp, err = r.Dispatch(context.Background(), Command{
		Name: "clear", Args: args, Confirmed: yes,
		Caller: Caller{Source: SourceCLI, IsOwner: true},
	})
	if err != nil || resp.Code != 0 || cleared != 1 {
		t.Fatalf("--yes: resp=%+v err=%v cleared=%d", resp, err, cleared)
	}
	if a, y := ParseConfirmFlag([]string{"x", "-y", "z"}); !y || len(a) != 2 || a[0] != "x" || a[1] != "z" {
		t.Errorf("ParseConfirmFlag(-y) = %v, %v", a, y)
	}
}

func TestClear_AsksConfirmation(t *testing.T) {
	var cleared int
	r := New(silentLog())
	RegisterSessionCommands(r,
		func(_ context.Context, _ string, _ int, clear bool) (string, error) {
			if clear {
				cleared++
			}
			return "", nil
		},
		func(int64) string { return "owner" })

	resp, err := r.Dispatch(context.Background(), Command{
		Name: "clear", Caller: Caller{ChatID: 1, Source: SourceTelegram},
	})
	if err != nil || resp.Code != CodeConfirm || cleared != 0 {
		t.Fatalf("clear without confirmation: resp=%+v err=%v cleared=%d", resp, err, cleared)
	}
	yes := resp.Buttons[0][0]
	if yes.Command != "clear" || !yes.Confirmed {
		t.Fatalf("yes = %+v", yes)
	}
	// /compact does not ask.
	resp, _ = r.Dispatch(context.Background(), Command{
		Name: "compact", Caller: Caller{ChatID: 1, Source: SourceTelegram},
	})
	if resp.Code == CodeConfirm {
		t.Error("compact asked for confirmation")
	}
}

func TestProvider_PickerAndConfirmation(t *testing.T) {
	var switched []string
	r := New(silentLog())
	RegisterProviderCommand(r,
		func(_ context.Context, _, name string) (string, error) {
			switched = append(switched, name)
			return "Switched to " + name, nil
		},
		func(string) string { return "claude" },
		[]string{"claude", "codex"},
		func(int64) string { return "owner" })
	call := func(confirmed bool, args ...string) Response {
		resp, err := r.Dispatch(context.Background(), Command{
			Name: "provider", Args: args, Confirmed: confirmed,
			Caller: Caller{ChatID: 1, Source: SourceTelegram},
		})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		return resp
	}

	picker := call(false)
	if picker.Code != 0 || len(picker.Buttons) == 0 {
		t.Fatalf("picker = %+v", picker)
	}
	var labels []string
	for _, row := range picker.Buttons {
		for _, b := range row {
			labels = append(labels, b.Label)
			if b.Command != "provider" || len(b.Args) != 1 {
				t.Errorf("picker button %+v", b)
			}
			if b.Confirmed {
				t.Errorf("picker button %q skips the confirmation", b.Label)
			}
			if (b.Args[0] == "claude") != (b.Style == StylePrimary) {
				t.Errorf("current provider not marked primary: %+v", b)
			}
		}
	}
	if len(labels) != 2 {
		t.Fatalf("labels = %v", labels)
	}

	// Unknown and current providers are answered without a question.
	if resp := call(false, "xyz"); resp.Code != 1 || len(resp.Buttons) != 0 ||
		!strings.Contains(resp.Text, "unknown provider") || len(switched) != 0 {
		t.Fatalf("unknown provider: %+v", resp)
	}
	if resp := call(false, "claude"); resp.Code != 0 || len(resp.Buttons) != 0 ||
		!strings.Contains(resp.Text, "Already using claude") || len(switched) != 0 {
		t.Fatalf("current provider: %+v", resp)
	}
	if resp := call(false, "codex"); resp.Code != CodeConfirm || len(switched) != 0 {
		t.Fatalf("unconfirmed switch: %+v, switched %v", resp, switched)
	}
	if resp := call(true, "codex"); resp.Code != 0 || len(switched) != 1 || switched[0] != "codex" {
		t.Fatalf("confirmed switch: %+v, switched %v", resp, switched)
	}
}

func TestModelPicker_Buttons(t *testing.T) {
	r := New(silentLog())
	RegisterModelCommand(r,
		func(string) string { return "sonnet" },
		func(_, model string) (string, error) { return "set " + model, nil },
		func(string) []string { return []string{"opus", "sonnet"} },
		func(int64) string { return "owner" })
	resp, err := r.Dispatch(context.Background(), Command{
		Name: "model", Caller: Caller{ChatID: 1, Source: SourceTelegram},
	})
	if err != nil || resp.Code != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	got := map[string]Button{}
	for _, row := range resp.Buttons {
		for _, b := range row {
			if b.Command != "model" || len(b.Args) != 1 {
				t.Fatalf("button %+v", b)
			}
			got[b.Args[0]] = b
		}
	}
	for _, want := range []string{"opus", "sonnet", "default"} {
		if _, ok := got[want]; !ok {
			t.Errorf("no button for %q in %+v", want, resp.Buttons)
		}
	}
	if got["sonnet"].Style != StylePrimary || got["opus"].Style == StylePrimary {
		t.Errorf("current model not marked: %+v", got)
	}
}

func TestProviderSwitchReceivesCommandContext(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "command"))
	cancel()
	r := New(silentLog())
	called := false
	RegisterProviderCommand(r, func(got context.Context, _, _ string) (string, error) {
		called = true
		if got.Value(key{}) != "command" || got.Err() != context.Canceled {
			t.Fatal("provider switch lost command context")
		}
		return "", got.Err()
	}, func(string) string { return "claude" }, []string{"claude", "codex"}, func(int64) string { return "owner" })
	_, _ = r.Dispatch(ctx, Command{Name: "provider", Args: []string{"codex"}, Confirmed: true, Caller: Caller{ChatID: 42, IsOwner: true}})
	if !called {
		t.Fatal("switch not called")
	}
}
