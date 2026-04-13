package mountsec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// boolPtr is a tiny helper for setting *bool fields in test fixtures.
func boolPtr(b bool) *bool { return &b }

// writeAllowlist marshals an Allowlist to t.TempDir/allowlist.json with
// the given file mode and returns the path. Tests use it to round-trip
// fixtures through Load.
func writeAllowlist(t *testing.T, a *Allowlist, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mount-allowlist.json")
	body, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("write: %v", err)
	}
	// os.WriteFile may have applied umask; force the mode explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

// realDir creates a real directory under t.TempDir to use as a host_path.
func realDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "mount-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}

// ---------------------------------------------------------------------
// Allowlist file lifecycle: missing / present / wrong perms / wrong type
// ---------------------------------------------------------------------

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	a, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a == nil || len(a.ExtraMounts) != 0 {
		t.Errorf("missing file should yield EmptyAllowlist, got %#v", a)
	}
}

func TestLoadRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("Load: want error when path is a directory")
	}
}

func TestLoadRejectsWidePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not enforced on windows")
	}
	a := EmptyAllowlist()
	path := writeAllowlist(t, a, 0o644)
	if _, err := Load(path); err == nil {
		t.Fatalf("Load: want permission error for 0644, got nil")
	}
}

func TestLoadAcceptsTightPermissions(t *testing.T) {
	a := EmptyAllowlist()
	path := writeAllowlist(t, a, 0o600)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load 0600: %v", err)
	}
}

func TestLoadRejectsUnknownTopLevelFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"extra_mounts":[],"surprise":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for unknown JSON field")
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	a := &Allowlist{Version: 99}
	path := writeAllowlist(t, a, 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for unsupported version")
	}
}

// ---------------------------------------------------------------------
// host_path validation
// ---------------------------------------------------------------------

func TestValidateRejectsRelativeHostPath(t *testing.T) {
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "rel",
			HostPath:      "relative/path",
			ContainerPath: "/workspace/extra/rel",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for relative host_path")
	}
}

func TestValidateRejectsTraversal(t *testing.T) {
	host := realDir(t)
	// String-concatenate the .. into the path; filepath.Join would
	// collapse it via filepath.Clean before validHostPath ever sees it.
	bad := host + "/../" + filepath.Base(host)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "trav",
			HostPath:      bad,
			ContainerPath: "/workspace/extra/trav",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for host_path with .. inside")
	}
}

func TestValidateRejectsNonexistentHostPath(t *testing.T) {
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "ghost",
			HostPath:      "/definitely/does/not/exist/gopod-test",
			ContainerPath: "/workspace/extra/ghost",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for nonexistent host_path")
	}
}

func TestValidateRejectsRegularFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(file)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "afile",
			HostPath:      resolved,
			ContainerPath: "/workspace/extra/afile",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for regular-file host_path")
	}
}

func TestValidateAcceptsLegitimateDirectory(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "ok",
			HostPath:      host,
			ContainerPath: "/workspace/extra/ok",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateSymlinkResolvesToBlocked(t *testing.T) {
	dir := t.TempDir()
	// Create a symlink in dir that points at /etc/sudoers (which is in
	// blockedPatterns). Validate must reject the entry.
	link := filepath.Join(dir, "trick")
	if err := os.Symlink("/etc/sudoers", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	// EvalSymlinks may fail if /etc/sudoers doesn't exist (rare on dev
	// laptops). Skip the test in that case rather than report a false
	// positive.
	if _, err := filepath.EvalSymlinks(link); err != nil {
		t.Skipf("/etc/sudoers not present, can't probe: %v", err)
	}
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "trick",
			HostPath:      link,
			ContainerPath: "/workspace/extra/trick",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	err := a.Validate()
	if err == nil {
		t.Fatal("want error for symlink resolving to blocked path")
	}
	// We expect EITHER "is not a directory" (the file) OR a blocked-path
	// error — both are correct outcomes for the symlink-to-secret attack.
	// Just confirm Validate refused.
}

// ---------------------------------------------------------------------
// container_path validation
// ---------------------------------------------------------------------

func TestValidateRejectsContainerPathOutsideExtra(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "rogue",
			HostPath:      host,
			ContainerPath: "/etc/passwd",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for container_path outside /workspace/extra/")
	}
}

func TestValidateRejectsContainerPathTraversal(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name:          "rogue",
			HostPath:      host,
			ContainerPath: "/workspace/extra/../etc",
			Mode:          "ro",
			AllowedChats:  []string{"*"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for container_path with ..")
	}
}

func TestValidateRejectsContainerPathCollision(t *testing.T) {
	host1 := realDir(t)
	host2 := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{
			{Name: "a", HostPath: host1, ContainerPath: "/workspace/extra/shared", Mode: "ro", AllowedChats: []string{"*"}},
			{Name: "b", HostPath: host2, ContainerPath: "/workspace/extra/shared", Mode: "ro", AllowedChats: []string{"*"}},
		},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for two entries sharing the same container_path")
	}
}

