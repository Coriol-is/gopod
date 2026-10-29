package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var reserveSeq atomic.Int64

func (f *deliveryFixture) reserve(files int, bytes int64) Preparation {
	f.t.Helper()
	p, err := f.st.ReserveArtifacts(f.t.Context(), ArtifactReservation{Scope: f.conv.Scope, OwnerRef: fmt.Sprintf("run-%d", reserveSeq.Add(1)), Files: files, Bytes: bytes})
	if err != nil {
		f.t.Fatalf("ReserveArtifacts: %v", err)
	}
	return p
}

func (f *deliveryFixture) ready(p Preparation, sizes ...int64) {
	f.t.Helper()
	var facts []ArtifactFacts
	for i, id := range p.ArtifactIDs {
		facts = append(facts, ArtifactFacts{ArtifactID: id, SHA256: testSHA, Size: sizes[i], MediaType: "text/plain", Filename: fmt.Sprintf("f%d.txt", i)})
	}
	if err := f.st.MarkArtifactsReady(f.t.Context(), f.conv.Scope, p.ID, facts); err != nil {
		f.t.Fatalf("MarkArtifactsReady: %v", err)
	}
}

func (f *deliveryFixture) uploadReq(p Preparation, key string) PublishRequest {
	var parts []DeliveryPart
	parts = append(parts, DeliveryPart{Kind: PartText, Payload: []byte("see files")})
	for _, id := range p.ArtifactIDs {
		parts = append(parts, DeliveryPart{Kind: PartUpload, ArtifactRef: ArtifactRef(id), Payload: []byte("caption")})
	}
	r := PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC, Parts: parts}
	if key != "" {
		r.Request = &RequestIdentity{Key: key, PayloadHash: sha256Hex([]byte(key))}
	}
	return r
}

func (f *deliveryFixture) setPart(d Delivery, ordinal int, state string) {
	f.t.Helper()
	if _, err := f.st.db.ExecContext(f.t.Context(), `UPDATE outbox_parts SET state=? WHERE delivery_id=? AND ordinal=?`, state, d.ID, ordinal); err != nil {
		f.t.Fatal(err)
	}
	if err := recomputeGroupState(f.t.Context(), f.st.db, d.ID, time.Now().UnixMilli()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deliveryFixture) artifactState(id string) string {
	f.t.Helper()
	a, err := f.st.GetArtifact(f.t.Context(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return a.State
}

func TestArtifactSchemaIsIdempotent(t *testing.T) {
	f := newDeliveryFixture(t)
	for i := 0; i < 2; i++ {
		if err := f.st.applyArtifactSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	path := f.st.path
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { st.Close() })
}

func TestArtifactLifecycleAttachesReferencesWithPublication(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(2, 1000)
	if used, _, _ := f.st.ArtifactUsage(t.Context(), f.conv.Scope.NamespaceID); used != 1000 {
		t.Fatalf("reserved usage %d", used)
	}
	f.ready(p, 300, 200)
	// Reserved bound still counts (not double counted with measured sizes).
	if used, global, _ := f.st.ArtifactUsage(t.Context(), f.conv.Scope.NamespaceID); used != 1000 || global != 1000 {
		t.Fatalf("usage while reserved %d/%d", used, global)
	}
	d, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, "req-1.json"), p.ID)
	if err != nil || !d.Created {
		t.Fatalf("publish: %v created=%v", err, d.Created)
	}
	if got := countRows(t, f.st, `SELECT count(*) FROM artifact_refs`); got != 2 {
		t.Fatalf("refs %d", got)
	}
	if used, _, _ := f.st.ArtifactUsage(t.Context(), f.conv.Scope.NamespaceID); used != 500 {
		t.Fatalf("usage after release should be measured sizes, got %d", used)
	}
	if d.Parts[1].Part.Kind != PartUpload || string(d.Parts[1].Part.Payload) != "caption" {
		t.Fatalf("part %+v", d.Parts[1])
	}
	a, _ := f.st.GetArtifact(t.Context(), p.ArtifactIDs[0])
	if a.State != ArtifactReady || a.LastReferenceAt == 0 || a.SHA256 != testSHA {
		t.Fatalf("artifact %+v", a)
	}
}

func TestArtifactReserveBounds(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxFileBytes: 100, MaxRequestBytes: 150, MaxNamespaceBytes: 400, MaxGlobalBytes: 500, MaxFiles: 2}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		files int
		bytes int64
	}{{0, 10}, {3, 10}, {1, 0}, {1, 151}} {
		if _, err := f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, Files: c.files, Bytes: c.bytes}); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	f.reserve(1, 150)
	f.reserve(1, 150)
	if _, err := f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, Files: 1, Bytes: 150}); !errors.Is(err, ErrArtifactQuotaExceeded) {
		t.Fatalf("namespace quota: %v", err)
	}
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxNamespaceBytes: 1 << 30, MaxGlobalBytes: 300, MaxRequestBytes: 150}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, Files: 1, Bytes: 1}); !errors.Is(err, ErrArtifactQuotaExceeded) {
		t.Fatalf("global quota: %v", err)
	}
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxFileBytes: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestArtifactMarkReadyRejectsOversizeAndBadFacts(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxFileBytes: 100}); err != nil {
		t.Fatal(err)
	}
	p := f.reserve(1, 100)
	bad := []ArtifactFacts{
		{ArtifactID: p.ArtifactIDs[0], SHA256: testSHA, Size: 101, Filename: "a"},
		{ArtifactID: p.ArtifactIDs[0], SHA256: "short", Size: 1, Filename: "a"},
		{ArtifactID: "nope", SHA256: testSHA, Size: 1, Filename: "a"},
	}
	for i, fact := range bad {
		if err := f.st.MarkArtifactsReady(t.Context(), f.conv.Scope, p.ID, []ArtifactFacts{fact}); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	if f.artifactState(p.ArtifactIDs[0]) != ArtifactPreparing {
		t.Fatal("rejected facts changed state")
	}
}

