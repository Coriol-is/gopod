package runner

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMergeNodeOptions(t *testing.T) {
	cases := []struct {
		name, existing string
		heap           int
		want           string
	}{
		{"empty", "", 768, "--max-old-space-size=768"},
		{"preserve other flags", "--enable-source-maps", 768, "--enable-source-maps --max-old-space-size=768"},
		{"already capped", "--max-old-space-size=2048 --trace-warnings", 768, "--max-old-space-size=2048 --trace-warnings"},
		{"underscore spelling", "--max_old_space_size=2048", 768, "--max_old_space_size=2048"},
		{"disabled", "--enable-source-maps", 0, "--enable-source-maps"},
		{"blank existing", "   ", 512, "--max-old-space-size=512"},
	}
	for _, c := range cases {
		if got := mergeNodeOptions(c.existing, c.heap); got != c.want {
			t.Errorf("%s: mergeNodeOptions(%q, %d) = %q, want %q", c.name, c.existing, c.heap, got, c.want)
		}
	}
}

func TestWithNodeHeap(t *testing.T) {
	base := []string{"GOPOD_MEMORY_CAPABILITY=tok"}
	got := withNodeHeap(base, "", 768)
	want := []string{"GOPOD_MEMORY_CAPABILITY=tok", "NODE_OPTIONS=--max-old-space-size=768"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	// The container's own NODE_OPTIONS is merged, not overwritten.
	got = withNodeHeap(nil, "--enable-source-maps", 768)
	if !reflect.DeepEqual(got, []string{"NODE_OPTIONS=--enable-source-maps --max-old-space-size=768"}) {
		t.Fatalf("got %q", got)
	}
	// An exec env that already sets NODE_OPTIONS wins over the container's.
	got = withNodeHeap([]string{"NODE_OPTIONS=--inspect"}, "--enable-source-maps", 768)
	if !reflect.DeepEqual(got, []string{"NODE_OPTIONS=--inspect --max-old-space-size=768"}) {
		t.Fatalf("got %q", got)
	}
	// Disabled: env untouched, nothing appended.
	if got := withNodeHeap(base, "--x", 0); !reflect.DeepEqual(got, base) {
		t.Fatalf("disabled got %q", got)
	}
}

// envExec records the env of every agent exec.
type envExec struct {
	*fakeAgentExec
	envs [][]string
}

func (e *envExec) Exec(ctx context.Context, id string, cmd, env []string) (*ExecResult, error) {
	if len(cmd) > 0 && (cmd[0] == "claude" || cmd[0] == "codex") {
		e.envs = append(e.envs, env)
	}
	return e.fakeAgentExec.Exec(ctx, id, cmd, env)
}

func nodeOptionsOf(env []string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "NODE_OPTIONS="); ok {
			return v, true
		}
	}
	return "", false
}

func TestAgentExecEnvClaudeOnly(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "--enable-source-maps")
	for _, tc := range []struct {
		prov    AgentProvider
		allow   []string
		want    string
		present bool
	}{
		{NewClaudeProvider("img"), nil, "--max-old-space-size=768", true},
		// NODE_OPTIONS forwarded into the container: merged.
		{NewClaudeProvider("img"), []string{"NODE_OPTIONS"}, "--enable-source-maps --max-old-space-size=768", true},
		{NewCodexProvider("img"), nil, "", false},
	} {
		ex := &envExec{fakeAgentExec: newFakeAgentExec()}
		r := &Runner{ex: ex, provider: tc.prov, allow: tc.allow, log: quietLog(), nodeHeapMB: 768}
		if _, err := r.execAgent(context.Background(), "cid", "chat", 0, tc.prov, tc.prov.RunFreshCmd("p")); err != nil {
			t.Fatalf("%s: %v", tc.prov.Name(), err)
		}
		got, ok := nodeOptionsOf(ex.envs[0])
		if ok != tc.present || got != tc.want {
			t.Errorf("%s allow=%v: NODE_OPTIONS = %q (set %v), want %q (set %v)",
				tc.prov.Name(), tc.allow, got, ok, tc.want, tc.present)
		}
	}
}

func TestSetAgentLimits(t *testing.T) {
	r := &Runner{log: quietLog()}
	r.SetAgentLimits(AgentLimits{MaxAgents: 3, MinAvailableMB: 1000, NodeHeapMB: 512})
	if r.gate == nil || r.gate.max != 3 || r.gate.minAvailMB != 1000 || r.gate.readMem == nil || r.nodeHeapMB != 512 {
		t.Fatalf("gate=%+v heap=%d", r.gate, r.nodeHeapMB)
	}
	r.SetAgentLimits(AgentLimits{MaxAgents: 0})
	if r.gate.max != 1 || r.gate.minAvailMB != 0 || r.nodeHeapMB != 0 {
		t.Fatalf("zero limits: max=%d min=%d heap=%d", r.gate.max, r.gate.minAvailMB, r.nodeHeapMB)
	}
}

func (e *envExec) ExecStream(ctx context.Context, id string, cmd, env []string) (*StreamHandle, error) {
	e.envs = append(e.envs, env)
	return e.fakeAgentExec.ExecStream(ctx, id, cmd, env)
}

func TestStreamedClaudeAgentGetsHeapCap(t *testing.T) {
	ex := &envExec{fakeAgentExec: newFakeAgentExec()}
	r := &Runner{ex: ex, log: quietLog(), nodeHeapMB: 768,
		turnCount: map[convKey]int{}, activity: map[string]time.Time{}}
	prov := NewClaudeProvider("img")
	stream, err := r.startStream(context.Background(), "cid", convKey{"chat", 0}, TierRegistered, nil, prov, "hi",
		fixedCmd(prov.RunCmd("hi", "", SessionRef{})))
	if err != nil {
		t.Fatal(err)
	}
	(<-ex.started).exit(0)
	readResult(t, stream)
	if got, _ := nodeOptionsOf(ex.envs[0]); got != "--max-old-space-size=768" {
		t.Fatalf("streamed NODE_OPTIONS = %q", got)
	}
}

// Forwarded credential env belongs to the personal foreground tier only; the
// managed task role forwards nothing (CP-004 PR 3).
func TestContainerEnvAllowByTierForBothProviders(t *testing.T) {
	for _, prov := range providersUnderTest() {
		if got := containerEnvAllow(TierRegistered, prov, []string{"EXTRA"}); len(got) != 0 {
			t.Errorf("%s: registered tier forwards %v", prov.Name(), got)
		}
		got := containerEnvAllow(TierOwner, prov, []string{"EXTRA"})
		if !slices.Contains(got, "EXTRA") || !slices.Contains(got, prov.RequiredEnvVars()[0]) {
			t.Errorf("%s: owner tier lost its default env: %v", prov.Name(), got)
		}
	}
}
