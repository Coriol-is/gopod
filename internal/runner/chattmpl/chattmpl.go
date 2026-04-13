// Package chattmpl seeds new gopod chat workspaces with default
// CLAUDE.md and MEMORY.md files so the agent wakes up with identity,
// a workspace map, and memory format hints instead of an empty
// directory.
//
// Templates are go:embed-bundled into the gopod binary; there is
// nothing to copy from the host filesystem at install time.
//
// Seeding rules:
//
//   - Files are written ONLY if they do not already exist. gopod
//     never overwrites operator-edited or agent-edited content.
//   - File mode is 0644. The owner is the host operator (whatever
//     uid runs gopod); the agent inside the container reads via
//     bind mount.
//   - Templates render with a tiny Vars struct (chat folder, owner
//     flag, current date). The text/template syntax is plain Go.
package chattmpl

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"
)

//go:embed CLAUDE.md.tmpl
var claudeTmplSrc string

//go:embed MEMORY.md.tmpl
var memoryTmplSrc string

// Vars is the data passed to the templates at render time.
type Vars struct {
	// ChatFolder is the on-disk folder name (e.g. "owner", "myproject").
	ChatFolder string

	// IsOwner toggles the owner-specific blurb in CLAUDE.md.
	IsOwner bool

	// Date is the seed date in YYYY-MM-DD form. Defaulted from
	// time.Now() in Seed if zero.
	Date string
}

// Seed writes CLAUDE.md and memory/MEMORY.md into chatDir if they do
// not already exist. Idempotent. Returns the list of files actually
// created (empty when both already existed).
//
// chatDir is the host-side directory that will be bind-mounted to
// /workspace/chat in the agent container. memoryDir is its
// `memory` subdirectory.
//
// Both directories must already exist; this function does not mkdir.
// Callers (typically EnsureChatDirs in mounts.go) handle the parent
// directory creation as part of their own bootstrap.
func Seed(chatDir, memoryDir string, vars Vars) ([]string, error) {
	if vars.Date == "" {
		vars.Date = time.Now().Format("2006-01-02")
	}

	var created []string

	claudePath := filepath.Join(chatDir, "CLAUDE.md")
	if wrote, err := writeIfMissing(claudePath, claudeTmplSrc, vars); err != nil {
		return created, err
	} else if wrote {
		created = append(created, claudePath)
	}

	memoryPath := filepath.Join(memoryDir, "MEMORY.md")
	if wrote, err := writeIfMissing(memoryPath, memoryTmplSrc, vars); err != nil {
		return created, err
	} else if wrote {
		created = append(created, memoryPath)
	}

	return created, nil
}

// writeIfMissing renders src against vars and writes to path iff path
// does not already exist. Returns true if a write happened.
func writeIfMissing(path, src string, vars Vars) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil // already present, leave alone
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("chattmpl: stat %q: %w", path, err)
	}

	tmpl, err := template.New(filepath.Base(path)).Parse(src)
	if err != nil {
		return false, fmt.Errorf("chattmpl: parse %q: %w", path, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return false, fmt.Errorf("chattmpl: render %q: %w", path, err)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("chattmpl: write %q: %w", path, err)
	}
	return true, nil
}
