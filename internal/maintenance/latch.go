package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// LatchName is the restore-quarantine latch file inside the data directory.
const LatchName = ".restore-quarantine"

// LatchPath returns the latch path for a data directory.
func LatchPath(dataDir string) string { return filepath.Join(dataDir, LatchName) }

// LatchInfo is the content of a latch; it never holds secrets.
type LatchInfo struct {
	SetID  string `json:"set_id,omitempty"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// LatchActive reports whether a normal start must be refused. It fails closed:
// any state other than "the latch does not exist" counts as active, including
// a latch that is a symlink or a directory, or a stat error.
func LatchActive(dataDir string) (bool, LatchInfo) {
	p := LatchPath(dataDir)
	if _, err := os.Lstat(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, LatchInfo{}
		}
		return true, LatchInfo{Reason: "latch state unreadable"}
	}
	info := LatchInfo{Reason: "latch present"}
	if f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NOFOLLOW, 0); err == nil {
		_ = json.NewDecoder(f).Decode(&info)
		_ = f.Close()
	}
	return true, info
}

// SetLatch creates the latch (0600, exclusive). An existing latch is kept.
func SetLatch(dataDir, setID, reason string, now time.Time) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("maintenance: create data dir: %w", err)
	}
	f, err := os.OpenFile(LatchPath(dataDir), os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("maintenance: create latch: %w", err)
	}
	if err := json.NewEncoder(f).Encode(LatchInfo{SetID: setID, Reason: reason, At: now.UTC().Format(time.RFC3339)}); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReleaseLatch removes the latch: the explicit host-side activation step.
func ReleaseLatch(dataDir string) error {
	if err := os.Remove(LatchPath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("maintenance: release latch: %w", err)
	}
	return nil
}
