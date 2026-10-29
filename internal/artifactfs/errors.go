//go:build linux || darwin

// Package artifactfs is the host-side filesystem half of CP-003 immutable
// artifact delivery: a rooted, every-component no-follow reader for agent-owned
// source files, and a host-private blob store for verified snapshots. It holds
// no SQL and makes no network calls.
//
// Limits stated plainly: path policy detects links, special files and changes
// during copy; it cannot detect a secret copied into an innocent file, and it
// is not a container boundary.
package artifactfs

import "errors"

var (
	// ErrUnsafePath is a traversal, a symlink in any component, or a malformed path.
	ErrUnsafePath = errors.New("artifactfs: unsafe path")
	// ErrNotRegular is a FIFO, socket, device or directory in place of a file.
	ErrNotRegular = errors.New("artifactfs: not a regular file")
	// ErrHardlinked is a file whose link count is not exactly one.
	ErrHardlinked = errors.New("artifactfs: hard-linked file")
	// ErrTooLarge is a file over the byte bound (checked while copying too).
	ErrTooLarge = errors.New("artifactfs: file exceeds the byte bound")
	// ErrSourceChanged is a source that changed between open and the end of the copy.
	ErrSourceChanged = errors.New("artifactfs: source changed during preparation")
	// ErrNotFound is a missing source or blob.
	ErrNotFound = errors.New("artifactfs: not found")
	// ErrCorrupt is a stored blob whose size, link count or SHA-256 differs from the inventory.
	ErrCorrupt = errors.New("artifactfs: blob does not match its inventory")
	// ErrStorageFull is ENOSPC or the free-space floor.
	ErrStorageFull = errors.New("artifactfs: storage full")
	// ErrUnsafeRoot is a blob root that is not a private directory owned by the host user.
	ErrUnsafeRoot = errors.New("artifactfs: unsafe blob root")
	// ErrSimulatedCrash makes a fault-injection point behave like a process
	// kill: the operation returns at once and performs no cleanup. Test use only.
	ErrSimulatedCrash = errors.New("artifactfs: simulated crash")
)

// FaultFunc is a fault-injection hook called at named points. A nil result
// continues. ErrSimulatedCrash aborts without cleanup; any other error is an
// ordinary failure that is cleaned up.
type FaultFunc func(point string) error
