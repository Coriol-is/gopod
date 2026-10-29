// Package backup creates, lists and verifies tool-owned backup sets (CP-007).
//
// A set is a private directory holding a store snapshot, optional declared
// configuration files and (for the stopped-writer mode) the artifact blobs the
// database references, described by a manifest with per-file SHA-256 and sealed
// by a completion marker. A set only ever appears under the backup root by an
// atomic rename of a fully written staging directory, so a crash or a failure
// at any step leaves no partial set that a restore could pick up.
//
// What a set does NOT contain is recorded in the manifest (NotCovered) and
// printed by verify: it is not a filesystem, provider-session or credential
// recovery guarantee.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

// File names and format inside a set.
const (
	FormatVersion = 1
	ManifestName  = "manifest.json"
	MarkerName    = "COMPLETE"
	DBName        = "store.sqlite"
	ConfigDir     = "config"
	ArtifactDir   = "artifacts"
	StagingDir    = ".staging"
)

// Capture modes.
const (
	// ModeDatabaseOnly is an online consistent database snapshot: useful
	// protection, explicitly not complete disaster recovery.
	ModeDatabaseOnly = "database_only"
	// ModeMatchedStopped additionally requires settled owned writers (stopped
	// controller, no live agent containers) and adds declared configuration
	// files and the artifact blobs referenced by the database.
	ModeMatchedStopped = "matched_stopped"
)

// File roles.
const (
	RoleDatabase = "database"
	RoleConfig   = "config"
	RoleArtifact = "artifact"
)

// Errors reported by create and verify.
var (
	ErrUnsupportedFormat = errors.New("backup: unsupported manifest format version")
	ErrInvalidSet        = errors.New("backup: invalid backup set")
	ErrUnsafeRoot        = errors.New("backup: unsafe backup root")
	ErrQuota             = errors.New("backup: backup quota or free space exhausted")
	ErrArtifactMissing   = errors.New("backup: artifact referenced by the database has no blob")
	ErrSourceChanged     = errors.New("backup: source changed during capture")
)

var setIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-(db|ms)-[0-9a-f]{8}$`)

// FileEntry is one file in a set.
type FileEntry struct {
	Role   string `json:"role"`
	Path   string `json:"path"` // slash separated, relative to the set directory
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// DatabaseEntry describes the snapshot database.
type DatabaseEntry struct {
	FileEntry
	SQLiteSchemaVersion int64            `json:"sqlite_schema_version"`
	SchemaFingerprint   string           `json:"schema_fingerprint"`
	IntegrityOK         bool             `json:"integrity_ok"`
	Tables              map[string]int64 `json:"tables"`
}

// FenceRecord is the settlement evidence of a matched_stopped capture.
type FenceRecord struct {
	ControllerLock    bool   `json:"controller_lock_held"`
	ContainerEvidence string `json:"container_evidence"`
}

// Manifest is the private, versioned description of a set.
type Manifest struct {
	FormatVersion int                    `json:"format_version"`
	SetID         string                 `json:"set_id"`
	Mode          string                 `json:"mode"`
	StartedAt     string                 `json:"started_at"`
	CompletedAt   string                 `json:"completed_at"`
	GopodVersion  string                 `json:"gopod_version"`
	Database      DatabaseEntry          `json:"database"`
	Files         []FileEntry            `json:"files"`
	Artifacts     []store.BackupArtifact `json:"artifacts"`
	Work          store.WorkEvidence     `json:"work_evidence"`
	Fence         *FenceRecord           `json:"fence,omitempty"`
	Coverage      []string               `json:"coverage"`
	NotCovered    []string               `json:"not_covered"`
}

// notCovered lists what a set of the given mode does not contain.
func notCovered(mode string) []string {
	nc := []string{
		"chats_dir_workspaces", "provider_session_homes", "provider_auth_homes",
		"ipc_pending_files", "container_images_and_source", "env_secrets_unless_declared",
	}
	if mode == ModeDatabaseOnly {
		nc = append([]string{"artifact_blobs", "declared_config_files"}, nc...)
	}
	return nc
}

func coverage(mode string) []string {
	if mode == ModeMatchedStopped {
		return []string{"database", "declared_config_files", "artifact_blobs"}
	}
	return []string{"database"}
}

// marshalManifest renders the canonical manifest bytes and their marker.
func marshalManifest(m Manifest) (manifest []byte, marker []byte, err error) {
	manifest, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("backup: encode manifest: %w", err)
	}
	manifest = append(manifest, '\n')
	return manifest, markerFor(manifest), nil
}

func markerFor(manifest []byte) []byte {
	sum := sha256.Sum256(manifest)
	return []byte("sha256:" + hex.EncodeToString(sum[:]) + "\n")
}

// parseManifest checks the format version before strict decoding so a newer
// set is reported as unsupported rather than as a malformed document.
func parseManifest(raw []byte) (Manifest, error) {
	var head struct {
		FormatVersion int `json:"format_version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest is not valid JSON", ErrInvalidSet)
	}
	if head.FormatVersion > FormatVersion {
		return Manifest{}, fmt.Errorf("%w: %d (this binary understands %d)", ErrUnsupportedFormat, head.FormatVersion, FormatVersion)
	}
	if head.FormatVersion < 1 {
		return Manifest{}, fmt.Errorf("%w: missing format version", ErrInvalidSet)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest: %v", ErrInvalidSet, err)
	}
	return m, nil
}

// validRelPath accepts only clean, relative, slash-separated paths that stay
// inside the set.
func validRelPath(p string) bool {
	return p != "" && fs.ValidPath(p) && p != "." && !strings.Contains(p, `\`) && !strings.HasPrefix(p, "/")
}
