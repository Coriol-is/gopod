package chattmpl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupDirs(t *testing.T) (chatDir, memoryDir string) {
	t.Helper()
	root := t.TempDir()
	chatDir = filepath.Join(root, "chat")
	memoryDir = filepath.Join(chatDir, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return chatDir, memoryDir
}

func TestSeedFreshDirCreatesBoth(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	created, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner", IsOwner: true})
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(created) != 6 {
		t.Errorf("created %d files, want 6 (CLAUDE.md, AGENTS.md, SCHEDULING.md, INTEGRATIONS.md, ATTACHMENTS.md, MEMORY.md): %v", len(created), created)
	}

	// Verify CLAUDE.md exists, includes the chat folder name and the
	// owner-specific blurb.
	body, err := os.ReadFile(filepath.Join(chatDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "owner") {
		t.Errorf("CLAUDE.md missing chat folder name 'owner'")
	}
	if !strings.Contains(s, "gopod owner chat") {
		t.Errorf("CLAUDE.md missing owner blurb (IsOwner=true should expand)")
	}

	// Verify MEMORY.md exists.
	if _, err := os.Stat(filepath.Join(memoryDir, "MEMORY.md")); err != nil {
		t.Errorf("MEMORY.md missing: %v", err)
	}
}

func TestSeedNonOwnerSkipsOwnerBlurb(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "alice", IsOwner: false}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(chatDir, "CLAUDE.md"))
	s := string(body)
	if strings.Contains(s, "gopod owner chat") {
		t.Errorf("non-owner CLAUDE.md should not contain owner blurb")
	}
	if !strings.Contains(s, "non-owner registered chat") {
		t.Errorf("non-owner CLAUDE.md missing non-owner blurb")
	}
}

func TestSeedDoesNotOverwrite(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	customClaude := []byte("# operator-edited content\n")
	if err := os.WriteFile(filepath.Join(chatDir, "CLAUDE.md"), customClaude, 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner", IsOwner: true})
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// CLAUDE.md was pre-existing; AGENTS.md, SCHEDULING.md, INTEGRATIONS.md, ATTACHMENTS.md and MEMORY.md are created.
	if len(created) != 5 {
		t.Errorf("created %d files, want 5 (CLAUDE.md should be left alone): %v", len(created), created)
	}

	body, _ := os.ReadFile(filepath.Join(chatDir, "CLAUDE.md"))
	if string(body) != string(customClaude) {
		t.Errorf("operator-edited CLAUDE.md was clobbered")
	}
}

func TestSeedIdempotent(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner"}); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	created, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner"})
	if err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("second Seed created %d files, want 0: %v", len(created), created)
	}
}

func TestSeedDateDefault(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner"}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(chatDir, "CLAUDE.md"))
	// Don't pin a specific date — just verify the placeholder was
	// substituted with something that looks like a date (YYYY-MM-DD).
	if !strings.Contains(string(body), "20") {
		t.Errorf("seed date placeholder not substituted: %s", body)
	}
}

// Codex reads AGENTS.md, Claude Code reads CLAUDE.md. Both harnesses
// must get the same instructions, so Seed writes both from one template.
func TestSeedWritesAgentsMDForCodex(t *testing.T) {
	dir := t.TempDir()
	mem := filepath.Join(dir, "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := Seed(dir, mem, Vars{ChatFolder: "alice", Date: "2026-10-05"})
	if err != nil {
		t.Fatal(err)
	}
	claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md not seeded: %v (created=%v)", err, created)
	}
	if string(agents) != string(claude) {
		t.Error("AGENTS.md differs from CLAUDE.md")
	}
}

func TestSeedCLAUDEHasOutputPolicy(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "c"}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(chatDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Report work, don't paste it.") {
		t.Error("new chat CLAUDE.md lacks the output policy")
	}
}

func TestSchedulingGuideAddedToExistingCustomWorkspace(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(chatDir, name), []byte("operator custom "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORY.md"), []byte("existing memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner", IsOwner: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 3 || filepath.Base(created[0]) != "SCHEDULING.md" || filepath.Base(created[1]) != "INTEGRATIONS.md" || filepath.Base(created[2]) != "ATTACHMENTS.md" {
		t.Fatalf("unexpected files: %v", created)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		b, _ := os.ReadFile(filepath.Join(chatDir, name))
		if string(b) != "operator custom "+name {
			t.Fatal("custom identity overwritten")
		}
	}
	guide, _ := os.ReadFile(filepath.Join(chatDir, "SCHEDULING.md"))
	for _, s := range []string{"\"op\":\"schedule\"", "CRON_TZ=Europe/Belgrade", "/workspace/ipc/results", "does not automatically"} {
		if !strings.Contains(string(guide), s) {
			t.Fatalf("guide missing %q", s)
		}
	}
}