// ---------------------------------------------------------------------
// mode + allowed_chats validation
// ---------------------------------------------------------------------

func TestValidateRejectsBadMode(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name: "x", HostPath: host, ContainerPath: "/workspace/extra/x",
			Mode: "rwx", AllowedChats: []string{"*"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for mode rwx")
	}
}

func TestValidateRejectsInvalidChatFolderName(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name: "x", HostPath: host, ContainerPath: "/workspace/extra/x",
			Mode: "ro", AllowedChats: []string{"../../etc"},
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for chat folder containing path traversal")
	}
}

func TestValidateAcceptsStarWildcard(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name: "x", HostPath: host, ContainerPath: "/workspace/extra/x",
			Mode: "ro", AllowedChats: []string{"*"},
		}},
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsMissingAllowedChats(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{{
			Name: "x", HostPath: host, ContainerPath: "/workspace/extra/x",
			Mode: "ro", // AllowedChats: nil
		}},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("want error for missing allowed_chats (nil)")
	}
}

// ---------------------------------------------------------------------
// blocked-pattern matcher (unit-tested in isolation)
// ---------------------------------------------------------------------

func TestIsBlockedSamples(t *testing.T) {
	tests := []struct {
		path  string
		block bool
	}{
		{"/Users/me/.ssh/id_rsa", true},
		{"/Users/me/.ssh", true},
		{"/Users/me/.aws/credentials", true},
		{"/Users/me/.gnupg", true},
		{"/Users/me/.docker/config.json", true},
		{"/Users/me/.config/gopod/state.json", true},
		{"/etc/shadow", true},
		{"/etc/sudoers", true},
		{"/etc/sudoers.d/foo", true},
		{"/proc/self/mem", true},
		{"/sys/kernel", true},
		{"/dev/null", true},
		{"/Users/me/Downloads/notes.md", false},
		{"/Users/me/code/gopod/main.go", false},
		{"/var/log/system.log", false},
		{"/Users/me/.envfile-but-not-env", false},
	}
	for _, tc := range tests {
		got := IsBlocked(tc.path)
		if got != tc.block {
			t.Errorf("IsBlocked(%q) = %v, want %v", tc.path, got, tc.block)
		}
	}
}

func TestIsBlockedCaseInsensitiveOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only test")
	}
	if !IsBlocked("/Users/me/.SSH/id_rsa") {
		t.Error("expected case-insensitive match for /Users/me/.SSH/id_rsa on darwin")
	}
}

// ---------------------------------------------------------------------
// EffectiveNonOwnerReadOnly tri-state
// ---------------------------------------------------------------------

func TestEffectiveNonOwnerReadOnly(t *testing.T) {
	cases := []struct {
		name string
		in   *bool
		want bool
	}{
		{"unset", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := ExtraMount{NonOwnerReadOnly: c.in}
			if got := m.EffectiveNonOwnerReadOnly(); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Round-trip Load → Validate on a realistic fixture
// ---------------------------------------------------------------------

func TestLoadRealisticAllowlist(t *testing.T) {
	host := realDir(t)
	a := &Allowlist{
		Version: 1,
		ExtraMounts: []ExtraMount{
			{
				Name:             "code",
				HostPath:         host,
				ContainerPath:    "/workspace/extra/code",
				Mode:             "ro",
				NonOwnerReadOnly: boolPtr(true),
				AllowedChats:     []string{"myproject"},
			},
		},
	}
	path := writeAllowlist(t, a, 0o600)

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.ExtraMounts) != 1 {
		t.Fatalf("got %d mounts, want 1", len(loaded.ExtraMounts))
	}
	if loaded.ExtraMounts[0].Name != "code" {
		t.Errorf("name = %q", loaded.ExtraMounts[0].Name)
	}
	if !strings.HasPrefix(loaded.ExtraMounts[0].ContainerPath, "/workspace/extra/") {
		t.Errorf("container_path = %q", loaded.ExtraMounts[0].ContainerPath)
	}
}
