package mountsec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"runtime"
)

// MaxAllowlistFileMode is the most permissive mode mountsec accepts on
// the allowlist file. Anything wider (group-readable, world-anything) is
// rejected at Load time.
//
// On Windows the unix mode bits are not meaningful, so the check is
// skipped — Windows operators are responsible for ACLs themselves.
const MaxAllowlistFileMode fs.FileMode = 0o600

// chatFolderRegex is the precompiled validator for AllowedChats entries.
var chatFolderRegex = regexp.MustCompile(chatFolderPattern)

// Load reads the allowlist file at path, validates it, and returns the
// parsed Allowlist. A missing file is NOT an error: callers get back an
// EmptyAllowlist() and the rest of gopod runs with no extras.
//
// Errors from Load are intentionally fatal — gopod refuses to start
// if the file is present but malformed, has wider-than-0600 permissions,
// references blocked paths, or contains traversal. The operator must fix
// the file before gopod will boot.
func Load(path string) (*Allowlist, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return EmptyAllowlist(), nil
		}
		return nil, fmt.Errorf("mountsec: stat %q: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("mountsec: %q is a directory, not a file", path)
	}

	if runtime.GOOS != "windows" {
		// Reject anything wider than 0600. Permissive modes risk leaking
		// the operator's host_path layout to other local users.
		if mode := info.Mode().Perm(); mode&^MaxAllowlistFileMode != 0 {
			return nil, fmt.Errorf(
				"mountsec: %q has permissions %#o, want at most %#o",
				path, mode, MaxAllowlistFileMode,
			)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mountsec: open %q: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var a Allowlist
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("mountsec: parse %q: %w", path, err)
	}
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("mountsec: validate %q: %w", path, err)
	}
	return &a, nil
}

// Validate runs every per-entry rule from ISOLATION.md §4.3 against the
// loaded Allowlist. It is called automatically from Load; exposed
// publicly so tests can construct synthetic Allowlist values and
// validate them without round-tripping through JSON.
//
// On the first failure Validate returns immediately so the error names
// the offending entry — operators don't have to fix N issues at once.
func (a *Allowlist) Validate() error {
	if a == nil {
		return errors.New("nil allowlist")
	}
	if a.Version != SupportedVersion {
		return fmt.Errorf("unsupported allowlist version %d (this build understands %d)", a.Version, SupportedVersion)
	}

	seenContainerPath := make(map[string]string, len(a.ExtraMounts))
	seenName := make(map[string]bool, len(a.ExtraMounts))

	for i, m := range a.ExtraMounts {
		if m.Name == "" {
			return fmt.Errorf("extra_mounts[%d]: name is empty", i)
		}
		if seenName[m.Name] {
			return fmt.Errorf("extra_mounts[%d] (%q): duplicate name", i, m.Name)
		}
		seenName[m.Name] = true

		if _, err := validHostPath(m.HostPath); err != nil {
			return fmt.Errorf("extra_mounts[%d] (%q): %w", i, m.Name, err)
		}
		if err := validContainerPath(m.ContainerPath); err != nil {
			return fmt.Errorf("extra_mounts[%d] (%q): %w", i, m.Name, err)
		}
		if other, ok := seenContainerPath[m.ContainerPath]; ok {
			return fmt.Errorf("extra_mounts[%d] (%q): container_path %q already used by %q", i, m.Name, m.ContainerPath, other)
		}
		seenContainerPath[m.ContainerPath] = m.Name

		switch m.Mode {
		case "ro", "rw":
		case "":
			return fmt.Errorf("extra_mounts[%d] (%q): mode is empty", i, m.Name)
		default:
			return fmt.Errorf("extra_mounts[%d] (%q): mode %q must be \"ro\" or \"rw\"", i, m.Name, m.Mode)
		}

		if m.AllowedChats == nil {
			return fmt.Errorf("extra_mounts[%d] (%q): allowed_chats is missing", i, m.Name)
		}
		for j, chat := range m.AllowedChats {
			if chat == "*" {
				continue
			}
			if !chatFolderRegex.MatchString(chat) {
				return fmt.Errorf(
					"extra_mounts[%d] (%q): allowed_chats[%d] %q is not a valid chat folder name (must match %s, or be \"*\")",
					i, m.Name, j, chat, chatFolderPattern)
			}
		}
	}
	return nil
}
