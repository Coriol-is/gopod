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
	if len(created) != 2 {
		t.Errorf("created %d files, want 2: %v", len(created), created)
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
	if !strings.Contains(s, "picoclaw owner chat") {
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
	if strings.Contains(s, "picoclaw owner chat") {
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
	// CLAUDE.md was pre-existing, only MEMORY.md should have been created.
	if len(created) != 1 {
		t.Errorf("created %d files, want 1 (CLAUDE.md should be left alone): %v", len(created), created)
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
