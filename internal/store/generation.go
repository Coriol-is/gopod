package store

// Generated-artifact ledger (CP-003 PR 5).
//
// A remote generation (text to speech) can be billed even when its result is
// never seen: a timeout, a dropped connection or a crash after the request left
// the process. The ledger is the host's memory of "a paid call for this intent
// was started", written BEFORE the call, so that nothing can start the same paid
// call twice by accident. One row per (namespace, epoch, kind, key):
//
//	generating  -> ready                 the bytes are a ready, hashed artifact
//	generating  -> failed                an explicit refusal proved nothing was done
//	generating  -> preparation_unknown   outcome unknowable (timeout, crash, partial write)
//
// A ready voice is uploaded at most once by the direct (pre-outbox) path: the
// upload is claimed before the API call (none -> claimed) and finished as sent,
// refused or unknown, so a retried turn or a crash never sends the same audio
// twice. A claim without an outcome is unknown, never "not sent".
//
// Every state except an absent row blocks a second call for the same key. A
// caller that wants another attempt after failed or preparation_unknown must
// present a new key, which is an explicit new intent. No SQL transaction is held
// across the backend call: callers use Begin, call, then one terminal write.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Generation kinds.
const GenerationVoice = "voice"

// Upload states of a ready generation on the direct path.
const (
	UploadNone    = "none"
	UploadClaimed = "claimed"
	UploadSent    = "sent"
	UploadRefused = "refused"
	UploadUnknown = "unknown"
)

// Generation states.
const (
	GenerationGenerating = "generating"
	GenerationReady      = "ready"
	GenerationFailed     = "failed"
	GenerationUnknown    = "preparation_unknown"
)

var (
	// ErrGenerationState is a terminal write on a row that is not generating.
	ErrGenerationState = errors.New("store: generation is not in progress")
	// ErrGenerationNotFound is an unknown generation id.
	ErrGenerationNotFound = errors.New("store: generation not found")
	// ErrUploadClaimed is a second claim of the same ready generation.
	ErrUploadClaimed = errors.New("store: generation upload already claimed")
	// ErrInvalidGeneration is a malformed kind, key, backend or reason.
	ErrInvalidGeneration = errors.New("store: invalid generation")
)

var generationReason = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

const maxGenerationKeyBytes = 255

// Generation is one ledger row. Key is an opaque host-derived intent key and
// never contains the generated text or a credential.
type Generation struct {
	ID             string
	NamespaceID    string
	NamespaceEpoch int64
	Kind           string
	Key            string
	Backend        string
	State          string
	ArtifactID     string
	ReasonCode     string
	UploadState    string
	CreatedAt      int64
	UpdatedAt      int64
}

var generationSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS generated_preparations (
 generation_id TEXT PRIMARY KEY,
 namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('voice')),
 gen_key TEXT NOT NULL,
 backend TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('generating','ready','failed','preparation_unknown')),
 artifact_id TEXT NOT NULL DEFAULT '',
 reason_code TEXT NOT NULL DEFAULT '',
 upload_state TEXT NOT NULL DEFAULT 'none' CHECK(upload_state IN ('none','claimed','sent','refused','unknown')),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(namespace_id, namespace_epoch, kind, gen_key))`,
	`CREATE INDEX IF NOT EXISTS idx_generated_preparations_state ON generated_preparations(state)`,
	// The identity of an intent never changes, and a finished row never reopens:
	// a buggy caller cannot turn an unknown outcome back into a fresh attempt.
	`CREATE TRIGGER IF NOT EXISTS generated_preparations_identity BEFORE UPDATE OF
 generation_id, namespace_id, namespace_epoch, kind, gen_key, backend, created_at ON generated_preparations
 BEGIN SELECT RAISE(ABORT, 'generation identity is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS generated_preparations_terminal BEFORE UPDATE OF state, artifact_id, reason_code ON generated_preparations
 WHEN OLD.state <> 'generating'
 BEGIN SELECT RAISE(ABORT, 'generation outcome is final'); END`,
}

func (s *Store) applyGenerationSchema(ctx context.Context) error {
	for i, stmt := range generationSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("generation statement %d: %w\n--- sql ---\n%s", i, err, stmt)
		}
	}
	return nil
}

const generationSelect = `SELECT generation_id,namespace_id,namespace_epoch,kind,gen_key,backend,state,artifact_id,reason_code,upload_state,created_at,updated_at FROM generated_preparations`

func scanGeneration(row interface{ Scan(...any) error }) (Generation, error) {
	var g Generation
	err := row.Scan(&g.ID, &g.NamespaceID, &g.NamespaceEpoch, &g.Kind, &g.Key, &g.Backend, &g.State, &g.ArtifactID, &g.ReasonCode, &g.UploadState, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

// BeginGeneration records the intent to start one paid generation and returns
// created=true only to the first caller of a key. Any other caller gets the
// existing row, in whatever state it is, and must not call the backend.
// The scope is revalidated in the same transaction. No backend work happens here.
func (s *Store) BeginGeneration(ctx context.Context, sc ScopeRef, kind, key, backend string) (Generation, bool, error) {
	if kind != GenerationVoice || key == "" || len(key) > maxGenerationKeyBytes || backend == "" || len(backend) > 64 {
		return Generation{}, false, ErrInvalidGeneration
	}
	var out Generation
	var created bool
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		row := q.QueryRowContext(ctx, generationSelect+` WHERE namespace_id=? AND namespace_epoch=? AND kind=? AND gen_key=?`, sc.NamespaceID, sc.NamespaceEpoch, kind, key)
		g, err := scanGeneration(row)
		if err == nil {
			out = g
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if _, err = q.ExecContext(ctx, `INSERT INTO generated_preparations(generation_id,namespace_id,namespace_epoch,kind,gen_key,backend,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'generating',?,?)`,
			id, sc.NamespaceID, sc.NamespaceEpoch, kind, key, backend, now, now); err != nil {
			return fmt.Errorf("store: insert generation: %w", err)
		}
		out = Generation{ID: id, NamespaceID: sc.NamespaceID, NamespaceEpoch: sc.NamespaceEpoch, Kind: kind, Key: key,
			Backend: backend, State: GenerationGenerating, UploadState: UploadNone, CreatedAt: now, UpdatedAt: now}
		created = true
		return nil
	})
	if err != nil {
		return Generation{}, false, err
	}
	return out, created, nil
}

// GetGeneration returns one ledger row.
func (s *Store) GetGeneration(ctx context.Context, id string) (Generation, error) {
	g, err := scanGeneration(s.taskDB(ctx).QueryRowContext(ctx, generationSelect+` WHERE generation_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Generation{}, ErrGenerationNotFound
	}
	return g, err
}

// CompleteGeneration records that the generated bytes are the ready artifact.
func (s *Store) CompleteGeneration(ctx context.Context, id, artifactID string) error {
	if artifactID == "" {
		return ErrInvalidGeneration
	}
	return s.finishGeneration(ctx, id, GenerationReady, artifactID, "")
}

// FailGeneration records an explicit refusal that proves the backend did no
// work and billed nothing. An ambiguous outcome must use MarkGenerationUnknown.
func (s *Store) FailGeneration(ctx context.Context, id, reason string) error {
	return s.finishGeneration(ctx, id, GenerationFailed, "", reason)
}

// MarkGenerationUnknown records that the result and the charge are unknowable.
func (s *Store) MarkGenerationUnknown(ctx context.Context, id, reason string) error {
	return s.finishGeneration(ctx, id, GenerationUnknown, "", reason)
}

func (s *Store) finishGeneration(ctx context.Context, id, state, artifactID, reason string) error {
	if state != GenerationReady && !generationReason.MatchString(reason) {
		return fmt.Errorf("%w: reason", ErrInvalidGeneration)
	}
	res, err := s.taskDB(ctx).ExecContext(ctx, `UPDATE generated_preparations SET state=?,artifact_id=?,reason_code=?,updated_at=? WHERE generation_id=? AND state='generating'`,
		state, artifactID, reason, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := s.GetGeneration(ctx, id); gerr != nil {
			return gerr
		}
		return ErrGenerationState
	}
	return nil
}