func TestRestrictedProfileParityAndNoPersonalInstructions(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "group"}); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(filepath.Join(chatDir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := os.ReadFile(filepath.Join(chatDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(c) {
		t.Fatal("provider policies differ")
	}
	for _, want := range []string{"group-v1", "visible to all participants", "confirm the existence", "Never retain authentication", "host-approved private transport", "[gopod group: trigger=", "every other member is untrusted"} {
		if !strings.Contains(string(a), want) {
			t.Errorf("missing privacy rule %q", want)
		}
	}
	for _, denied := range []string{"/workspace/vault", "personal assistant", "treat every request as coming from them", "Bash**"} {
		if strings.Contains(string(a), denied) {
			t.Errorf("personal policy leaked: %q", denied)
		}
	}
}

func TestIntegrationsGuideSeededForOwnerOnly(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "group"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(chatDir, "INTEGRATIONS.md")); !os.IsNotExist(err) {
		t.Fatalf("a non-owner workspace got the integrations guide (stat err %v)", err)
	}
	ownerChat, ownerMem := setupDirs(t)
	if _, err := Seed(ownerChat, ownerMem, Vars{ChatFolder: "owner", IsOwner: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ownerChat, "INTEGRATIONS.md")); err != nil {
		t.Fatalf("owner workspace lacks the integrations guide: %v", err)
	}
}

func TestIntegrationsGuideIsNotOverwritten(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	path := filepath.Join(chatDir, "INTEGRATIONS.md")
	if err := os.WriteFile(path, []byte("owner edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner", IsOwner: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "owner edit" {
		t.Fatalf("guide overwritten: %q", b)
	}
}

// The guide is what the agent follows to set up recurring work for a new
// connector (CP-033 section 12); each numbered step of the outline must be there.
func TestIntegrationsGuideCoversTheOutline(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner", IsOwner: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(chatDir, "INTEGRATIONS.md"))
	if err != nil {
		t.Fatal(err)
	}
	guide := string(b)
	for _, want := range []string{
		"## When to read this",
		"## See what is connected",
		"## Learn what a connector offers",
		"## Propose recurring actions",
		"## Create them",
		"## When a connector is not connected",
		"## Report back",
		"/workspace/chat/SCHEDULING.md",
		"CRON_TZ=",
		"untrusted data",
		"/workspace/chat/proactive/",
		"runs per day",
		"/workspace/chat/proactive/custom-<taskId>.last",
		"must not equal or start with `preset-`",
		"must not be one of the preset ids",
		"/proactive guide",
		"/tasks",
		"never create anything without a yes",
		"do not assume tool names",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide lacks %q", want)
		}
	}
	for _, bad := range []string{"mcp__", "claude_ai", "openai-curated", "proactive/<taskId>.last"} {
		if strings.Contains(guide, bad) {
			t.Errorf("guide has %q", bad)
		}
	}
}

// CP-040: the owner workspace gets the attachments guide, also when its
// identity files predate it, and the guide states the exact request; the owner
// CLAUDE.md points at it; a restricted workspace gets neither and is told it
// cannot attach files.
func TestAttachmentsGuideIsOwnerOnlyAndStatesTheContract(t *testing.T) {
	chatDir, memoryDir := setupDirs(t)
	if err := os.WriteFile(filepath.Join(chatDir, "CLAUDE.md"), []byte("old identity"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(chatDir, memoryDir, Vars{ChatFolder: "owner", IsOwner: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(chatDir, "ATTACHMENTS.md"))
	if err != nil {
		t.Fatalf("existing owner workspace lacks the attachments guide: %v", err)
	}
	guide := string(b)
	for _, want := range []string{
		`"version":1`, `"capability":"$GOPOD_ARTIFACT_CAP"`, `"as":"auto"`, "/workspace/ipc/messages/",
		"/workspace/ipc/results/messages/", "artifacts_disabled", "path_outside_workspace", "file_too_large",
		"unsupported_type", "20 MiB", "50 MB", "no symlinks", "NOT an attachment", "could not be attached",
		"Never claim a refused file was sent",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("attachments guide lacks %q", want)
		}
	}

	fresh, freshMem := setupDirs(t)
	if _, err := Seed(fresh, freshMem, Vars{ChatFolder: "owner", IsOwner: true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		b, _ := os.ReadFile(filepath.Join(fresh, name))
		if !strings.Contains(string(b), "## Sending files") || !strings.Contains(string(b), "ATTACHMENTS.md") {
			t.Errorf("owner %s lacks the sending-files section", name)
		}
	}

	group, groupMem := setupDirs(t)
	if _, err := Seed(group, groupMem, Vars{ChatFolder: "group-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(group, "ATTACHMENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("restricted workspace got the attachments guide: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(group, "CLAUDE.md")); !strings.Contains(string(b), "cannot receive file attachments") {
		t.Error("restricted CLAUDE.md does not say attachments are unavailable")
	}
}