func TestArtifactQuotaRaceNeverExceedsCap(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxRequestBytes: 30, MaxNamespaceBytes: 100, MaxGlobalBytes: 100}); err != nil {
		t.Fatal(err)
	}
	var ok, quota atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.st.ReserveArtifacts(context.Background(), ArtifactReservation{Scope: f.conv.Scope, Files: 1, Bytes: 30})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrArtifactQuotaExceeded):
				quota.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 3 || quota.Load() != 21 {
		t.Fatalf("ok=%d quota=%d", ok.Load(), quota.Load())
	}
	if ns, global, _ := f.st.ArtifactUsage(t.Context(), f.conv.Scope.NamespaceID); ns != 90 || global != 90 {
		t.Fatalf("usage %d/%d", ns, global)
	}
}

func TestArtifactStaleScopeRejected(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(1, 10)
	f.ready(p, 5)
	if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, Files: 1, Bytes: 1}); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, "k.json"), p.ID); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("publish: %v", err)
	}
	if err := f.st.MarkArtifactsReady(t.Context(), f.conv.Scope, p.ID, nil); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("mark ready: %v", err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 0 || countRows(t, f.st, `SELECT count(*) FROM artifact_refs`) != 0 {
		t.Fatal("stale publication left rows")
	}
}

func TestArtifactPublishNeedsEveryArtifactReadyAndReferencedOnce(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(2, 100)
	// not ready yet
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, ""), p.ID); !errors.Is(err, ErrPreparationState) {
		t.Fatalf("preparing artifacts: %v", err)
	}
	f.ready(p, 1, 1)
	missing := f.uploadReq(p, "")
	missing.Parts = missing.Parts[:2]
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), missing, p.ID); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("unpublished artifact: %v", err)
	}
	other := f.reserve(1, 10)
	foreign := f.uploadReq(p, "")
	foreign.Parts[1].ArtifactRef = ArtifactRef(other.ArtifactIDs[0])
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), foreign, p.ID); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("foreign artifact: %v", err)
	}
	twice := f.uploadReq(p, "")
	twice.Parts[2].ArtifactRef = twice.Parts[1].ArtifactRef
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), twice, p.ID); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("same artifact twice: %v", err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 0 {
		t.Fatal("rejected publications left deliveries")
	}
}

