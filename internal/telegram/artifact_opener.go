package telegram

// Verified-snapshot artifact opener (CP-003 PR 4).
//
// The inventory row, not any agent path, decides what is uploaded. The blob is
// opened without following links, checked against the inventoried size and
// SHA-256 into a bounded buffer, and those exact bytes are handed to the single
// upload attempt. A hash mismatch, missing or corrupt blob quarantines the
// artifact and returns an error, so nothing is uploaded and the part fails with
// a sanitized reason (the transport maps any error to a refusal, never a blind
// retry).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/store"
)

const quarantineTimeout = 10 * time.Second

// ErrArtifactUnavailable is returned for any artifact that cannot be uploaded.
var ErrArtifactUnavailable = errors.New("telegram: artifact unavailable")

// ArtifactInventory is the part of the store the opener needs.
type ArtifactInventory interface {
	GetArtifact(ctx context.Context, id string) (store.Artifact, error)
	QuarantineArtifact(ctx context.Context, id, reason string) (int, error)
}

// BlobReader reads a verified blob. *artifactfs.Blobs implements it.
type BlobReader interface {
	ReadVerified(key string, size int64, sha string, maxBytes int64) ([]byte, error)
}

// SnapshotOpener implements ArtifactOpener over the inventory and blob store.
type SnapshotOpener struct {
	Inventory ArtifactInventory
	Blobs     BlobReader
	// MaxBytes bounds the buffer; it should equal the store's per-file limit.
	MaxBytes int64
	Log      *slog.Logger
}

// OpenArtifact returns the sanitized filename and a reader over the verified bytes.
func (o *SnapshotOpener) OpenArtifact(ctx context.Context, ref, namespaceID string, epoch int64) (string, io.Reader, error) {
	id, ok := store.ParseArtifactRef(ref)
	if !ok {
		return "", nil, ErrArtifactUnavailable
	}
	art, err := o.Inventory.GetArtifact(ctx, id)
	if err != nil {
		return "", nil, fmt.Errorf("%w: inventory", ErrArtifactUnavailable)
	}
	if art.NamespaceID != namespaceID || art.NamespaceEpoch != epoch || namespaceID == "" {
		// A reference can only be the delivery's own namespace and epoch.
		return "", nil, fmt.Errorf("%w: scope", ErrArtifactUnavailable)
	}
	if art.State != store.ArtifactReady {
		return "", nil, fmt.Errorf("%w: state %s", ErrArtifactUnavailable, art.State)
	}
	data, err := o.Blobs.ReadVerified(art.BlobKey, art.Size, art.SHA256, o.MaxBytes)
	if err != nil {
		reason := ""
		switch {
		case errors.Is(err, artifactfs.ErrNotFound), errors.Is(err, artifactfs.ErrPreparationMissing):
			reason = "blob_missing"
		case errors.Is(err, artifactfs.ErrCorrupt), errors.Is(err, artifactfs.ErrUnsafePath):
			reason = "hash_mismatch"
		}
		if reason != "" {
			// Quarantine on a fresh context: the attempt context may be ending.
			qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), quarantineTimeout)
			defer cancel()
			if _, qerr := o.Inventory.QuarantineArtifact(qctx, id, reason); qerr != nil && o.Log != nil {
				o.Log.Error("quarantine artifact", "artifact", id, "reason", reason, "err", qerr)
			}
			if o.Log != nil {
				o.Log.Error("artifact quarantined before upload", "artifact", id, "reason", reason)
			}
		}
		return "", nil, fmt.Errorf("%w: blob", ErrArtifactUnavailable)
	}
	return art.Filename, bytes.NewReader(data), nil
}
