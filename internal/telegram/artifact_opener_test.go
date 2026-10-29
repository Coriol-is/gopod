package telegram

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/store"
)

type openerFixture struct {
	t      *testing.T
	st     *store.Store
	blobs  *artifactfs.Blobs
	root   string
	art    store.Artifact
	dispID string
}

func resolvedDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newOpenerFixture publishes one text + one upload delivery whose artifact was
// snapshotted from a real file, through the same store paths production uses.
func newOpenerFixture(t *testing.T, content string) *openerFixture {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	conv, err := st.SnapshotConversation(ctx, "tg:1", 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	src := resolvedDir(t)
	if err = os.WriteFile(filepath.Join(src, "r.csv"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	blobs, err := artifactfs.OpenBlobs(filepath.Join(resolvedDir(t), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blobs.Close() })

	prep, err := st.ReserveArtifacts(ctx, store.ArtifactReservation{Scope: conv.Scope, Files: 1, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = blobs.NewPreparation(prep.ID); err != nil {
		t.Fatal(err)
	}
	root, err := artifactfs.OpenSourceRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.Open("r.csv", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, name, _ := cutKey(prep.BlobKeys[0])
	snap, err := blobs.Snapshot(prep.ID, name, file, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.MarkArtifactsReady(ctx, conv.Scope, prep.ID, []store.ArtifactFacts{{ArtifactID: prep.ArtifactIDs[0], SHA256: snap.SHA256, Size: snap.Size, MediaType: "text/csv", Filename: "report.csv"}}); err != nil {
		t.Fatal(err)
	}
	d, err := st.PublishDeliveryWithArtifacts(ctx, store.PublishRequest{Conversation: conv, Owner: store.OwnerLegacyAgentIPC, Parts: []store.DeliveryPart{
		{Kind: store.PartText, Payload: []byte("here")},
		{Kind: store.PartUpload, ArtifactRef: store.ArtifactRef(prep.ArtifactIDs[0]), Payload: []byte("the report")},
	}}, prep.ID)
	if err != nil {
		t.Fatal(err)
	}
	art, err := st.GetArtifact(ctx, prep.ArtifactIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	return &openerFixture{t: t, st: st, blobs: blobs, art: art, dispID: d.ID}
}

func cutKey(k string) (string, string, bool) {
	for i := 0; i < len(k); i++ {
		if k[i] == '/' {
			return k[:i], k[i+1:], true
		}
	}
	return k, "", false
}

func (f *openerFixture) opener() *SnapshotOpener {
	return &SnapshotOpener{Inventory: f.st, Blobs: f.blobs, MaxBytes: 1 << 20, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func (f *openerFixture) uploadReq() SendRequest {
	return SendRequest{Kind: store.PartUpload, ChatID: 1, Thread: 3, Text: "the report", ArtifactRef: store.ArtifactRef(f.art.ID)}
}

func TestSnapshotOpenerUploadsTheExactVerifiedBytesWithNoPathReopen(t *testing.T) {
	f := newOpenerFixture(t, "a,b\n1,2\n")
	// The agent's source is gone and no pathname can be reopened: the upload
	// uses the verified snapshot buffer.
	api := &fakeAPI{msg: &models.Message{ID: 5}}
	out := (&botTransport{api: api, artifacts: f.opener()}).Attempt(t.Context(), f.uploadReq())
	if out.Kind != OutcomeAck || out.RemoteMessageID != 5 {
		t.Fatalf("outcome %+v", out)
	}
	if api.docs != 1 || string(api.docBytes) != "a,b\n1,2\n" {
		t.Fatalf("docs=%d bytes=%q", api.docs, api.docBytes)
	}
	up := api.lastDoc.Document.(*models.InputFileUpload)
	if up.Filename != "report.csv" || api.lastDoc.Caption != "the report" || api.lastDoc.MessageThreadID != 3 || api.lastDoc.ChatID != int64(1) {
		t.Fatalf("params %+v / %+v", api.lastDoc, up)
	}
}

func TestSnapshotOpenerHashMismatchQuarantinesWithoutUpload(t *testing.T) {
	f := newOpenerFixture(t, "original")
	blob := filepath.Join(f.blobPath(), f.art.BlobKey)
	if err := os.WriteFile(blob, []byte("TAMPERED"), 0o600); err != nil { // same size
		t.Fatal(err)
	}
	api := &fakeAPI{msg: &models.Message{ID: 5}}
	out := (&botTransport{api: api, artifacts: f.opener()}).Attempt(t.Context(), f.uploadReq())
	if out.Kind != OutcomeRefused || out.Code != "artifact_unavailable" {
		t.Fatalf("outcome %+v", out)
	}
	if api.docs != 0 {
		t.Fatal("tampered bytes were uploaded")
	}
	art, _ := f.st.GetArtifact(t.Context(), f.art.ID)
	if art.State != store.ArtifactQuarantined || art.ReasonCode != "hash_mismatch" {
		t.Fatalf("artifact %+v", art)
	}
	// A second attempt never reaches the blob and never uploads.
	if out = (&botTransport{api: api, artifacts: f.opener()}).Attempt(t.Context(), f.uploadReq()); out.Kind != OutcomeRefused || api.docs != 0 {
		t.Fatalf("second attempt %+v docs=%d", out, api.docs)
	}
}

func (f *openerFixture) blobPath() string { return f.blobs.RootForTest() }

func TestSnapshotOpenerMissingAndForeignRefs(t *testing.T) {
	f := newOpenerFixture(t, "data")
	if err := os.Remove(filepath.Join(f.blobPath(), f.art.BlobKey)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.opener().OpenArtifact(t.Context(), store.ArtifactRef(f.art.ID)); err == nil {
		t.Fatal("missing blob opened")
	}
	if art, _ := f.st.GetArtifact(t.Context(), f.art.ID); art.State != store.ArtifactQuarantined || art.ReasonCode != "blob_missing" {
		t.Fatalf("artifact %+v", art)
	}
	for _, ref := range []string{"", "x", "artifact:../etc/passwd", "/etc/passwd", store.ArtifactRef("11111111-1111-4111-8111-111111111111")} {
		if _, _, err := f.opener().OpenArtifact(t.Context(), ref); err == nil {
			t.Errorf("ref %q opened", ref)
		}
	}
}

func TestSnapshotOpenerOnlyOpensReadyArtifacts(t *testing.T) {
	f := newOpenerFixture(t, "data")
	if _, err := f.st.QuarantineArtifact(t.Context(), f.art.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.opener().OpenArtifact(t.Context(), store.ArtifactRef(f.art.ID)); err == nil {
		t.Fatal("quarantined artifact opened")
	}
}

// The dispatcher-level contract: a failing opener fails the part (safe explicit
// refusal) while the already acknowledged text prefix is untouched and never
// resent.
func TestUploadPartFailsSafelyAfterAcknowledgedPrefix(t *testing.T) {
	f := newOpenerFixture(t, "data")
	if err := os.Remove(filepath.Join(f.blobPath(), f.art.BlobKey)); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{msg: &models.Message{ID: 7}}
	tr := &botTransport{api: api, artifacts: f.opener()}
	disp := NewOutboxDispatcher(mustOwner(t, f.st), tr, OutboxConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := disp.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer disp.Stop(5 * time.Second)
	disp.Notify(f.dispID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := f.st.GetDelivery(t.Context(), f.dispID)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Unresolved {
			if d.Parts[0].State != store.PartSent || d.Parts[1].State != store.PartFailed || d.State != store.DeliveryPartial {
				t.Fatalf("delivery %s parts %+v", d.State, d.Parts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery never resolved: %+v", d)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if api.sends != 1 || api.docs != 0 {
		t.Fatalf("sends=%d docs=%d: text must go once, the corrupt upload never", api.sends, api.docs)
	}
}

func mustOwner(t *testing.T, st *store.Store) *store.OutboxOwner {
	t.Helper()
	o, err := st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Release)
	return o
}
