package store

import (
	"errors"
	"sync"
	"testing"
)

func finalFixture(t *testing.T) (*deliveryFixture, TurnResult, FinalReplyPlan) {
	t.Helper()
	f := newDeliveryFixture(t)
	r := f.freeze(ExecInteractive, "answer", ResultMember{TurnID: f.turn("telegram", "final:1", 0, 1), Attempt: 1})
	if _, err := f.st.BeginFinalReply(t.Context(), r.ID, []byte(`{"mode":"text"}`)); err != nil {
		t.Fatal(err)
	}
	return f, r, FinalReplyPlan{Request: PublishRequest{ResultID: r.ID, Conversation: f.conv, Owner: OwnerControllerResult, Parts: []DeliveryPart{{Kind: PartText, Payload: []byte("answer"), ReplyToMessageID: 42}}}}
}
func TestFinalReplyImmutableRecovery(t *testing.T) {
	f, r, p := finalFixture(t)
	if _, err := f.st.BeginFinalReply(t.Context(), r.ID, []byte(`{"mode":"voice"}`)); !errors.Is(err, ErrFinalReplyConflict) {
		t.Fatal(err)
	}
	if _, err := f.st.PublishReadyFinalReply(t.Context(), r.ID); !errors.Is(err, ErrFinalReplyNotReady) {
		t.Fatal(err)
	}
	if _, err := f.st.StageFinalReply(t.Context(), r.ID, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.StageFinalReply(t.Context(), r.ID, p); err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.Request.Parts = []DeliveryPart{{Kind: PartText, Payload: []byte("changed")}}
	if _, err := f.st.StageFinalReply(t.Context(), r.ID, changed); !errors.Is(err, ErrFinalReplyConflict) {
		t.Fatal(err)
	}
	path := f.st.path
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids, err := st.ReadyFinalReplies(t.Context(), "", 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("%v %v", ids, err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, e := st.PublishReadyFinalReply(t.Context(), r.ID)
			if e != nil || len(d.Parts) != 1 || d.Parts[0].Part.ReplyToMessageID != 42 {
				t.Errorf("%+v %v", d, e)
			}
		}()
	}
	wg.Wait()
	if n := countRows(t, st, `SELECT count(*) FROM delivery_groups`); n != 1 {
		t.Fatal(n)
	}
	if _, err := st.db.Exec(`UPDATE outbox_parts SET reply_to_message_id=43`); err == nil {
		t.Fatal("mutable reply target")
	}
}
func TestFinalReplyMultiplePreparationRollback(t *testing.T) {
	f, r, p := finalFixture(t)
	for range 2 {
		a := f.reserve(1, 10)
		f.ready(a, 3)
		p.Preparations = append(p.Preparations, a.ID)
		p.Request.Parts = append(p.Request.Parts, DeliveryPart{Kind: PartUpload, ArtifactRef: ArtifactRef(a.ArtifactIDs[0])})
	}
	if _, err := f.st.StageFinalReply(t.Context(), r.ID, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`CREATE TRIGGER fail_final BEFORE INSERT ON artifact_refs BEGIN SELECT RAISE(ABORT,'fail'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PublishReadyFinalReply(t.Context(), r.ID); err == nil {
		t.Fatal("expected rollback")
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM delivery_groups`); n != 0 {
		t.Fatal(n)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM artifact_reservations WHERE state='active'`); n != 2 {
		t.Fatal(n)
	}
	i, _ := f.st.GetFinalReply(t.Context(), r.ID)
	if i.State != FinalReplyReady {
		t.Fatal(i.State)
	}
	if _, err := f.st.db.Exec(`DROP TRIGGER fail_final`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PublishReadyFinalReply(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM artifact_refs`); n != 2 {
		t.Fatal(n)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM artifact_reservations WHERE state='active'`); n != 0 {
		t.Fatal(n)
	}
}
func TestFinalReplyRejectsMalformedMembership(t *testing.T) {
	for _, kind := range []string{"duplicate-prep", "duplicate-ref", "unused", "missing", "voice-unverified", "scope"} {
		t.Run(kind, func(t *testing.T) {
			f, r, p := finalFixture(t)
			a := f.reserve(1, 10)
			f.ready(a, 3)
			p.Preparations = []string{a.ID}
			p.Request.Parts = append(p.Request.Parts, DeliveryPart{Kind: PartUpload, ArtifactRef: ArtifactRef(a.ArtifactIDs[0])})
			switch kind {
			case "duplicate-prep":
				p.Preparations = append(p.Preparations, a.ID)
			case "duplicate-ref":
				p.Request.Parts = append(p.Request.Parts, p.Request.Parts[1])
			case "unused":
				p.Request.Parts = p.Request.Parts[:1]
			case "missing":
				p.Preparations = nil
			case "voice-unverified":
				p.Request.Parts[1].ParseMode = UploadPresentationVoice
			case "scope":
				p.Request.Conversation.Scope.NamespaceEpoch++
			}
			if _, err := f.st.StageFinalReply(t.Context(), r.ID, p); err == nil {
				t.Fatal("accepted invalid plan")
			}
			i, _ := f.st.GetFinalReply(t.Context(), r.ID)
			if i.State != FinalReplyPreparing {
				t.Fatal(i.State)
			}
		})
	}
}

func TestFinalReplyBackupEvidenceAndSchemaRepeat(t *testing.T) {
	f, r, p := finalFixture(t)
	for range 2 {
		if err := f.st.applyFinalReplySchema(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err := ReadWorkEvidence(t.Context(), f.st.path)
	if err != nil || evidence.FinalRepliesPending != 1 {
		t.Fatalf("%+v %v", evidence, err)
	}
	if _, err = f.st.StageFinalReply(t.Context(), r.ID, p); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.PublishReadyFinalReply(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	evidence, err = ReadWorkEvidence(t.Context(), f.st.path)
	if err != nil || evidence.FinalRepliesPending != 0 {
		t.Fatalf("%+v %v", evidence, err)
	}
}

func TestFinalReplyExternalPublicationIsDistinctFromCorruption(t *testing.T) {
	f, r, p := finalFixture(t)
	if _, err := f.st.StageFinalReply(t.Context(), r.ID, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PublishDelivery(t.Context(), p.Request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PublishReadyFinalReply(t.Context(), r.ID); !errors.Is(err, ErrFinalReplyPublishedElsewhere) {
		t.Fatal(err)
	}
	i, _ := f.st.GetFinalReply(t.Context(), r.ID)
	if i.State != FinalReplyReady {
		t.Fatal(i)
	}
}

func TestFinalReplyMigrationPreservesOldPopulatedOutboxHash(t *testing.T) {
	f := newDeliveryFixture(t)
	part := DeliveryPart{Kind: PartText, Payload: []byte("old final"), ParseMode: "HTML"}
	oldHash := newCanonicalHasher("gopod.delivery-part.v1").int(0).str(string(part.Kind)).bytes(part.Payload).str(part.ArtifactRef).str(part.ParseMode).str(part.ReplyMarkup).int(part.TargetMessageID).sum()
	if partHash(0, part) != oldHash {
		t.Fatal("zero target changed legacy hash")
	}
	d, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC, Parts: []DeliveryPart{part}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TRIGGER outbox_reply_target_immutable`, `ALTER TABLE outbox_parts DROP COLUMN reply_to_message_id`, `DROP TABLE final_reply_intents`} {
		if _, err = f.st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	path := f.st.path
	if err = f.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.GetDelivery(t.Context(), d.ID)
	if err != nil || len(got.Parts) != 1 || got.Parts[0].Part.ReplyToMessageID != 0 || string(got.Parts[0].Part.Payload) != "old final" {
		t.Fatalf("%+v %v", got, err)
	}
	var stored string
	if err = st.db.QueryRow(`SELECT part_hash FROM outbox_parts WHERE part_id=?`, got.Parts[0].ID).Scan(&stored); err != nil || stored != oldHash {
		t.Fatalf("%s %v", stored, err)
	}
}
