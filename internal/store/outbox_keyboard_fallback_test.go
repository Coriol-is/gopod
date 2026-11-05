package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func keyboardFallbackFixture(t *testing.T) (*deliveryFixture, *OutboxOwner, Delivery, OutboxClaim) {
	t.Helper()
	f := topicDeliveryFixture(t)
	d, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: f.conv, Owner: OwnerControllerResult, Parts: []DeliveryPart{{Kind: PartText, Payload: []byte("<b>answer</b>"), ParseMode: "HTML", ReplyMarkup: `{"inline_keyboard":[[{"text":"more","callback_data":"mo:1"}]]}`, ReplyToMessageID: 13}}})
	if err != nil {
		t.Fatal(err)
	}
	o := f.owner()
	c, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	return f, o, d, c
}
func TestKeyboardFallbackCommitsTwoAttemptsAndKeepsImmutableSource(t *testing.T) {
	f, o, d, c := keyboardFallbackFixture(t)
	next, err := o.BeginKeyboardFallback(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !next.KeyboardFallback || next.Part.ParseMode != "HTML" || next.AttemptID == c.AttemptID || next.Attempt != 2 {
		t.Fatal(next)
	}
	if next.Part.ArtifactRef != c.Part.ArtifactRef || string(next.Part.Payload) != string(c.Part.Payload) || next.Part.ReplyToMessageID != 13 || next.Conversation != c.Conversation {
		t.Fatal("fallback changed immutable route/content")
	}
	var hash, outcome string
	var ended int64
	if err = f.st.db.QueryRow(`SELECT source_part_hash FROM outbox_keyboard_fallbacks WHERE part_id=?`, c.PartID).Scan(&hash); err != nil || hash != partHash(c.Ordinal, c.Part) {
		t.Fatal(hash, err)
	}
	if err = f.st.db.QueryRow(`SELECT outcome,ended_at FROM outbox_attempts WHERE attempt_id=?`, c.AttemptID).Scan(&outcome, &ended); err != nil || outcome != "refused" || ended == 0 {
		t.Fatal(outcome, ended, err)
	}
	original, err := f.st.GetDelivery(t.Context(), d.ID)
	if err != nil || original.Parts[0].Part.ReplyMarkup != c.Part.ReplyMarkup {
		t.Fatal(original, err)
	}
	if partHash(0, original.Parts[0].Part) != hash {
		t.Fatal("part hash changed")
	}
	if err = o.AckPart(t.Context(), next, 99); err != nil {
		t.Fatal(err)
	}
	diag, err := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if err != nil || diag.KeyboardFallbacks != 1 {
		t.Fatal(diag, err)
	}
	if _, err = f.st.db.Exec(`UPDATE outbox_keyboard_fallbacks SET policy='keyboard_bare_v1'`); err == nil {
		t.Fatal("mutable branch")
	}
	// The branch evidence follows the existing attempt-retention cascade.
	if _, err = f.st.db.Exec(`DELETE FROM outbox_attempts WHERE part_id=?`, c.PartID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM outbox_keyboard_fallbacks`); n != 0 {
		t.Fatal(n)
	}
}
func TestKeyboardFallbackRejectsUnsafeTransitions(t *testing.T) {
	for _, scenario := range []string{"scope", "topic", "budget", "expiry", "kind", "payload", "route", "fence", "second", "stale-claim"} {
		t.Run(scenario, func(t *testing.T) {
			f, o, _, c := keyboardFallbackFixture(t)
			switch scenario {
			case "scope":
				if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, Name: "changed", AddedAt: 1}); err != nil {
					t.Fatal(err)
				}
			case "topic":
				beginTopic(t, f.st, f.conv.Scope, 7)
			case "budget":
				if _, err := f.st.db.Exec(`UPDATE outbox_parts SET attempts=? WHERE part_id=?`, MaxPartAttempts, c.PartID); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				if _, err := o.beginKeyboardFallback(t.Context(), c, c.ExpiresAt); err == nil {
					t.Fatal("expired fallback")
				}
				return
			case "kind":
				c.Part.Kind = PartUpload
			case "payload":
				c.Part.Payload = []byte("different caption")
			case "route":
				c.Conversation.ThreadID = 9
			case "fence":
				c.Fence = "wrong"
			case "second":
				var err error
				c, err = o.BeginKeyboardFallback(t.Context(), c)
				if err != nil {
					t.Fatal(err)
				}
			case "stale-claim":
				if _, err := o.BeginKeyboardFallback(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := o.BeginKeyboardFallback(t.Context(), c); err == nil {
				t.Fatal("unsafe fallback accepted")
			}
		})
	}
}
func TestKeyboardFallbackSelectionRollbackAndConcurrentClaim(t *testing.T) {
	f, o, _, c := keyboardFallbackFixture(t)
	if _, err := f.st.db.Exec(`CREATE TRIGGER fail_branch BEFORE INSERT ON outbox_keyboard_fallbacks BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := o.BeginKeyboardFallback(t.Context(), c); err == nil {
		t.Fatal("expected rollback")
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM outbox_attempts`); n != 1 {
		t.Fatal(n)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM outbox_attempts WHERE ended_at IS NULL`); n != 1 {
		t.Fatal(n)
	}
	if _, err := f.st.db.Exec(`DROP TRIGGER fail_branch`); err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.BeginKeyboardFallback(t.Context(), c); err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrClaimLost) {
				t.Errorf("unexpected race error: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal(won.Load())
	}
}
func TestKeyboardFallbackRestartNeverReplaysButtons(t *testing.T) {
	for _, state := range []string{"sending", "known-unsent"} {
		t.Run(state, func(t *testing.T) {
			f, o, d, c := keyboardFallbackFixture(t)
			next, err := o.BeginKeyboardFallback(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			if state == "known-unsent" {
				if _, err = o.ReleaseUnsent(t.Context(), next, time.Now(), "rate_limited", "", false); err != nil {
					t.Fatal(err)
				}
			}
			path := f.st.path
			o.Release()
			if err = f.st.Close(); err != nil {
				t.Fatal(err)
			}
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
			if _, err = fresh.SweepSending(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, ok, err := fresh.ClaimNext(t.Context(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if state == "sending" {
				if ok {
					t.Fatal("replayed uncertain alternative")
				}
				diag, _ := st.DeliveryDiagnostic(t.Context(), d.ID)
				if diag.Parts[0].State != PartUncertain {
					t.Fatal(diag)
				}
			} else if !ok || !got.KeyboardFallback || got.Part.ParseMode != "HTML" || got.Attempt != 3 {
				t.Fatalf("wrong resumed branch: %+v %v", got, ok)
			}
		})
	}
}

func TestKeyboardFallbackMigrationKeepsOldKeyboardPlanAndHash(t *testing.T) {
	f, o, d, c := keyboardFallbackFixture(t)
	if _, err := o.ReleaseUnsent(t.Context(), c, time.Now(), "connect_failed", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`DROP TABLE outbox_keyboard_fallbacks`); err != nil {
		t.Fatal(err)
	}
	path := f.st.path
	o.Release()
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.GetDelivery(t.Context(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var storedHash string
	if err = st.db.QueryRow(`SELECT part_hash FROM outbox_parts WHERE part_id=?`, c.PartID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != partHash(c.Ordinal, c.Part) || partHash(0, got.Parts[0].Part) != storedHash {
		t.Fatal("migration rewrote legacy source")
	}
	if n := countRows(t, st, `SELECT count(*) FROM outbox_keyboard_fallbacks`); n != 0 {
		t.Fatal("invented branch", n)
	}
	fresh, err := st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	claim, ok, err := fresh.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok || claim.KeyboardFallback || claim.Part.ReplyMarkup != c.Part.ReplyMarkup {
		t.Fatal(claim, ok, err)
	}
}
func TestKeyboardFallbackCorruptBranchFailsClosed(t *testing.T) {
	f, o, _, c := keyboardFallbackFixture(t)
	next, err := o.BeginKeyboardFallback(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.ReleaseUnsent(t.Context(), next, time.Now(), "rate_limited", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.Exec(`DROP TRIGGER outbox_keyboard_fallback_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.Exec(`UPDATE outbox_keyboard_fallbacks SET source_part_hash='corrupt'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.ClaimNext(t.Context(), time.Now()); ok || !errors.Is(err, ErrInvalidDelivery) {
		t.Fatal(ok, err)
	}
}
