package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spaceinvaderz/gopod/internal/runner/mountsec"
)

// mkPaths builds a fully-validated Paths struct with every directory
// pre-created under t.TempDir so EnsureChatDirs / BuildMounts work
// against a real filesystem layout.
func mkPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	p := Paths{
		RepoRoot:           filepath.Join(root, "repo"),
		DataDir:            filepath.Join(root, "data"),
		ChatsDir:           filepath.Join(root, "repo", "chats"),
		ContainerSkillsDir: filepath.Join(root, "repo", "container", "skills"),
		EmptyFile:          filepath.Join(root, "data", "empty-env"),
	}
	for _, d := range []string{p.RepoRoot, p.DataDir, p.ChatsDir, p.ContainerSkillsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}
	return p
}

func TestPathsValidate(t *testing.T) {
	good := mkPaths(t)
	if err := good.Validate(); err != nil {
		t.Errorf("good Paths: %v", err)
	}

	bad := good
	bad.RepoRoot = ""
	if err := bad.Validate(); err == nil {
		t.Error("empty RepoRoot: want error")
	}

	bad = good
	bad.DataDir = "relative/path"
	if err := bad.Validate(); err == nil {
		t.Error("relative DataDir: want error")
	}
}

func TestEnsureChatDirsIdempotent(t *testing.T) {
	p := mkPaths(t)
	if err := EnsureChatDirs(p, "alice", false, nil); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := EnsureChatDirs(p, "alice", false, nil); err != nil {
		t.Fatalf("second: %v", err)
	}

	for _, d := range []string{
		filepath.Join(p.ChatsDir, "alice"),
		filepath.Join(p.ChatsDir, "alice", "memory"),
		filepath.Join(p.DataDir, "ipc", "alice"),
		filepath.Join(p.DataDir, "sessions", "alice", ".claude"),
	} {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("missing dir %q: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q exists but is not a directory", d)
		}
	}

	info, err := os.Stat(p.EmptyFile)
	if err != nil {
		t.Errorf("empty-env file: %v", err)
	} else if info.Size() != 0 {
		t.Errorf("empty-env size = %d, want 0", info.Size())
	}
}

func TestEnsureChatDirsRejectsEmptyChat(t *testing.T) {
	p := mkPaths(t)
	if err := EnsureChatDirs(p, "", false, nil); err == nil {
		t.Error("want error for empty chat folder")
	}
}

