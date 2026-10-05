package control

import (
	"context"
	"strings"
	"testing"
)

func TestModelCommand(t *testing.T) {
	r := New(nil)
	current := map[string]string{}
	RegisterModelCommand(r,
		func(folder string) string { return current[folder] },
		func(folder, model string) (string, error) { current[folder] = model; return "ok " + model, nil },
		func(chatID int64) string {
			if chatID == 1 {
				return "owner"
			}
			return ""
		})
	call := func(args ...string) Response {
		resp, err := r.Dispatch(context.Background(), Command{Name: "model", Args: args, Caller: Caller{ChatID: 1, Source: SourceTelegram}})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		return resp
	}
	if resp := call(); !strings.Contains(resp.Text, "default") {
		t.Errorf("no-arg should show default, got %q", resp.Text)
	}
	if resp := call("sonnet"); !strings.Contains(resp.Text, "ok sonnet") || current["owner"] != "sonnet" {
		t.Errorf("set failed: %q / %v", resp.Text, current)
	}
	if resp := call(); !strings.Contains(resp.Text, "sonnet") {
		t.Errorf("show after set = %q", resp.Text)
	}
	if resp := call("default"); current["owner"] != "" || !strings.Contains(resp.Text, "ok ") {
		t.Errorf("default should clear: %q / %v", resp.Text, current)
	}
	resp, _ := r.Dispatch(context.Background(), Command{Name: "model", Args: []string{"x"}, Caller: Caller{ChatID: 99, Source: SourceTelegram}})
	if resp.Code == 0 {
		t.Error("unregistered chat must be refused")
	}
}
