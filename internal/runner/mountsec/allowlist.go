// Package mountsec validates the gopod mount allowlist file and the
// extra mounts it contains.
//
// This is the security boundary between "what the operator wrote in JSON"
// and "what gopod passes to docker run". Every rule in this package is
// motivated by a threat in docs/ISOLATION.md §1, and every rejection here
// is preferable to a runtime container compromise.
//
// The package has zero Docker SDK dependencies on purpose, so the
// security-critical validator can be unit-tested in complete isolation
// using t.TempDir + os.Symlink.
package mountsec

// Allowlist is the on-disk schema of ${GOPOD_DATA_DIR}/mount-allowlist.json.
//
// Version is the schema version. v1 is the only version this build
// understands; loading a newer version is a hard error so gopod never
// silently ignores fields a future version added.
type Allowlist struct {
	Version     int          `json:"version"`
	ExtraMounts []ExtraMount `json:"extra_mounts"`
}

// ExtraMount is one entry in the allowlist. Field meanings mirror
// ISOLATION.md §4.2 exactly.
type ExtraMount struct {
	// Name is the operator-facing identifier ("code", "downloads", etc).
	// Logged with the mount, used by future /mounts subcommands.
	Name string `json:"name"`

	// HostPath is the absolute path on the host. Must exist, must be a
	// directory, must not contain `..`, must resolve via filepath.EvalSymlinks
	// to a path that is itself absolute and not blocked.
	HostPath string `json:"host_path"`

	// ContainerPath is where it appears inside the agent container. Must
	// start with "/workspace/extra/", must not contain `..`, must be unique
	// within the allowlist.
	ContainerPath string `json:"container_path"`

	// Mode is "ro" or "rw". Owner chats get this mode unconditionally;
	// non-owner chats get it filtered through NonOwnerReadOnly.
	Mode string `json:"mode"`

	// NonOwnerReadOnly is a tri-state because the JSON spec says default
	// is true. Pointer distinguishes "absent" (default true) from "set
	// to false" (operator explicitly opted in to RW for non-owners).
	NonOwnerReadOnly *bool `json:"non_owner_read_only,omitempty"`

	// AllowedChats lists the chat folder names permitted to mount this
	// entry. ["*"] means any registered chat. [] (empty) means owner-only.
	AllowedChats []string `json:"allowed_chats"`
}

// EffectiveNonOwnerReadOnly returns the effective NonOwnerReadOnly flag
// for an entry, applying the documented default (true) when the operator
// did not set the field.
func (m ExtraMount) EffectiveNonOwnerReadOnly() bool {
	if m.NonOwnerReadOnly == nil {
		return true
	}
	return *m.NonOwnerReadOnly
}

// EmptyAllowlist returns an Allowlist with no extra mounts. Used by Load
// when the file does not exist on disk — no allowlist is a valid state,
// it just means gopod will only construct standard mounts and never
// any extras.
func EmptyAllowlist() *Allowlist {
	return &Allowlist{Version: SupportedVersion, ExtraMounts: nil}
}

// SupportedVersion is the highest schema version this build understands.
const SupportedVersion = 1