func TestArtifactDuplicateRequestConvergesAndAbandonsSecondPreparation(t *testing.T) {
	f := newDeliveryFixture(t)
	a := f.reserve(1, 100)
	b := f.reserve(1, 100)
	f.ready(a, 10)
	f.ready(b, 10)
	first, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(a, "dup.json"), a.ID)
	if err != nil || !first.Created {
		t.Fatalf("first: %v", err)
	}
	second, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(b, "dup.json"), b.ID)
	if err != nil || second.Created || second.ID != first.ID {
		t.Fatalf("second: %v %+v", err, second)
	}
	if f.artifactState(b.ArtifactIDs[0]) != ArtifactDeleting || f.artifactState(a.ArtifactIDs[0]) != ArtifactReady {
		t.Fatal("duplicate must abandon only the second preparation")
	}
	if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 1 || countRows(t, f.st, `SELECT count(*) FROM artifact_refs`) != 1 {
		t.Fatal("duplicate published twice")
	}
}

func TestArtifactConcurrentDuplicatesConverge(t *testing.T) {
	f := newDeliveryFixture(t)
	const n = 8
	preps := make([]Preparation, n)
	for i := range preps {
		preps[i] = f.reserve(1, 100)
		f.ready(preps[i], 10)
	}
	var created atomic.Int32
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := f.st.PublishDeliveryWithArtifacts(context.Background(), f.uploadReq(preps[i], "same.json"), preps[i].ID)
			if err != nil {
				t.Errorf("publish %d: %v", i, err)
				return
			}
			ids[i] = d.ID
			if d.Created {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d deliveries", created.Load())
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("duplicates diverged")
		}
	}
	if got := countRows(t, f.st, `SELECT count(*) FROM artifacts WHERE state='deleting'`); got != n-1 {
		t.Fatalf("abandoned %d", got)
	}
}

func TestArtifactChangedBytesUnderSameKeyConflict(t *testing.T) {
	f := newDeliveryFixture(t)
	a := f.reserve(1, 100)
	b := f.reserve(1, 100)
	f.ready(a, 1)
	f.ready(b, 1)
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(a, "k.json"), a.ID); err != nil {
		t.Fatal(err)
	}
	r := f.uploadReq(b, "k.json")
	r.Request.PayloadHash = sha256Hex([]byte("other"))
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), r, b.ID); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if f.artifactState(b.ArtifactIDs[0]) != ArtifactReady {
		t.Fatal("conflict must leave the preparation to the caller")
	}
	if err := f.st.AbandonPreparation(t.Context(), b.ID, "request_conflict"); err != nil {
		t.Fatal(err)
	}
	if f.artifactState(b.ArtifactIDs[0]) != ArtifactDeleting {
		t.Fatal("abandon did not mark deleting")
	}
}

