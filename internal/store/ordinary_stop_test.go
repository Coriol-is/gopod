package store

import (
	"errors"
	"sync"
	"testing"
)

func stopFixture(t *testing.T) (*deliveryFixture, OrdinaryStopBinding) {
	f := newDeliveryFixture(t)
	a, b := f.turn("telegram", "stop-a", 0, 1), f.turn("telegram", "stop-b", 0, 1)
	return f, OrdinaryStopBinding{Conversation: f.conv, Members: []ResultMember{{TurnID: a, Attempt: 1}, {TurnID: b, Attempt: 1}}}
}
func TestOrdinaryStopSurvivesStatusRewriteAndLateFreeze(t *testing.T) {
	f, b := stopFixture(t)
	id, err := f.st.RecordOrdinaryStop(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.st.RecordOrdinaryStop(t.Context(), b); err != nil || again != id {
		t.Fatal(again, err)
	}
	r := f.freeze(ExecInteractive, "late result", b.Members...)
	if err := f.st.RecordStopCompletion(t.Context(), id, r.Text, "returned"); err != nil {
		t.Fatal(err)
	}
	for _, m := range b.Members {
		if err := f.st.MarkTurnFinished(t.Context(), m.TurnID, TurnRunning, ""); err != nil {
			t.Fatal(err)
		}
	}
	path := f.st.path
	f.st.Close()
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if found, yes, err := st.FindOrdinaryStop(t.Context(), b); err != nil || !yes || found != id {
		t.Fatal(found, yes, err)
	}
	if _, err := st.PublishDelivery(t.Context(), PublishRequest{ResultID: r.ID, Conversation: f.conv, Owner: OwnerControllerResult, Parts: []DeliveryPart{{Kind: PartText, Payload: []byte("answer")}}}); !errors.Is(err, ErrResultStopped) {
		t.Fatal(err)
	}
	if _, err := st.StartTurnBatch(t.Context(), []TurnStart{{ID: b.Members[0].TurnID, Folder: "owner", Source: "telegram", Attempts: 1}}); err == nil {
		t.Fatal("restarted stopped attempt")
	}
	for _, m := range b.Members {
		rec, err := st.RecoverOrdinaryTurn(t.Context(), m.TurnID)
		if err != nil || rec.Action != OrdinarySettled {
			t.Fatal(rec, err)
		}
	}
	var text string
	if err := st.db.QueryRow(`SELECT result_text FROM ordinary_stop_completions WHERE intent_id=?`, id).Scan(&text); err != nil || text != "late result" {
		t.Fatal(text, err)
	}
	for _, table := range []string{"ordinary_stop_intents", "ordinary_stop_members", "ordinary_stop_completions"} {
		if _, err := st.db.Exec("DELETE FROM " + table); err == nil {
			t.Fatal("deletable", table)
		}
	}
}
func TestOrdinaryStopValidationIsAtomic(t *testing.T) {
	for _, variant := range []string{"attempt", "scope", "stale_scope", "duplicate", "task", "partial"} {
		t.Run(variant, func(t *testing.T) {
			f, b := stopFixture(t)
			switch variant {
			case "attempt":
				b.Members[1].Attempt++
			case "scope":
				b.Conversation.ThreadID = 4
			case "stale_scope":
				b.Conversation.Scope.NamespaceEpoch++
			case "duplicate":
				b.Members[1] = b.Members[0]
			case "task":
				_, err := f.st.db.Exec(`UPDATE turns SET source='task' WHERE id=?`, b.Members[1].TurnID)
				if err != nil {
					t.Fatal(err)
				}
			case "partial":
				if _, err := f.st.RecordOrdinaryStop(t.Context(), b); err != nil {
					t.Fatal(err)
				}
				b.Members = b.Members[:1]
			}
			if _, err := f.st.RecordOrdinaryStop(t.Context(), b); err == nil {
				t.Fatal("accepted invalid binding")
			}
			want := 0
			if variant == "partial" {
				want = 2
			}
			if n := countRows(t, f.st, `SELECT count(*) FROM ordinary_stop_members`); n != want {
				t.Fatal(n)
			}
		})
	}
}
func TestOrdinaryStopAndPublicationHaveOneWinner(t *testing.T) {
	for range 20 {
		f, b := stopFixture(t)
		r := f.freeze(ExecInteractive, "answer", b.Members...)
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		var stopErr, publishErr error
		go func() { defer wg.Done(); <-start; _, stopErr = f.st.RecordOrdinaryStop(t.Context(), b) }()
		go func() {
			defer wg.Done()
			<-start
			_, publishErr = f.st.PublishDelivery(t.Context(), PublishRequest{ResultID: r.ID, Conversation: f.conv, Owner: OwnerControllerResult, Parts: []DeliveryPart{{Kind: PartText, Payload: []byte("answer")}}})
		}()
		close(start)
		wg.Wait()
		if stopErr == nil {
			if !errors.Is(publishErr, ErrResultStopped) {
				t.Fatal(stopErr, publishErr)
			}
		} else if publishErr != nil || !errors.Is(stopErr, ErrStopPublished) {
			t.Fatal(stopErr, publishErr)
		}
	}
}
func TestOrdinaryStopOldSchemaUpgrade(t *testing.T) {
	f, b := stopFixture(t)
	path := f.st.path
	for _, table := range []string{"ordinary_stop_completions", "ordinary_stop_members", "ordinary_stop_intents"} {
		if _, err := f.st.db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	f.st.Close()
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.RecordOrdinaryStop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM turns`); n != 2 {
		t.Fatal(n)
	}
}

func TestOrdinaryStopPublicationFirstRejectsIntent(t *testing.T) {
	f, b := stopFixture(t)
	r := f.freeze(ExecInteractive, "answer", b.Members...)
	original, err := f.st.PublishDelivery(t.Context(), PublishRequest{ResultID: r.ID, Conversation: f.conv, Owner: OwnerControllerResult, Parts: []DeliveryPart{{Kind: PartText, Payload: []byte("answer")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecordOrdinaryStop(t.Context(), b); !errors.Is(err, ErrStopPublished) {
		t.Fatal(err)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM ordinary_stop_intents`); n != 0 {
		t.Fatal(n)
	}
	actual, err := readDelivery(t.Context(), f.st.taskDB(t.Context()), original.ID)
	if err != nil || actual.State != original.State || actual.PlanHash != original.PlanHash {
		t.Fatal(actual, err)
	}
}
