package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Artifact states. Published ownership is expressed by references, never by a
// mutable agent path. Only the host mutates the inventory.
const (
	ArtifactPreparing   = "preparing"
	ArtifactReady       = "ready"
	ArtifactQuarantined = "quarantined"
	ArtifactDeleting    = "deleting"
)

// ArtifactRefPrefix prefixes the opaque reference stored in an upload part.
const ArtifactRefPrefix = "artifact:"

var (
	ErrArtifactQuotaExceeded = errors.New("store: artifact quota exceeded")
	ErrArtifactRateLimited   = errors.New("store: artifact admission rate limit")
	ErrInvalidArtifact       = errors.New("store: invalid artifact request")
	ErrPreparationExpired    = errors.New("store: artifact preparation expired")
	ErrPreparationState      = errors.New("store: artifact preparation is not active")
	ErrArtifactNotFound      = errors.New("store: artifact not found")
	ErrArtifactPinned        = errors.New("store: artifact is pinned by an unresolved reference")
)

// ArtifactRef returns the opaque part reference for an artifact id.
func ArtifactRef(artifactID string) string { return ArtifactRefPrefix + artifactID }

// ParseArtifactRef extracts the artifact id from a part reference.
func ParseArtifactRef(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, ArtifactRefPrefix)
	return id, ok && namespaceIDPattern.MatchString(id)
}

// Artifact is one inventory row.
type Artifact struct {
	ID              string
	NamespaceID     string
	NamespaceEpoch  int64
	State           string
	BlobKey         string
	SHA256          string
	Size            int64
	MediaType       string
	Filename        string
	ReasonCode      string
	CreatedAt       int64
	LastReferenceAt int64
	PreparationID   string
}

// ArtifactReservation asks for the full upper bound of a preparation up front.
type ArtifactReservation struct {
	Scope    ScopeRef
	OwnerRef string
	Files    int
	Bytes    int64
	TTL      time.Duration
}

// Preparation is the granted reservation with one preparing artifact per file.
type Preparation struct {
	ID          string
	ArtifactIDs []string
	BlobKeys    []string // relative_blob_key per artifact, same order
	Bytes       int64
	ExpiresAt   int64
}

// ArtifactFacts are the verified facts of one finished private snapshot.
type ArtifactFacts struct {
	ArtifactID string
	SHA256     string
	Size       int64
	MediaType  string
	Filename   string
}

// ReserveArtifacts reserves the whole upper bound under namespace and global
// quota before any file is read, and creates one preparing artifact per file in
// the same transaction. No filesystem work happens here.
func (s *Store) ReserveArtifacts(ctx context.Context, req ArtifactReservation) (Preparation, error) {
	lim := s.artifactLimits()
	if req.Files < 1 || req.Files > lim.MaxFiles || req.Bytes < 1 || req.Bytes > lim.MaxRequestBytes {
		return Preparation{}, fmt.Errorf("%w: %d files, %d bytes", ErrInvalidArtifact, req.Files, req.Bytes)
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultPreparationTTL
	}
	if ttl > MaxPreparationTTL {
		return Preparation{}, fmt.Errorf("%w: ttl", ErrInvalidArtifact)
	}
	now := time.Now().UnixMilli()
	var out Preparation
	_, err := s.withTx(ctx, func(q taskSQL) error {
		sc := req.Scope
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		ns, global, err := artifactUsageQ(ctx, q, sc.NamespaceID)
		if err != nil {
			return err
		}
		if ns+req.Bytes > lim.MaxNamespaceBytes || global+req.Bytes > lim.MaxGlobalBytes {
			return ErrArtifactQuotaExceeded
		}
		if err = checkArtifactRateQ(ctx, q, lim, sc.NamespaceID, req.OwnerRef, req.Files, req.Bytes, now); err != nil {
			return err
		}
		prep, err := newUUID()
		if err != nil {
			return err
		}
		out = Preparation{ID: prep, Bytes: req.Bytes, ExpiresAt: now + ttl.Milliseconds()}
		if _, err = q.ExecContext(ctx, `INSERT INTO artifact_reservations(preparation_id,namespace_id,namespace_epoch,owner_ref,bytes,file_count,state,created_at,expires_at) VALUES(?,?,?,?,?,?,'active',?,?)`,
			prep, sc.NamespaceID, sc.NamespaceEpoch, req.OwnerRef, req.Bytes, req.Files, now, out.ExpiresAt); err != nil {
			return fmt.Errorf("store: insert reservation: %w", err)
		}
		for i := 0; i < req.Files; i++ {
			id, err := newUUID()
			if err != nil {
				return err
			}
			key := prep + "/" + id + ".blob"
			if _, err = q.ExecContext(ctx, `INSERT INTO artifacts(artifact_id,namespace_id,namespace_epoch,state,relative_blob_key,created_at,preparation_id) VALUES(?,?,?,'preparing',?,?,?)`,
				id, sc.NamespaceID, sc.NamespaceEpoch, key, now, prep); err != nil {
				return fmt.Errorf("store: insert artifact: %w", err)
			}
			out.ArtifactIDs = append(out.ArtifactIDs, id)
			out.BlobKeys = append(out.BlobKeys, key)
		}
		return nil
	})
	if err != nil {
		return Preparation{}, err
	}
	return out, nil
}