func TestArtifactFindAcceptedRequest(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(1, 100)
	f.ready(p, 1)
	req := f.uploadReq(p, "find.json")
	if _, ok, err := f.st.FindAcceptedRequest(t.Context(), f.conv.Scope, *req.Request); ok || err != nil {
		t.Fatalf("before: %v %v", ok, err)
	}
	d, err := f.st.PublishDeliveryWithArtifacts(t.Context(), req, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := f.st.FindAcceptedRequest(t.Context(), f.conv.Scope, *req.Request)
	if err != nil || !ok || got.ID != d.ID {
		t.Fatalf("after: %v %v", ok, err)
	}
	if _, _, err = f.st.FindAcceptedRequest(t.Context(), f.conv.Scope, RequestIdentity{Key: "find.json", PayloadHash: sha256Hex([]byte("x"))}); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestArtifactExpiredPreparationIsReconciled(t *testing.T) {
	f := newDeliveryFixture(t)
	p, err := f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, Files: 1, Bytes: 50, TTL: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	facts := []ArtifactFacts{{ArtifactID: p.ArtifactIDs[0], SHA256: testSHA, Size: 1, Filename: "a"}}
	if err = f.st.MarkArtifactsReady(t.Context(), f.conv.Scope, p.ID, facts); !errors.Is(err, ErrPreparationExpired) {
		t.Fatalf("late ready: %v", err)
	}
	live := f.reserve(1, 10)
	ids, err := f.st.ExpiredPreparations(t.Context(), time.Now())
	if err != nil || len(ids) != 1 || ids[0] != p.ID {
		t.Fatalf("expired %v %v", ids, err)
	}
	if f.artifactState(p.ArtifactIDs[0]) != ArtifactDeleting || f.artifactState(live.ArtifactIDs[0]) != ArtifactPreparing {
		t.Fatal("only the expired preparation is reclaimed")
	}
	if ns, _, _ := f.st.ArtifactUsage(t.Context(), f.conv.Scope.NamespaceID); ns != 10 {
		t.Fatalf("expired reservation still counted: %d", ns)
	}
	del, _ := f.st.ListDeletingArtifacts(t.Context(), "", 10)
	if len(del) != 1 || del[0].BlobKey != p.BlobKeys[0] {
		t.Fatalf("deleting %+v", del)
	}
	if err = f.st.CompleteArtifactDeletion(t.Context(), del[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.GetArtifact(t.Context(), del[0].ID); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("row remains: %v", err)
	}
	if again, _ := f.st.ExpiredPreparations(t.Context(), time.Now()); len(again) != 0 {
		t.Fatal("reconcile is not idempotent")
	}
}

func TestArtifactQuarantineFailsPendingPartsOnly(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(2, 100)
	f.ready(p, 1, 1)
	d, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, ""), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.setPart(d, 0, PartSent)
	f.setPart(d, 2, PartSending)
	n, err := f.st.QuarantineArtifact(t.Context(), p.ArtifactIDs[0], "blob_missing")
	if err != nil || n != 1 {
		t.Fatalf("failed parts %d %v", n, err)
	}
	got, _ := f.st.GetDelivery(t.Context(), d.ID)
	if got.Parts[1].State != PartFailed || got.Parts[1].ReasonCode != "artifact_quarantined" || got.Parts[2].State != PartSending || got.Parts[0].State != PartSent {
		t.Fatalf("parts %+v", got.Parts)
	}
	if n, err = f.st.QuarantineArtifact(t.Context(), p.ArtifactIDs[0], "again"); err != nil || n != 0 {
		t.Fatalf("idempotent: %d %v", n, err)
	}
	if _, err = f.st.QuarantineArtifact(t.Context(), "00000000-0000-4000-8000-000000000000", "x"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestArtifactGCNeverDeletesPendingOrUncertainReference(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(3, 100)
	f.ready(p, 1, 1, 1)
	d, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, ""), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(365 * 24 * time.Hour)
	// parts: 0 text, 1..3 uploads for artifacts 0..2
	f.setPart(d, 0, PartSent)
	f.setPart(d, 1, PartPending)
	f.setPart(d, 2, PartUncertain)
	f.setPart(d, 3, PartSent)
	n, err := f.st.MarkArtifactsForGC(t.Context(), future, time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("group is unresolved (pending + uncertain): collected %d", n)
	}
	// Even if the group were mislabelled resolved, the per-part pin holds.
	if _, err = f.st.db.ExecContext(t.Context(), `UPDATE delivery_groups SET unresolved=0 WHERE delivery_id=?`, d.ID); err != nil {
		t.Fatal(err)
	}
	if n, err = f.st.MarkArtifactsForGC(t.Context(), future, time.Hour, 10); err != nil || n != 1 {
		t.Fatalf("only the sent upload may go: %d %v", n, err)
	}
	if f.artifactState(p.ArtifactIDs[2]) != ArtifactDeleting || f.artifactState(p.ArtifactIDs[0]) != ArtifactReady || f.artifactState(p.ArtifactIDs[1]) != ArtifactReady {
		t.Fatal("pending/uncertain references must stay ready")
	}
	// Raw deletion of a pinned artifact is blocked by the trigger.
	if _, err = f.st.db.ExecContext(t.Context(), `DELETE FROM artifacts WHERE artifact_id=?`, p.ArtifactIDs[1]); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("raw delete of ready artifact: %v", err)
	}
	if _, err = f.st.db.ExecContext(t.Context(), `UPDATE artifacts SET state='deleting' WHERE artifact_id=?`, p.ArtifactIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.ExecContext(t.Context(), `DELETE FROM artifacts WHERE artifact_id=?`, p.ArtifactIDs[1]); err == nil {
		t.Fatal("trigger let a pinned deleting artifact go")
	}
	if err = f.st.CompleteArtifactDeletion(t.Context(), p.ArtifactIDs[1]); !errors.Is(err, ErrArtifactPinned) {
		t.Fatalf("complete pinned: %v", err)
	}
	if _, err = f.st.db.ExecContext(t.Context(), `DELETE FROM artifact_refs WHERE artifact_id=?`, p.ArtifactIDs[1]); err == nil {
		t.Fatal("trigger let a pinned reference go")
	}
	// The collectable one completes.
	if err = f.st.CompleteArtifactDeletion(t.Context(), p.ArtifactIDs[2]); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactGCRespectsRetentionAndTerminalState(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(1, 100)
	f.ready(p, 1)
	d, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, ""), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.setPart(d, 0, PartSent)
	f.setPart(d, 1, PartSent)
	if n, _ := f.st.MarkArtifactsForGC(t.Context(), time.Now(), 30*24*time.Hour, 10); n != 0 {
		t.Fatal("collected before retention")
	}
	if n, _ := f.st.MarkArtifactsForGC(t.Context(), time.Now().Add(31*24*time.Hour), 30*24*time.Hour, 10); n != 1 {
		t.Fatal("not collected after retention")
	}
	if n, _ := f.st.MarkArtifactsForGC(t.Context(), time.Now().Add(31*24*time.Hour), 30*24*time.Hour, 10); n != 0 {
		t.Fatal("collected twice")
	}
}

func TestArtifactGCRacesPublishWithoutLosingReferences(t *testing.T) {
	f := newDeliveryFixture(t)
	future := time.Now().Add(400 * 24 * time.Hour)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := f.st.MarkArtifactsForGC(context.Background(), future, time.Hour, 50); err != nil {
					t.Errorf("gc: %v", err)
					return
				}
			}
		}
	}()
	for i := 0; i < 12; i++ {
		p := f.reserve(1, 100)
		f.ready(p, 1)
		d, err := f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, fmt.Sprintf("r%d.json", i)), p.ID)
		if err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		// The part is pending, so the artifact must survive concurrent GC.
		if st := f.artifactState(p.ArtifactIDs[0]); st != ArtifactReady {
			t.Fatalf("artifact %d is %s while its part %s is pending", i, st, d.Parts[1].State)
		}
	}
	close(stop)
	wg.Wait()
}