func TestEnsureEmptyFileRejectsNonEmpty(t *testing.T) {
	p := mkPaths(t)
	// Write a non-empty file at EmptyFile and verify EnsureChatDirs
	// refuses rather than clobbering operator-supplied content.
	if err := os.MkdirAll(filepath.Dir(p.EmptyFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.EmptyFile, []byte("surprise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChatDirs(p, "alice", false, nil); err == nil {
		t.Error("want error when EmptyFile exists with content")
	}
}

func TestBuildMountsOwner(t *testing.T) {
	p := mkPaths(t)
	if err := EnsureChatDirs(p, "owner", true, nil); err != nil {
		t.Fatalf("EnsureChatDirs: %v", err)
	}
	// The .env mask mount is only emitted when RepoRoot/.env actually
	// exists on the host (Docker can't create a mountpoint inside a
	// RO bind). Touch a fake .env so this test exercises the
	// "8 mounts including the mask" branch.
	if err := os.WriteFile(filepath.Join(p.RepoRoot, ".env"), []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, err := BuildMounts(p, "owner", TierOwner, nil)
	if err != nil {
		t.Fatalf("BuildMounts: %v", err)
	}

	// Owner should have 3 owner-only mounts + 5 baseline = 8.
	if len(ms) != 8 {
		t.Errorf("len(mounts) = %d, want 8\n%#v", len(ms), ms)
	}

	want := map[string]struct {
		source   string // substring match; full path is temp-dir dependent
		readOnly bool
	}{
		"/workspace/project":              {source: "repo", readOnly: true},
		"/workspace/project/.env":         {source: "empty-env", readOnly: true},
		"/workspace/store/store.sqlite":   {source: "store.sqlite", readOnly: false},
		"/workspace/chat":                 {source: "chats/owner", readOnly: false},
		"/workspace/memory":               {source: "memory", readOnly: false},
		"/workspace/ipc":                  {source: "ipc/owner", readOnly: false},
		"/home/node/.claude":              {source: "sessions/owner/.claude", readOnly: false},
		"/home/node/.claude/skills":       {source: "container/skills", readOnly: true},
	}
	seen := make(map[string]bool)
	for _, m := range ms {
		w, ok := want[m.Target]
		if !ok {
			t.Errorf("unexpected target %q", m.Target)
			continue
		}
		seen[m.Target] = true
		if m.ReadOnly != w.readOnly {
			t.Errorf("target %q: ReadOnly = %v, want %v", m.Target, m.ReadOnly, w.readOnly)
		}
		if !contains(m.Source, w.source) {
			t.Errorf("target %q: source %q does not contain %q", m.Target, m.Source, w.source)
		}
	}
	for target := range want {
		if !seen[target] {
			t.Errorf("missing target %q", target)
		}
	}
}

func TestBuildMountsOwnerSkipsEnvMaskWhenAbsent(t *testing.T) {
	p := mkPaths(t)
	if err := EnsureChatDirs(p, "owner", true, nil); err != nil {
		t.Fatalf("EnsureChatDirs: %v", err)
	}
	// No .env in RepoRoot — the mask mount must be skipped because
	// Docker cannot create a mountpoint inside a RO bind mount when
	// the target file does not already exist.
	ms, err := BuildMounts(p, "owner", TierOwner, nil)
	if err != nil {
		t.Fatalf("BuildMounts: %v", err)
	}
	// 8 - 1 (no mask) = 7 mounts.
	if len(ms) != 7 {
		t.Errorf("len(mounts) = %d, want 7 (.env mask should have been skipped)", len(ms))
	}
	for _, m := range ms {
		if m.Target == "/workspace/project/.env" {
			t.Errorf(".env mask was emitted despite missing RepoRoot/.env: %+v", m)
		}
	}
}

func TestBuildMountsRegistered(t *testing.T) {
	p := mkPaths(t)
	if err := EnsureChatDirs(p, "alice", false, nil); err != nil {
		t.Fatalf("EnsureChatDirs: %v", err)
	}
	ms, err := BuildMounts(p, "alice", TierRegistered, nil)
	if err != nil {
		t.Fatalf("BuildMounts: %v", err)
	}

	// Registered (non-owner) = 5 baseline mounts, no project/.env/store.
	if len(ms) != 5 {
		t.Errorf("len(mounts) = %d, want 5\n%#v", len(ms), ms)
	}
	for _, m := range ms {
		if m.Target == "/workspace/project" || m.Target == "/workspace/store/store.sqlite" || m.Target == "/workspace/project/.env" {
			t.Errorf("non-owner chat must not see owner-only target %q", m.Target)
		}
	}
}

func TestBuildMountsAllowlistExtras(t *testing.T) {
	p := mkPaths(t)
	if err := EnsureChatDirs(p, "alice", false, nil); err != nil {
		t.Fatalf("EnsureChatDirs: %v", err)
	}

	// Create two real directories outside the gopod tree to use as
	// host_path targets.
	codeDir := filepath.Join(t.TempDir(), "code")
	dlDir := filepath.Join(t.TempDir(), "downloads")
	for _, d := range []string{codeDir, dlDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	truth := true
	falsy := false
	al := &mountsec.Allowlist{
		Version: 1,
		ExtraMounts: []mountsec.ExtraMount{
			{
				Name:             "code",
				HostPath:         codeDir,
				ContainerPath:    "/workspace/extra/code",
				Mode:             "ro",
				NonOwnerReadOnly: &truth,
				AllowedChats:     []string{"alice"},
			},
			{
				Name:             "downloads",
				HostPath:         dlDir,
				ContainerPath:    "/workspace/extra/downloads",
				Mode:             "rw",
				NonOwnerReadOnly: &falsy, // non-owners may write too
				AllowedChats:     []string{"*"},
			},
		},
	}
	if err := al.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ownerMounts, err := BuildMounts(p, "owner", TierOwner, al)
	if err != nil {
		t.Fatalf("owner BuildMounts: %v", err)
	}
	// Owner: owner doesn't appear in "code" allowed_chats, only sees
	// downloads; owner picks the entry mode directly (rw).
	var foundDl, foundCode bool
	for _, m := range ownerMounts {
		if m.Target == "/workspace/extra/downloads" {
			foundDl = true
			if m.ReadOnly {
				t.Error("owner downloads mount should be RW (entry mode rw, owner ignores NonOwnerReadOnly)")
			}
		}
		if m.Target == "/workspace/extra/code" {
			foundCode = true
			t.Error("owner should not see /workspace/extra/code (not in allowed_chats)")
		}
	}
	if !foundDl {
		t.Error("owner missing /workspace/extra/downloads")
	}
	_ = foundCode

	aliceMounts, err := BuildMounts(p, "alice", TierRegistered, al)
	if err != nil {
		t.Fatalf("alice BuildMounts: %v", err)
	}
	var aliceCode, aliceDl bool
	for _, m := range aliceMounts {
		if m.Target == "/workspace/extra/code" {
			aliceCode = true
			if !m.ReadOnly {
				t.Error("alice code mount should be RO (ro entry + non_owner_read_only=true)")
			}
		}
		if m.Target == "/workspace/extra/downloads" {
			aliceDl = true
			if m.ReadOnly {
				t.Error("alice downloads mount should be RW (rw entry + non_owner_read_only=false)")
			}
		}
	}
	if !aliceCode {
		t.Error("alice missing /workspace/extra/code")
	}
	if !aliceDl {
		t.Error("alice missing /workspace/extra/downloads")
	}
}

func TestBuildMountsRejectsBadPaths(t *testing.T) {
	var p Paths
	if _, err := BuildMounts(p, "alice", TierOwner, nil); err == nil {
		t.Error("empty Paths: want error")
	}

	good := mkPaths(t)
	if _, err := BuildMounts(good, "", TierOwner, nil); err == nil {
		t.Error("empty chatFolder: want error")
	}
}

// contains is a tiny substring helper to avoid strings import bloat in
// this test file (plenty of other deps already).
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