// artifactUsageQ counts every reserved and stored byte. A preparation counts its
// reserved upper bound while the reservation is active and its measured sizes
// afterwards, so a finished artifact is never counted twice. Quarantined and
// deleting blobs keep counting until the blob is really gone.
func artifactUsageQ(ctx context.Context, q taskSQL, namespaceID string) (ns, global int64, err error) {
	err = q.QueryRowContext(ctx, `SELECT
 coalesce((SELECT sum(bytes) FROM artifact_reservations WHERE state='active' AND namespace_id=?),0)
 + coalesce((SELECT sum(a.size) FROM artifacts a WHERE a.namespace_id=? AND a.state IN ('ready','quarantined','deleting')
    AND NOT EXISTS (SELECT 1 FROM artifact_reservations r WHERE r.preparation_id=a.preparation_id AND r.state='active')),0),
 coalesce((SELECT sum(bytes) FROM artifact_reservations WHERE state='active'),0)
 + coalesce((SELECT sum(a.size) FROM artifacts a WHERE a.state IN ('ready','quarantined','deleting')
    AND NOT EXISTS (SELECT 1 FROM artifact_reservations r WHERE r.preparation_id=a.preparation_id AND r.state='active')),0)`,
		namespaceID, namespaceID).Scan(&ns, &global)
	if err != nil {
		return 0, 0, fmt.Errorf("store: artifact usage: %w", err)
	}
	return ns, global, nil
}

// ArtifactUsage reports reserved plus stored bytes for a namespace and globally.
func (s *Store) ArtifactUsage(ctx context.Context, namespaceID string) (ns, global int64, err error) {
	return artifactUsageQ(ctx, s.taskDB(ctx), namespaceID)
}