func TestArtifactRefParsing(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	if got, ok := ParseArtifactRef(ArtifactRef(id)); !ok || got != id {
		t.Fatal("round trip")
	}
	for _, bad := range []string{"", "artifact:", "artifact:../x", "x:" + id, id} {
		if _, ok := ParseArtifactRef(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestArtifactImmutableFacts(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(1, 10)
	f.ready(p, 5)
	for _, q := range []string{
		`UPDATE artifacts SET sha256='x' WHERE artifact_id=?`,
		`UPDATE artifacts SET size=9 WHERE artifact_id=?`,
		`UPDATE artifacts SET relative_blob_key='other' WHERE artifact_id=?`,
		`UPDATE artifacts SET namespace_id='x' WHERE artifact_id=?`,
	} {
		if _, err := f.st.db.ExecContext(t.Context(), q, p.ArtifactIDs[0]); err == nil {
			t.Fatalf("mutable: %s", q)
		}
	}
}

func TestPublishDeliveryRefusesUploadParts(t *testing.T) {
	f := newDeliveryFixture(t)
	req := PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC,
		Parts: []DeliveryPart{{Kind: PartUpload, ArtifactRef: ArtifactRef("11111111-1111-4111-8111-111111111111")}}}
	if _, err := f.st.PublishDelivery(t.Context(), req); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("upload via PublishDelivery: %v", err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 0 {
		t.Fatal("rejected upload left a delivery")
	}
}

func TestArtifactRunAndHourlyRateCaps(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxRunFiles: 3, MaxRunBytes: 1000, MaxHourFiles: 5, MaxHourBytes: 2000}); err != nil {
		t.Fatal(err)
	}
	res := func(owner string, files int, bytes int64) error {
		_, err := f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, OwnerRef: owner, Files: files, Bytes: bytes})
		return err
	}
	if err := res("a", 2, 100); err != nil {
		t.Fatal(err)
	}
	if err := res("a", 2, 100); !errors.Is(err, ErrArtifactRateLimited) {
		t.Fatalf("per-run files: %v", err)
	}
	if err := res("a", 1, 901); !errors.Is(err, ErrArtifactRateLimited) {
		t.Fatalf("per-run bytes: %v", err)
	}
	if err := res("b", 3, 100); err != nil {
		t.Fatalf("another run is independent: %v", err)
	}
	if err := res("c", 1, 100); !errors.Is(err, ErrArtifactRateLimited) {
		t.Fatalf("per-hour files (5 used): %v", err)
	}
}

