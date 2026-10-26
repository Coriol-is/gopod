package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const testGroupURL = "http://gopod-relay:8787/mcp/abcdefghijklmnopqrstuvwxyz0123456789"
const testGroupSession = "01234567-89ab-cdef-0123-456789abcdef"

func groupFixture(t *testing.T, name string) AgentProvider {
	t.Helper()
	var base AgentProvider = NewClaudeProvider("")
	if name == "codex" {
		base = NewCodexProvider("")
	}
	p, err := GroupAgentProvider(base, GroupProviderConfig{Image: "group-" + name + ":qualified", MCPURL: testGroupURL})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestGroupProviderRejectsFallbackAndForeignEndpoints(t *testing.T) {
	base := NewClaudeProvider("")
	for _, cfg := range []GroupProviderConfig{
		{MCPURL: testGroupURL}, {Image: base.Image(), MCPURL: testGroupURL},
		{Image: "group", MCPURL: "http://example.com:8787/mcp/abcdefghijklmnopqrstuvwxyz0123456789"},
		{Image: "group", MCPURL: testGroupURL + "?secret=1"},
		{Image: "group", MCPURL: "http://gopod-relay:8787/mcp/short"},
		{Image: "group", MCPURL: strings.Replace(testGroupURL, "/mcp/", "/mcp/%2f", 1)},
		{Image: "group", MCPURL: strings.Replace(testGroupURL, "gopod-relay", "user:password@gopod-relay", 1)},
	} {
		if _, err := GroupAgentProvider(base, cfg); err == nil {
			t.Errorf("accepted invalid config image=%q", cfg.Image)
		}
	}
	if _, err := GroupAgentProvider(nil, GroupProviderConfig{Image: "group", MCPURL: testGroupURL}); err == nil {
		t.Fatal("accepted missing provider")
	}
}
func TestGroupCommandsKeepNativeAuthorityClosedAcrossSessionModes(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			p := groupFixture(t, name)
			for _, ref := range []SessionRef{{}, {ID: testGroupSession}, {ID: testGroupSession, Resume: true}} {
				cmd := p.RunCmd("untrusted prompt", "supplied group memory", ref)
				joined := strings.Join(cmd, "\n")
				for _, forbidden := range []string{"--dangerously-skip-permissions", "--dangerously-bypass-approvals-and-sandbox", "--continue", "--last", "--profile", "--plugin-dir"} {
					if slices.Contains(cmd, forbidden) {
						t.Errorf("unsafe command token %s", forbidden)
					}
				}
				if !strings.Contains(joined, "Never retrieve, infer, quote") || !strings.Contains(joined, "supplied group memory") {
					t.Error("trusted group policy/context missing")
				}
				if name == "claude" {
					i := slices.Index(cmd, "--tools")
					if i < 0 || cmd[i+1] != "" {
						t.Fatal("builtins enabled")
					}
					if !slices.Contains(cmd, "--strict-mcp-config") || !slices.Contains(cmd, "--restricted") {
						t.Fatal("native policy missing")
					}
				} else if !slices.Contains(cmd, "--strict-config") {
					t.Fatal("Codex config not strict")
				}
				if ref.Resume && !slices.Contains(cmd, testGroupSession) {
					t.Error("explicit resume lost")
				}
			}
			if len(p.RequiredEnvVars()) != 0 {
				t.Error("host credentials inherited")
			}
			if p.RestoreConfigCmd() != nil {
				t.Error("personal config restore allowed")
			}
			if !slices.Contains(p.RunFreshCmd("summary"), map[string]string{"claude": "--no-session-persistence", "codex": "--ephemeral"}[name]) {
				t.Error("fresh run persisted")
			}
			if !slices.Contains(p.LoginCmd(), "sh") || !strings.Contains(strings.Join(p.LoginCmd(), " "), "exit 2") {
				t.Error("unauthorized group login allowed")
			}
			if !reflect.DeepEqual(p.LatestSessionIDCmd(), []string{"sh", "-c", "exit 0"}) {
				t.Error("legacy adoption possible")
			}
			for _, bad := range []SessionRef{{ID: "../../private"}, {Resume: true}} {
				if got := p.RunCmd("x", "", bad); !reflect.DeepEqual(got, invalidSessionIDCmd()) {
					t.Error("invalid session not refused")
				}
			}
		})
	}
}
func TestGroupNativePolicyFilesAreFixedAndProviderScoped(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		p := groupFixture(t, name)
		native := p.(interface {
			PolicyFiles() map[string][]byte
			GroupExecEnv() []string
			PolicyVersion() string
		})
		if native.PolicyVersion() != GroupNativePolicyVersion {
			t.Fatal("unversioned policy")
		}
		files := native.PolicyFiles()
		if len(files) != 1 {
			t.Fatal("unexpected config inventory")
		}
		if name == "claude" {
			var cfg map[string]any
			if err := json.Unmarshal(files[groupMCPFile], &cfg); err != nil {
				t.Fatal(err)
			}
			servers := cfg["mcpServers"].(map[string]any)
			if len(servers) != 1 || servers["group"] == nil {
				t.Fatal("extra MCP servers")
			}
		} else {
			s := string(files[groupCodexConfig])
			for _, wanted := range []string{"sandbox_mode = \"read-only\"", "approval_policy = \"never\"", "required = true", "enabled_tools = [\"group_exec\", \"group_request\"]"} {
				if !strings.Contains(s, wanted) {
					t.Errorf("missing %s", wanted)
				}
			}
			for _, feature := range groupDisabledCodexFeatures {
				if !strings.Contains(s, feature+" = false") {
					t.Errorf("feature not denied: %s", feature)
				}
			}
		}
		for path, b := range files {
			b[0] = '!'
			if native.PolicyFiles()[path][0] == '!' {
				t.Error("caller mutated immutable policy")
			}
		}
		for _, env := range native.GroupExecEnv() {
			if strings.Contains(env, "API_KEY=") {
				t.Error("API billing fallback")
			}
		}
	}
	if _, ok := groupFixture(t, "claude").(StructuredStreamer); !ok {
		t.Fatal("Claude streaming lost")
	}
	if _, ok := groupFixture(t, "codex").(StructuredStreamer); ok {
		t.Fatal("unsupported Codex streaming added")
	}
}
func TestGroupImageMatrixPreservesPersonalAndExcludesPersonalTools(t *testing.T) {
	root := filepath.Join("..", "..", "container")
	for _, suffix := range []string{"", ".codex"} {
		old, err := os.ReadFile(filepath.Join(root, "Dockerfile"+suffix))
		if err != nil {
			t.Fatal(err)
		}
		personal, err := os.ReadFile(filepath.Join(root, "personal", "Dockerfile"+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(old, personal) {
			t.Error("personal image behavior changed")
		}
		group, err := os.ReadFile(filepath.Join(root, "group", "Dockerfile"+suffix))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"frf-builder", "COPY skills", "COPY --from=frf", "curl ", "ripgrep ", "gopod-agent:latest"} {
			if strings.Contains(string(group), bad) {
				t.Errorf("group inherited personal tool: %s", bad)
			}
		}
		if !strings.Contains(string(group), "WORKDIR /workspace/inference") || !strings.Contains(string(group), "org.gopod.profile=\"group\"") {
			t.Error("missing group identity")
		}
	}
	if _, err := (GroupImageSet{}).Resolve("claude"); err == nil {
		t.Error("missing group image fell back")
	}
	if _, err := (GroupImageSet{Claude: "a", Codex: "b"}).Resolve("unknown"); err == nil {
		t.Error("unknown provider accepted")
	}
}