// checkArtifactRateQ enforces the per-run and per-namespace-per-hour caps. A
// reservation counts its reserved bytes while active and the measured sizes of
// its artifacts afterwards, so tiny files do not look like 40 MiB.
func checkArtifactRateQ(ctx context.Context, q taskSQL, lim ArtifactLimits, namespaceID, owner string, files int, bytes, now int64) error {
	const eff = `CASE WHEN r.state='active' THEN r.bytes ELSE coalesce((SELECT sum(a.size) FROM artifacts a WHERE a.preparation_id=r.preparation_id),0) END`
	var runFiles, hourFiles int
	var runBytes, hourBytes int64
	since := now - artifactRateWindow.Milliseconds()
	if err := q.QueryRowContext(ctx, `SELECT coalesce(sum(r.file_count),0), coalesce(sum(`+eff+`),0) FROM artifact_reservations r WHERE r.namespace_id=? AND r.owner_ref=?`,
		namespaceID, owner).Scan(&runFiles, &runBytes); err != nil {
		return fmt.Errorf("store: artifact run usage: %w", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT coalesce(sum(r.file_count),0), coalesce(sum(`+eff+`),0) FROM artifact_reservations r WHERE r.namespace_id=? AND r.created_at>=?`,
		namespaceID, since).Scan(&hourFiles, &hourBytes); err != nil {
		return fmt.Errorf("store: artifact hourly usage: %w", err)
	}
	if runFiles+files > lim.MaxRunFiles || runBytes+bytes > lim.MaxRunBytes ||
		hourFiles+files > lim.MaxHourFiles || hourBytes+bytes > lim.MaxHourBytes {
		return ErrArtifactRateLimited
	}
	return nil
}

type reservationRow struct {
	namespaceID string
	epoch       int64
	state       string
	bytes       int64
	expiresAt   int64
}

func readReservation(ctx context.Context, q taskSQL, prep string) (reservationRow, error) {
	var r reservationRow
	err := q.QueryRowContext(ctx, `SELECT namespace_id,namespace_epoch,state,bytes,expires_at FROM artifact_reservations WHERE preparation_id=?`, prep).
		Scan(&r.namespaceID, &r.epoch, &r.state, &r.bytes, &r.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrPreparationState
	}
	return r, err
}

func listPreparationArtifacts(ctx context.Context, q taskSQL, prep string) ([]Artifact, error) {
	rows, err := q.QueryContext(ctx, artifactSelect+` WHERE a.preparation_id=? ORDER BY a.artifact_id`, prep)
	if err != nil {
		return nil, err
	}
	return scanArtifacts(rows)
}

// MarkArtifactsReady revalidates the scope and the reservation and, once every
// snapshot of the preparation is persisted, marks all of its artifacts ready in
// one transaction. facts must cover exactly the preparation's artifacts.
func (s *Store) MarkArtifactsReady(ctx context.Context, sc ScopeRef, prep string, facts []ArtifactFacts) error {
	lim := s.artifactLimits()
	now := time.Now().UnixMilli()
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		res, err := readReservation(ctx, q, prep)
		if err != nil {
			return err
		}
		if res.namespaceID != sc.NamespaceID || res.epoch != sc.NamespaceEpoch || res.state != "active" {
			return ErrPreparationState
		}
		if now >= res.expiresAt {
			return ErrPreparationExpired
		}
		arts, err := listPreparationArtifacts(ctx, q, prep)
		if err != nil {
			return err
		}
		if len(arts) != len(facts) {
			return fmt.Errorf("%w: facts do not cover the preparation", ErrInvalidArtifact)
		}
		byID := map[string]ArtifactFacts{}
		var total int64
		for _, f := range facts {
			if _, dup := byID[f.ArtifactID]; dup || len(f.SHA256) != 64 || f.Size < 0 || f.Size > lim.MaxFileBytes || f.Filename == "" {
				return fmt.Errorf("%w: facts", ErrInvalidArtifact)
			}
			byID[f.ArtifactID] = f
			total += f.Size
		}
		if total > res.bytes {
			return fmt.Errorf("%w: snapshots exceed the reserved bytes", ErrInvalidArtifact)
		}
		for _, a := range arts {
			f, ok := byID[a.ID]
			if !ok || a.State != ArtifactPreparing {
				return fmt.Errorf("%w: artifact state", ErrPreparationState)
			}
			if _, err = q.ExecContext(ctx, `UPDATE artifacts SET state='ready',sha256=?,size=?,media_type=?,sanitized_filename=? WHERE artifact_id=? AND state='preparing'`,
				f.SHA256, f.Size, f.MediaType, f.Filename, a.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// FindAcceptedRequest returns the delivery already accepted for this request
// identity. A same-key request with different bytes is ErrRequestConflict; a
// durably rejected one is ErrRequestRejected. It lets the host converge
// duplicates before spending any preparation work.
func (s *Store) FindAcceptedRequest(ctx context.Context, sc ScopeRef, id RequestIdentity) (Delivery, bool, error) {
	var out Delivery
	var found bool
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		d, ok, e := findAcceptedQ(ctx, q, sc, id)
		out, found = d, ok
		return e
	})
	return out, found, err
}

func findAcceptedQ(ctx context.Context, q taskSQL, sc ScopeRef, id RequestIdentity) (Delivery, bool, error) {
	var storedHash, outcome, reason string
	var deliveryID sql.NullString
	err := q.QueryRowContext(ctx, `SELECT payload_hash,outcome,reason_code,delivery_id FROM ipc_message_requests WHERE namespace_id=? AND namespace_epoch=? AND request_key=?`,
		sc.NamespaceID, sc.NamespaceEpoch, id.Key).Scan(&storedHash, &outcome, &reason, &deliveryID)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, err
	}
	if storedHash != id.PayloadHash {
		return Delivery{}, false, ErrRequestConflict
	}
	if outcome == "rejected" {
		return Delivery{}, false, fmt.Errorf("%w: %s", ErrRequestRejected, reason)
	}
	d, err := readDelivery(ctx, q, deliveryID.String)
	return d, err == nil, err
}

// PublishDeliveryWithArtifacts is PublishDelivery plus artifact attachment in
// one transaction: every upload part must reference exactly one ready artifact
// of the preparation, all references are attached with the parts, and the
// reservation is released. A duplicate request (same key and bytes) returns the
// first delivery with Created=false and abandons this preparation. No
// filesystem or network work happens here.
func (s *Store) PublishDeliveryWithArtifacts(ctx context.Context, req PublishRequest, prep string) (Delivery, error) {
	now := time.Now().UnixMilli()
	if err := validatePublish(req, now); err != nil {
		return Delivery{}, err
	}
	if req.ExpiresAt == 0 {
		req.ExpiresAt = now + DefaultDeliveryExpiry.Milliseconds()
	}
	var out Delivery
	enlisted, err := s.withTx(ctx, func(q taskSQL) error {
		sc := req.Conversation.Scope
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		res, err := readReservation(ctx, q, prep)
		if err != nil {
			return err
		}
		if res.namespaceID != sc.NamespaceID || res.epoch != sc.NamespaceEpoch {
			return ErrPreparationState
		}
		if r := req.Request; r != nil {
			d, ok, e := findAcceptedQ(ctx, q, sc, *r)
			if e != nil {
				return e
			}
			if ok {
				out = d
				return abandonPreparationQ(ctx, q, prep, "duplicate_request", now)
			}
		}
		if res.state != "active" {
			return ErrPreparationState
		}
		if now >= res.expiresAt {
			return ErrPreparationExpired
		}
		if err = checkPartsMatchPreparation(ctx, q, req.Parts, prep); err != nil {
			return err
		}
		d, err := publishDeliveryQ(ctx, q, s.outboxLimits(), req, now)
		if err != nil {
			return err
		}
		out = d
		if !d.Created {
			return abandonPreparationQ(ctx, q, prep, "duplicate_request", now)
		}
		for _, p := range d.Parts {
			if p.Part.Kind != PartUpload {
				continue
			}
			id, _ := ParseArtifactRef(p.Part.ArtifactRef)
			if _, err = q.ExecContext(ctx, `INSERT INTO artifact_refs(artifact_id,ref_kind,ref_id,created_at) VALUES(?,'outbox_part',?,?)`, id, p.ID, now); err != nil {
				return fmt.Errorf("store: attach artifact reference: %w", err)
			}
			if _, err = q.ExecContext(ctx, `UPDATE artifacts SET last_reference_at=? WHERE artifact_id=?`, now, id); err != nil {
				return err
			}
		}
		_, err = q.ExecContext(ctx, `UPDATE artifact_reservations SET state='released' WHERE preparation_id=? AND state='active'`, prep)
		return err
	})
	if err != nil {
		return Delivery{}, err
	}
	if out.Created && !enlisted {
		s.NotifyPublished(out.ID)
	}
	return out, nil
}

// checkPartsMatchPreparation requires a bijection between the upload parts and
// the preparation's artifacts, all ready.
func checkPartsMatchPreparation(ctx context.Context, q taskSQL, parts []DeliveryPart, prep string) error {
	arts, err := listPreparationArtifacts(ctx, q, prep)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, a := range arts {
		if a.State != ArtifactReady {
			return fmt.Errorf("%w: artifact %s is %s", ErrPreparationState, a.ID, a.State)
		}
		want[a.ID] = true
	}
	for _, p := range parts {
		if p.Kind != PartUpload {
			continue
		}
		id, ok := ParseArtifactRef(p.ArtifactRef)
		if !ok || !want[id] {
			return fmt.Errorf("%w: upload part does not reference this preparation", ErrInvalidArtifact)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		return fmt.Errorf("%w: prepared artifacts are not all published", ErrInvalidArtifact)
	}
	return nil
}

// AbandonPreparation releases the reservation and marks every unreferenced
// artifact of the preparation deleting, so the host can remove the blobs.
func (s *Store) AbandonPreparation(ctx context.Context, prep, reason string) error {
	if reason == "" {
		return fmt.Errorf("%w: reason", ErrInvalidArtifact)
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		return abandonPreparationQ(ctx, q, prep, reason, time.Now().UnixMilli())
	})
	return err
}

func abandonPreparationQ(ctx context.Context, q taskSQL, prep, reason string, _ int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE artifact_reservations SET state='released' WHERE preparation_id=? AND state='active'`, prep); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE artifacts SET state='deleting',reason_code=? WHERE preparation_id=? AND state IN ('preparing','ready')
 AND NOT EXISTS (SELECT 1 FROM artifact_refs r WHERE r.artifact_id=artifacts.artifact_id)`, reason, prep)
	return err
}

const artifactSelect = `SELECT a.artifact_id,a.namespace_id,a.namespace_epoch,a.state,a.relative_blob_key,a.sha256,a.size,a.media_type,a.sanitized_filename,a.reason_code,a.created_at,a.last_reference_at,a.preparation_id FROM artifacts a`

func scanArtifacts(rows *sql.Rows) ([]Artifact, error) {
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.NamespaceID, &a.NamespaceEpoch, &a.State, &a.BlobKey, &a.SHA256, &a.Size, &a.MediaType, &a.Filename, &a.ReasonCode, &a.CreatedAt, &a.LastReferenceAt, &a.PreparationID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetArtifact returns one inventory row.
func (s *Store) GetArtifact(ctx context.Context, id string) (Artifact, error) {
	rows, err := s.taskDB(ctx).QueryContext(ctx, artifactSelect+` WHERE a.artifact_id=?`, id)
	if err != nil {
		return Artifact{}, err
	}
	arts, err := scanArtifacts(rows)
	if err != nil {
		return Artifact{}, err
	}
	if len(arts) == 0 {
		return Artifact{}, ErrArtifactNotFound
	}
	return arts[0], nil
}

// QuarantineArtifact marks a ready artifact quarantined (missing, corrupt or
// hash-mismatched) and fails every still-pending part that references it, so the
// affected publication fails visibly instead of uploading wrong bytes. A part
// that is sending or uncertain is never rewritten. It returns the number of
// parts failed. Quarantining twice is a no-op.
func (s *Store) QuarantineArtifact(ctx context.Context, id, reason string) (int, error) {
	if reason == "" {
		return 0, fmt.Errorf("%w: reason", ErrInvalidArtifact)
	}
	failed := 0
	_, err := s.withTx(ctx, func(q taskSQL) error {
		now := time.Now().UnixMilli()
		res, err := q.ExecContext(ctx, `UPDATE artifacts SET state='quarantined',reason_code=? WHERE artifact_id=? AND state='ready'`, reason, id)
		if err != nil {
			return err
		}
		changed, _ := res.RowsAffected()
		if changed == 0 {
			var st string
			if e := q.QueryRowContext(ctx, `SELECT state FROM artifacts WHERE artifact_id=?`, id).Scan(&st); errors.Is(e, sql.ErrNoRows) {
				return ErrArtifactNotFound
			} else if e != nil {
				return e
			}
		}
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT p.delivery_id FROM outbox_parts p JOIN artifact_refs r ON r.ref_id=p.part_id AND r.ref_kind='outbox_part'
 WHERE r.artifact_id=? AND p.state='pending'`, id)
		if err != nil {
			return err
		}
		var deliveries []string
		for rows.Next() {
			var d string
			if err = rows.Scan(&d); err != nil {
				rows.Close()
				return err
			}
			deliveries = append(deliveries, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		upd, err := q.ExecContext(ctx, `UPDATE outbox_parts SET state='failed',reason_code='artifact_quarantined',updated_at=? WHERE state='pending'
 AND part_id IN (SELECT ref_id FROM artifact_refs WHERE artifact_id=? AND ref_kind='outbox_part')`, now, id)
		if err != nil {
			return err
		}
		n, _ := upd.RowsAffected()
		failed = int(n)
		for _, d := range deliveries {
			if err = recomputeGroupState(ctx, q, d, now); err != nil {
				return err
			}
		}
		return nil
	})
	return failed, err
}