func TestArtifactRateCountsMeasuredBytesAfterRelease(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxRunBytes: 1 << 20, MaxHourBytes: 600}); err != nil {
		t.Fatal(err)
	}
	p, err := f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, OwnerRef: "r", Files: 1, Bytes: 500})
	if err != nil {
		t.Fatal(err)
	}
	f.ready(p, 10)
	if _, err = f.st.PublishDeliveryWithArtifacts(t.Context(), f.uploadReq(p, ""), p.ID); err != nil {
		t.Fatal(err)
	}
	// 10 measured bytes, not 500 reserved, count once released.
	if _, err = f.st.ReserveArtifacts(t.Context(), ArtifactReservation{Scope: f.conv.Scope, OwnerRef: "r2", Files: 1, Bytes: 500}); err != nil {
		t.Fatalf("measured bytes should leave room: %v", err)
	}
}

func TestArtifactRestoreAfterQuarantine(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(1, 100)
	f.ready(p, 1)
	if _, err := f.st.QuarantineArtifact(t.Context(), p.ArtifactIDs[0], "blob_missing"); err != nil {
		t.Fatal(err)
	}
	q, err := f.st.QuarantinedArtifacts(t.Context(), "", 10)
	if err != nil || len(q) != 1 {
		t.Fatalf("quarantined %v %v", q, err)
	}
	if err = f.st.RestoreArtifact(t.Context(), p.ArtifactIDs[0]); err != nil {
		t.Fatal(err)
	}
	if f.artifactState(p.ArtifactIDs[0]) != ArtifactReady {
		t.Fatal("not restored")
	}
	if err = f.st.RestoreArtifact(t.Context(), p.ArtifactIDs[0]); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("restore of a ready artifact: %v", err)
	}
}

func TestListDeletingArtifactsPagesPastFailingRows(t *testing.T) {
	f := newDeliveryFixture(t)
	var ids []string
	for i := 0; i < 5; i++ {
		p := f.reserve(1, 10)
		if err := f.st.AbandonPreparation(t.Context(), p.ID, "x"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ArtifactIDs[0])
	}
	first, _ := f.st.ListDeletingArtifacts(t.Context(), "", 2)
	if len(first) != 2 {
		t.Fatalf("page %d", len(first))
	}
	second, _ := f.st.ListDeletingArtifacts(t.Context(), first[1].ID, 10)
	if len(second) != 3 || second[0].ID <= first[1].ID {
		t.Fatalf("cursor did not advance past the first page: %v", second)
	}
	_ = ids
}
