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
					sp := promptForTier(t, h, prov, mode, TierOwner)
					if !strings.Contains(sp, SchedulingPolicy) {
						t.Fatal("scheduling guide reference absent from agent prompt")
					}
					if !strings.Contains(sp, IntegrationsPolicy) {
						t.Fatal("integrations guide reference absent from agent prompt")
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

// promptForTier runs one turn at the given tier and returns the system prompt
// the provider's argv carried.
func promptForTier(t *testing.T, h *convHarness, prov AgentProvider, mode string, tier Tier) string {
	t.Helper()
	if mode == "Run" {
		if _, err := h.r.Run(context.Background(), convChat, 0, tier, nil, "hi"); err != nil {
			t.Fatalf("Run: %v", err)
		}
	} else {
		stream, err := h.r.RunStream(context.Background(), convChat, 0, tier, nil, "hi")
		if err != nil {
			t.Fatalf("RunStream: %v", err)
		}
		if res := readResult(t, stream); res.Err != nil {
			t.Fatalf("stream: %v", res.Err)
		}
	}
	return systemPromptIn(t, prov, h.home.lastRun())
}

// Only the owner's own turns learn about the integrations guide: a group
// member who got the agent to create the file must not make later turns read it.
func TestIntegrationsPolicyIsOwnerTurnsOnly(t *testing.T) {
	for _, prov := range providersUnderTest() {
		for _, mode := range []string{"Run", "RunStream"} {
			t.Run(prov.Name()+"/"+mode, func(t *testing.T) {
				if sp := promptForTier(t, newConvHarness(t, prov), prov, mode, TierRegistered); strings.Contains(sp, IntegrationsPolicy) || strings.Contains(sp, "INTEGRATIONS.md") {
					t.Fatalf("a non-owner turn carries the integrations policy:\n%s", sp)
				}
				if sp := promptForTier(t, newConvHarness(t, prov), prov, mode, TierOwner); !strings.Contains(sp, IntegrationsPolicy) {
					t.Fatal("the owner turn lacks the integrations policy")
				}
			})
		}
	}
}

func TestSystemPromptWithOmitsIntegrationsPolicyUnlessAsked(t *testing.T) {
	for _, mem := range []string{"", "memory"} {
		if sp := systemPromptWith(mem, false); strings.Contains(sp, IntegrationsPolicy) || !strings.Contains(sp, SchedulingPolicy) || !strings.Contains(sp, OutputPolicy) {
			t.Errorf("owner=false mem=%q: %q", mem, sp)
		}
		if sp := systemPromptWith(mem, true); !strings.Contains(sp, IntegrationsPolicy) {
			t.Errorf("owner=true mem=%q lacks the policy", mem)
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func TestIntegrationsPolicyPointsAtTheGuideOnlyWhenItExists(t *testing.T) {
	for _, want := range []string{"/workspace/chat/INTEGRATIONS.md", "If that file does not exist", "SCHEDULING.md", "without a yes"} {
		if !strings.Contains(IntegrationsPolicy, want) {
			t.Errorf("IntegrationsPolicy lacks %q", want)
		}
	}
	for _, bad := range []string{"mcp__", "claude_ai"} {
		if strings.Contains(IntegrationsPolicy, bad) {
			t.Errorf("policy names a provider-specific tool %q", bad)
		}
	}
}
