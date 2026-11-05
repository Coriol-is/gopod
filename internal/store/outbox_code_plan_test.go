package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func codePlanFixture(t *testing.T, n int) (*deliveryFixture, TurnResult, FinalReplyPlan) {
	t.Helper()
	f, r, p := finalFixture(t)
	p.Request.Parts = nil
	a := &CodeAlternatives{Version: 1, Prefix: n}
	for i := 0; i < n; i++ {
		prep := f.reserve(1, 100)
		f.ready(prep, 10)
		p.Preparations = append(p.Preparations, prep.ID)
		p.Request.Parts = append(p.Request.Parts, DeliveryPart{Kind: PartUpload, ArtifactRef: ArtifactRef(prep.ArtifactIDs[0])})
		a.Documents = append(a.Documents, i)
	}
	for mask := 0; mask < 1<<n; mask++ {
		a.Variants = append(a.Variants, []DeliveryPart{{Kind: PartText, Payload: []byte(fmt.Sprintf("variant %d", mask)), ReplyToMessageID: 42}})
	}
	p.Request.Parts = append(p.Request.Parts, a.Variants[len(a.Variants)-1]...)
	p.Request.CodeAlternatives = a
	return f, r, p
}
func publishCodeFixture(t *testing.T, f *deliveryFixture, r TurnResult, p FinalReplyPlan) Delivery {
	t.Helper()
	if _, err := f.st.StageFinalReply(t.Context(), r.ID, p); err != nil {
		t.Fatal(err)
	}
	d, err := f.st.PublishReadyFinalReply(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestCodePlanEveryMaskAndCompletion(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			f, r, p := codePlanFixture(t, 4)
			d := publishCodeFixture(t, f, r, p)
			o := f.owner()
			for i := 0; i < 4; i++ {
				c, ok, err := o.ClaimNext(t.Context(), time.Now())
				if err != nil || !ok || c.Ordinal != i {
					t.Fatal(c, ok, err)
				}
				if mask&(1<<i) != 0 {
					err = o.AckPart(t.Context(), c, int64(i+10))
				} else {
					var handled bool
					handled, err = o.RefuseCodeDocument(t.Context(), c, "class=refused code=document_refused http=400")
					if !handled {
						t.Fatal("not handled", err)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			held, _ := f.st.GetDelivery(t.Context(), d.ID)
			if !held.Unresolved || held.State == DeliverySent || len(held.Parts) != 4 {
				t.Fatal(held)
			}
			c, ok, err := o.ClaimNext(t.Context(), time.Now())
			if err != nil || !ok || string(c.Part.Payload) != fmt.Sprintf("variant %d", mask) {
				t.Fatal(c, ok, err)
			}
			if err = o.AckPart(t.Context(), c, 99); err != nil {
				t.Fatal(err)
			}
			done, _ := f.st.GetDelivery(t.Context(), d.ID)
			if done.Unresolved || done.State != DeliverySent || done.PlanHash != d.PlanHash {
				t.Fatal(done)
			}
			if _, ok, err = o.ClaimNext(t.Context(), time.Now()); err != nil || ok {
				t.Fatal(ok, err)
			}
			if _, err = f.st.db.Exec(`UPDATE outbox_code_plans SET selected_mask=0`); err == nil {
				t.Fatal("selection mutable")
			}
			row, err := f.st.GetTurn(t.Context(), r.Members[0].TurnID)
			if err != nil || row.ReplyMsgID != 99 {
				t.Fatal(row, err)
			}
		})
	}
}
func TestCodePlanHoldsUnknownAndBlocksLaterDelivery(t *testing.T) {
	for _, state := range []string{"uncertain", "failed", "retry", "lost_ack"} {
		t.Run(state, func(t *testing.T) {
			f, r, p := codePlanFixture(t, 1)
			d := publishCodeFixture(t, f, r, p)
			o := f.owner()
			c, _, _ := o.ClaimNext(t.Context(), time.Now())
			switch state {
			case "uncertain":
				_ = o.MarkUncertain(t.Context(), c, "timeout", "")
			case "failed":
				_ = o.FailPart(t.Context(), c, "bad_request", "")
			case "retry":
				_, _ = o.ReleaseUnsent(t.Context(), c, time.Now().Add(time.Hour), "rate_limited", "", false)
			case "lost_ack":
				_, _ = o.SweepSending(t.Context())
			}
			req := p.Request
			req.ResultID = ""
			req.CodeAlternatives = nil
			req.Parts = []DeliveryPart{{Kind: PartText, Payload: []byte("later")}}
			if _, err := f.st.PublishDelivery(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := o.ClaimNext(t.Context(), time.Now()); err != nil || ok {
				t.Fatal(ok, err)
			}
			got, _ := f.st.GetDelivery(t.Context(), d.ID)
			if !got.Unresolved || len(got.Parts) != 1 {
				t.Fatal(got)
			}
		})
	}
}
func TestCodePlanSelectionRollbackAndReopen(t *testing.T) {
	f, r, p := codePlanFixture(t, 1)
	d := publishCodeFixture(t, f, r, p)
	o := f.owner()
	c, _, _ := o.ClaimNext(t.Context(), time.Now())
	if _, err := o.RefuseCodeDocument(t.Context(), c, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`CREATE TRIGGER selection_fault BEFORE UPDATE OF selected_mask ON outbox_code_plans BEGIN SELECT RAISE(ABORT,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.ClaimNext(t.Context(), time.Now()); err == nil {
		t.Fatal("expected fault")
	}
	got, _ := f.st.GetDelivery(t.Context(), d.ID)
	if len(got.Parts) != 1 {
		t.Fatal("partial suffix insert")
	}
	_, _ = f.st.db.Exec(`DROP TRIGGER selection_fault`)
	path := f.st.path
	o.Release()
	_ = f.st.Close()
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fresh, err := st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	c, ok, err := fresh.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok || string(c.Part.Payload) != "variant 0" {
		t.Fatal(c, ok, err)
	}
	if _, err = fresh.SweepSending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = fresh.ClaimNext(t.Context(), time.Now()); err != nil || ok {
		t.Fatal("replayed selected text", ok, err)
	}
}
func TestCodePlanCancellationAndExpiry(t *testing.T) {
	for _, op := range []string{"cancel", "expire"} {
		t.Run(op, func(t *testing.T) {
			f, r, p := codePlanFixture(t, 1)
			d := publishCodeFixture(t, f, r, p)
			o := f.owner()
			c, _, _ := o.ClaimNext(t.Context(), time.Now())
			_, _ = o.RefuseCodeDocument(t.Context(), c, "")
			at := time.Now()
			if op == "cancel" {
				if _, err := f.st.CancelDelivery(t.Context(), d.ID, "stopped"); err != nil {
					t.Fatal(err)
				}
			} else {
				at = at.Add(48 * time.Hour)
			}
			if _, ok, err := o.ClaimNext(t.Context(), at); err != nil || ok {
				t.Fatal(ok, err)
			}
			got, _ := f.st.GetDelivery(t.Context(), d.ID)
			if got.Unresolved || len(got.Parts) != 1 {
				t.Fatal(got)
			}
		})
	}
}
func TestCodePlanValidationAndScope(t *testing.T) {
	for _, change := range []string{"duplicates", "too_many", "missing_variant", "upload_suffix", "wrong_base", "oversize", "bad_route", "forged_claim", "fence"} {
		t.Run(change, func(t *testing.T) {
			f, r, p := codePlanFixture(t, 1)
			a := p.Request.CodeAlternatives
			switch change {
			case "duplicates":
				a.Documents = []int{0, 0}
			case "too_many":
				a.Documents = make([]int, 5)
			case "missing_variant":
				a.Variants = a.Variants[:1]
			case "upload_suffix":
				a.Variants[0][0] = p.Request.Parts[0]
			case "wrong_base":
				p.Request.Parts[1].Payload = []byte("forged")
			case "oversize":
				a.Variants[0][0].Payload = []byte(strings.Repeat("x", MaxCodePlanBytes))
			}
			if change == "bad_route" || change == "forged_claim" || change == "fence" {
				_ = publishCodeFixture(t, f, r, p)
				o := f.owner()
				c, _, _ := o.ClaimNext(t.Context(), time.Now())
				switch change {
				case "bad_route":
					c.Conversation.ThreadID++
				case "forged_claim":
					c.Part.Payload = []byte("wrong")
				case "fence":
					_ = f.st.SetState(t.Context(), outboxFenceKey, "other")
				}
				if _, err := o.RefuseCodeDocument(t.Context(), c, ""); err == nil {
					t.Fatal("accepted unsafe claim")
				}
				return
			}
			if _, err := f.st.StageFinalReply(t.Context(), r.ID, p); !errors.Is(err, ErrInvalidDelivery) {
				t.Fatal(err)
			}
		})
	}
}

func TestCodePlanAcknowledgedPrefixNeverCompletesOnCancel(t *testing.T) {
	for _, op := range []string{"cancel", "expire"} {
		t.Run(op, func(t *testing.T) {
			f, r, p := codePlanFixture(t, 1)
			d := publishCodeFixture(t, f, r, p)
			o := f.owner()
			c, _, _ := o.ClaimNext(t.Context(), time.Now())
			if err := o.AckPart(t.Context(), c, 12); err != nil {
				t.Fatal(err)
			}
			at := time.Now()
			if op == "cancel" {
				_, _ = f.st.CancelDelivery(t.Context(), d.ID, "stopped")
			} else {
				at = at.Add(48 * time.Hour)
			}
			if _, ok, err := o.ClaimNext(t.Context(), at); err != nil || ok {
				t.Fatal(ok, err)
			}
			got, _ := f.st.GetDelivery(t.Context(), d.ID)
			if got.Unresolved || got.State == DeliverySent || len(got.Parts) != 1 {
				t.Fatal(got)
			}
			turn, _ := f.st.GetTurn(t.Context(), r.Members[0].TurnID)
			if turn.ReplyMsgID != 0 {
				t.Fatal("false reply memo")
			}
		})
	}
}
func TestCodePlanRetentionAndHashIntegrity(t *testing.T) {
	f, r, p := codePlanFixture(t, 1)
	d := publishCodeFixture(t, f, r, p)
	o := f.owner()
	c, _, _ := o.ClaimNext(t.Context(), time.Now())
	_, _ = o.RefuseCodeDocument(t.Context(), c, "")
	if _, err := f.st.db.Exec(`UPDATE outbox_code_plans SET manifest=NULL`); err == nil {
		t.Fatal("cleared unresolved manifest")
	}
	c, _, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = o.AckPart(t.Context(), c, 42); err != nil {
		t.Fatal(err)
	}
	diag, err := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if err != nil || diag.CodeInlineFallbacks != 1 {
		t.Fatal(diag, err)
	}
	if _, err = f.st.CompactTerminalDeliveries(t.Context(), time.Now().Add(48*time.Hour), time.Hour, 100); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = f.st.db.QueryRow(`SELECT count(*) FROM outbox_code_plans WHERE manifest IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = f.st.db.QueryRow(`SELECT count(*) FROM final_reply_intents WHERE plan_json IS NULL AND state='published'`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	again, err := f.st.PublishReadyFinalReply(t.Context(), r.ID)
	if err != nil || again.ID != d.ID || again.PlanHash != d.PlanHash {
		t.Fatal(again, err)
	}
}

func TestCodePlanSelectionDoesNotStarveBehindFencedTopics(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.SetArtifactLimits(ArtifactLimits{MaxHourFiles: 200}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetOutboxLimits(OutboxLimits{MaxUnresolvedGlobal: 200, MaxUnresolvedPerNamespace: 200, MaxRejectionsGlobal: 1000, MaxRejectionsPerNamespace: 1000}); err != nil {
		t.Fatal(err)
	}
	o := f.owner()
	for thread := 1; thread <= 101; thread++ {
		f.conv.ThreadID = thread
		r := f.freeze(ExecInteractive, "code", ResultMember{TurnID: f.turn("telegram", fmt.Sprint(thread), thread, 1), Attempt: 1})
		if _, err := f.st.BeginFinalReply(t.Context(), r.ID, []byte(`{"version":2}`)); err != nil {
			t.Fatal(err)
		}
		prep := f.reserve(1, 100)
		f.ready(prep, 10)
		p := FinalReplyPlan{Preparations: []string{prep.ID}, Request: PublishRequest{ResultID: r.ID, Owner: OwnerControllerResult, Conversation: f.conv, Parts: []DeliveryPart{{Kind: PartUpload, ArtifactRef: ArtifactRef(prep.ArtifactIDs[0])}, {Kind: PartText, Payload: []byte("ok")}}, CodeAlternatives: &CodeAlternatives{Version: 1, Prefix: 1, Documents: []int{0}, Variants: [][]DeliveryPart{{{Kind: PartText, Payload: []byte("inline")}}, {{Kind: PartText, Payload: []byte("ok")}}}}}}
		_ = publishCodeFixture(t, f, r, p)
		c, ok, err := o.ClaimNext(t.Context(), time.Now())
		if err != nil || !ok || c.Part.Kind != PartUpload {
			t.Fatal(thread, c, ok, err)
		}
		if err = o.AckPart(t.Context(), c, int64(thread)); err != nil {
			t.Fatal(err)
		}
		if thread <= 100 {
			beginTopic(t, f.st, f.conv.Scope, thread)
		}
	}
	c, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok || c.Conversation.ThreadID != 101 || c.Part.Kind != PartText {
		t.Fatal(c, ok, err)
	}
}

func TestCodePlanConcurrentSelectionAndCorruption(t *testing.T) {
	f, r, p := codePlanFixture(t, 1)
	d := publishCodeFixture(t, f, r, p)
	o := f.owner()
	c, _, _ := o.ClaimNext(t.Context(), time.Now())
	_, _ = o.RefuseCodeDocument(t.Context(), c, "")
	var wg sync.WaitGroup
	var claims atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := o.ClaimNext(t.Context(), time.Now())
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatal(claims.Load())
	}
	got, _ := f.st.GetDelivery(t.Context(), d.ID)
	if len(got.Parts) != 2 {
		t.Fatal("duplicate suffix")
	}
	// Unknown selected suffix cannot be replayed after restart sweep.
	_, _ = o.SweepSending(t.Context())
	if _, ok, err := o.ClaimNext(t.Context(), time.Now()); err != nil || ok {
		t.Fatal(ok, err)
	}
}
func TestCodePlanCorruptManifestFailsClosed(t *testing.T) {
	f, r, p := codePlanFixture(t, 1)
	_ = publishCodeFixture(t, f, r, p)
	o := f.owner()
	c, _, _ := o.ClaimNext(t.Context(), time.Now())
	_ = o.AckPart(t.Context(), c, 10)
	_, _ = f.st.db.Exec(`DROP TRIGGER outbox_code_manifest_immutable`)
	_, _ = f.st.db.Exec(`UPDATE outbox_code_plans SET manifest='{}'`)
	if _, ok, err := o.ClaimNext(t.Context(), time.Now()); ok || !errors.Is(err, ErrInvalidDelivery) {
		t.Fatal(ok, err)
	}
}