// ClaimGenerationUpload atomically takes the single direct upload of a ready
// generation. Only the first caller gets nil; every later caller gets
// ErrUploadClaimed and must not upload. Claim before the API call.
func (s *Store) ClaimGenerationUpload(ctx context.Context, id string) error {
	res, err := s.taskDB(ctx).ExecContext(ctx, `UPDATE generated_preparations SET upload_state='claimed',updated_at=? WHERE generation_id=? AND state='ready' AND upload_state='none'`,
		time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		g, gerr := s.GetGeneration(ctx, id)
		if gerr != nil {
			return gerr
		}
		if g.State != GenerationReady {
			return ErrGenerationState
		}
		return ErrUploadClaimed
	}
	return nil
}

// ReleaseGenerationUpload returns a claim to none. It is only for a failure
// that provably happened before the Telegram API call (the clip could not be
// read, the connection was never made), so a retry may still send the clip that
// was already paid for. Anything that may have reached the API must finish as
// unknown instead.
func (s *Store) ReleaseGenerationUpload(ctx context.Context, id string) error {
	res, err := s.taskDB(ctx).ExecContext(ctx, `UPDATE generated_preparations SET upload_state='none',updated_at=? WHERE generation_id=? AND upload_state='claimed'`,
		time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGenerationState
	}
	return nil
}

// FinishGenerationUpload records what the one claimed upload proved: sent
// (acknowledged), refused (an explicit refusal) or unknown (anything else).
func (s *Store) FinishGenerationUpload(ctx context.Context, id, outcome string) error {
	if outcome != UploadSent && outcome != UploadRefused && outcome != UploadUnknown {
		return ErrInvalidGeneration
	}
	res, err := s.taskDB(ctx).ExecContext(ctx, `UPDATE generated_preparations SET upload_state=?,updated_at=? WHERE generation_id=? AND upload_state='claimed'`,
		outcome, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGenerationState
	}
	return nil
}

// SweepGenerations turns every generation still in progress, and every upload
// claimed without an outcome, that is older than olderThan into unknown. It
// never starts a call and never deletes a row.
func (s *Store) SweepGenerations(ctx context.Context, olderThan time.Duration) (int, error) {
	return s.SweepGenerationsBefore(ctx, time.Now().Add(-olderThan))
}

// SweepGenerationsBefore is the restart sweep: with cutoff set to the start of
// this process it records an unknown outcome for rows an earlier process left
// in progress, and leaves every row this process created (strictly after the
// cutoff) alone, so a live generation after a restart is never flipped.
func (s *Store) SweepGenerationsBefore(ctx context.Context, cutoff time.Time) (int, error) {
	now := time.Now().UnixMilli()
	c := cutoff.UnixMilli()
	res, err := s.taskDB(ctx).ExecContext(ctx, `UPDATE generated_preparations SET state='preparation_unknown',reason_code='interrupted',updated_at=? WHERE state='generating' AND created_at<?`, now, c)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	res, err = s.taskDB(ctx).ExecContext(ctx, `UPDATE generated_preparations SET upload_state='unknown',updated_at=? WHERE upload_state='claimed' AND updated_at<?`, now, c)
	if err != nil {
		return int(n), err
	}
	m, _ := res.RowsAffected()
	return int(n + m), nil
}

// DefaultGenerationRetention is how long a finished ledger row is kept. A key is
// derived from the turn and message ids plus a digest of the text, and turns are
// never replayed after days, so 30 days is far past any replay of an old key.
// A failed or unknown row guards against a second paid call for its key, so it
// is kept exactly as long as a ready one.
const DefaultGenerationRetention = 30 * 24 * time.Hour

// PurgeGenerations deletes finished rows (ready, failed or unknown, with no
// upload in flight) last updated before now-olderThan, at most limit per call.
func (s *Store) PurgeGenerations(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	if olderThan <= 0 || limit <= 0 {
		return 0, ErrInvalidGeneration
	}
	cut := time.Now().Add(-olderThan).UnixMilli()
	res, err := s.taskDB(ctx).ExecContext(ctx, `DELETE FROM generated_preparations WHERE generation_id IN (
 SELECT generation_id FROM generated_preparations WHERE state IN ('ready','failed','preparation_unknown')
 AND upload_state <> 'claimed' AND updated_at<? ORDER BY updated_at LIMIT ?)`, cut, limit)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