// ExpiredPreparations abandons every active reservation whose deadline passed.
// Their unreferenced artifacts become deleting; the host then removes the blobs
// and calls CompleteArtifactDeletion. It returns the abandoned preparation ids.
func (s *Store) ExpiredPreparations(ctx context.Context, now time.Time) ([]string, error) {
	var ids []string
	_, err := s.withTx(ctx, func(q taskSQL) error {
		rows, err := q.QueryContext(ctx, `SELECT preparation_id FROM artifact_reservations WHERE state='active' AND expires_at<=?`, now.UnixMilli())
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err = abandonPreparationQ(ctx, q, id, "preparation_expired", now.UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// MarkArtifactsForGC moves ready or quarantined artifacts whose last reference
// activity is older than now-retention to deleting, but only when no reference
// is pending, sending or uncertain. The check and the update are one statement.
// The caller unlinks the blobs and then calls CompleteArtifactDeletion.
func (s *Store) MarkArtifactsForGC(ctx context.Context, now time.Time, retention time.Duration, limit int) (int, error) {
	if retention <= 0 {
		retention = DefaultArtifactRetention
	}
	if limit <= 0 {
		limit = 100
	}
	cutoff := now.Add(-retention).UnixMilli()
	var n int64
	_, err := s.withTx(ctx, func(q taskSQL) error {
		res, err := q.ExecContext(ctx, `UPDATE artifacts SET state='deleting',reason_code='gc' WHERE artifact_id IN (
 SELECT a.artifact_id FROM artifacts a WHERE a.state IN ('ready','quarantined')
 AND NOT EXISTS (SELECT 1 FROM artifact_reservations r WHERE r.preparation_id=a.preparation_id AND r.state='active')
 AND NOT `+pinnedByUnresolvedSQL+`
 AND max(a.created_at, a.last_reference_at, coalesce((SELECT max(g.updated_at) FROM artifact_refs gr
   JOIN outbox_parts gp ON gp.part_id=gr.ref_id JOIN delivery_groups g ON g.delivery_id=gp.delivery_id
   WHERE gr.artifact_id=a.artifact_id AND gr.ref_kind='outbox_part'),0)) < ?
 ORDER BY a.created_at LIMIT ?)`, cutoff, limit)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

// DeletingArtifact is a blob the host must unlink before the row can go.
type DeletingArtifact struct {
	ID            string
	BlobKey       string
	PreparationID string
}

// ListDeletingArtifacts pages (by id, after afterID) artifacts awaiting blob
// removal, so a run of failing rows cannot hide the rest, and so
// a crash between unlink and row deletion finishes idempotently.
func (s *Store) ListDeletingArtifacts(ctx context.Context, afterID string, limit int) ([]DeletingArtifact, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT artifact_id,relative_blob_key,preparation_id FROM artifacts WHERE state='deleting' AND artifact_id>? ORDER BY artifact_id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeletingArtifact
	for rows.Next() {
		var d DeletingArtifact
		if err = rows.Scan(&d.ID, &d.BlobKey, &d.PreparationID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CompleteArtifactDeletion removes a deleting artifact and its terminal
// references after its blob is gone. A pinned artifact is never removed.
func (s *Store) CompleteArtifactDeletion(ctx context.Context, id string) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var pinned int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM artifacts a WHERE a.artifact_id=? AND a.state='deleting' AND `+pinnedByUnresolvedSQL, id).Scan(&pinned); err != nil {
			return err
		}
		if pinned != 0 {
			return ErrArtifactPinned
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM artifact_refs WHERE artifact_id=?`, id); err != nil {
			return err
		}
		res, err := q.ExecContext(ctx, `DELETE FROM artifacts WHERE artifact_id=? AND state='deleting'`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrArtifactNotFound
		}
		return nil
	})
	return err
}

// ReadyArtifacts pages ready artifacts by id for blob verification sweeps.
func (s *Store) ReadyArtifacts(ctx context.Context, afterID string, limit int) ([]Artifact, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, artifactSelect+` WHERE a.state='ready' AND a.artifact_id>? ORDER BY a.artifact_id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanArtifacts(rows)
}

// KnownPreparation reports whether any reservation or artifact exists for the
// preparation id, so the host can tell a foreign directory from its own.
func (s *Store) KnownPreparation(ctx context.Context, prep string) (bool, error) {
	var n int
	err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifact_reservations WHERE preparation_id=?)+(SELECT count(*) FROM artifacts WHERE preparation_id=?)`, prep, prep).Scan(&n)
	return n > 0, err
}

// RestoreArtifact returns a quarantined artifact to ready after the host
// re-verified its blob (size and SHA-256), for example after a blob root was
// restored. Parts already failed because of the quarantine stay failed.
func (s *Store) RestoreArtifact(ctx context.Context, id string) error {
	var changed int64
	_, err := s.withTx(ctx, func(q taskSQL) error {
		res, err := q.ExecContext(ctx, `UPDATE artifacts SET state='ready',reason_code='' WHERE artifact_id=? AND state='quarantined'`, id)
		if err != nil {
			return err
		}
		changed, _ = res.RowsAffected()
		return nil
	})
	if err == nil && changed == 0 {
		return ErrArtifactNotFound
	}
	return err
}

// QuarantinedArtifacts pages quarantined artifacts by id.
func (s *Store) QuarantinedArtifacts(ctx context.Context, afterID string, limit int) ([]Artifact, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, artifactSelect+` WHERE a.state='quarantined' AND a.artifact_id>? ORDER BY a.artifact_id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanArtifacts(rows)
}
