package runner

import (
	"context"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

type selectedGroupRuntime struct {
	model    string
	thread   int
	provider string
}

func (g *selectedGroupRuntime) Authorize(context.Context, store.RegisteredChat) error { return nil }
func (g *selectedGroupRuntime) Start(_ context.Context, _ store.RegisteredChat, p AgentProvider, thread int, model string) (string, AgentProvider, func(), error) {
	g.model = model
	g.thread = thread
	g.provider = p.Name()
	return "qualified", p, func() {}, nil
}
func TestGroupRuntimeReceivesSelectedModelForEveryLease(t *testing.T) {
	g := &selectedGroupRuntime{}
	r := &Runner{provider: NewCodexProvider("")}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:-2", Folder: "group"}}})
	r.SetGroupRuntime(g)
	for _, model := range []string{"qualified-model", "another-model", ""} {
		r.SetChatModel("group", model)
		_, _, release, err := r.turnRuntime(t.Context(), "group", 7, TierRegistered, nil)
		if err != nil {
			t.Fatal(err)
		}
		release()
		if g.model != model || g.thread != 7 || g.provider != "codex" {
			t.Fatalf("selected model omitted %q", g.model)
		}
	}
}
