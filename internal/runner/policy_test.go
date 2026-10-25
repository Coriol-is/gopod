package runner

import (
	"context"
	"strings"
	"testing"
)

// fixedMemory compiles the same context for every query.
type fixedMemory string

func (m fixedMemory) CompileContext(context.Context, string, int, string) string { return string(m) }

// systemPromptIn returns the system prompt a provider's final argv
// carries: Claude's --append-system-prompt value, or for Codex (which
// has no system-prompt flag) the prompt argument, where RunCmd
// prepends it.
func systemPromptIn(t *testing.T, prov AgentProvider, argv []string) string {
	t.Helper()
	if prov.Name() == "claude" {
		v, ok := flagValue(argv, "--append-system-prompt")
		if !ok {
			t.Fatalf("no --append-system-prompt in %q", argv)
		}
		return v
	}
	return argv[len(argv)-1]
}

func TestOutputPolicyReachesAgentOnEveryTurn(t *testing.T) {
	for _, prov := range providersUnderTest() {
		for _, mem := range []string{"", "## Memory\n- likes tea"} {
			for _, mode := range []string{"Run", "RunStream"} {
				t.Run(prov.Name()+"/"+mode+"/memory="+boolStr(mem != ""), func(t *testing.T) {
					h := newConvHarness(t, prov)
					h.r.memory = fixedMemory(mem)
					if mode == "Run" {
						h.run(t, 0)
					} else if res := h.runStream(t, 0); res.Err != nil {
						t.Fatalf("stream: %v", res.Err)
					}
					sp := systemPromptIn(t, prov, h.home.lastRun())
					if !strings.Contains(sp, SchedulingPolicy) {
						t.Fatal("scheduling guide reference absent from agent prompt")
					}
					if !strings.Contains(sp, OutputPolicy) {
						t.Fatalf("output policy missing from the agent argv:\n%s", sp)
					}
					if mem != "" && !strings.Contains(sp, mem) {
						t.Fatalf("memory context dropped:\n%s", sp)
					}
				})
			}
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
