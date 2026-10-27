package store

import (
	"errors"
	"testing"
)

func historyCount(t *testing.T, st *Store) int {
	return countRows(t, st, `SELECT count(*) FROM conversation_history`)
}

func TestFreezeInteractiveAppendsContinuityOnce(t *testing.T) {
	f := newDeliveryFixture(t)
	turn := f.turn("telegram", "1:1", 0, 1)
	m := ResultMember{TurnID: turn, Attempt: 1}

	res := f.freeze(ExecInteractive, "final answer", m)
	if !res.Created || res.Authority != "model_reported" || res.TextHash != sha256Hex([]byte("final answer")) {
		t.Fatalf("unexpected result %+v", res)
	}
	if historyCount(t, f.st) != 1 {
		t.Fatalf("continuity not appended exactly once: %d", historyCount(t, f.st))
	}
	if v, _ := f.st.GetState(t.Context(), continuityKey("owner", 0, "provider")); v != "claude" {
		t.Fatalf("provider marker %q", v)
	}
	again := f.freeze(ExecInteractive, "final answer", m)
	if again.ID != res.ID || again.Created || historyCount(t, f.st) != 1 {
		t.Fatalf("replay appended or created: %+v history=%d", again, historyCount(t, f.st))
	}
	_, err := f.st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: f.conv, Kind: ExecInteractive, Members: []ResultMember{m}, Provider: "claude"}, "different")
	if !errors.Is(err, ErrResultConflict) {
		t.Fatalf("changed text accepted: %v", err)
	}
	if historyCount(t, f.st) != 1 {
		t.Fatal("conflicting freeze changed history")
	}
}

func TestCoalescedTurnsShareOneResult(t *testing.T) {
	f := newDeliveryFixture(t)
	a, b := f.turn("telegram", "1:1", 0, 1), f.turn("telegram", "1:2", 0, 1)
	res := f.freeze(ExecInteractive, "both", ResultMember{TurnID: b, Attempt: 1}, ResultMember{TurnID: a, Attempt: 1})
	if len(res.Members) != 2 || res.Members[0].TurnID != a {
		t.Fatalf("members %+v", res.Members)
	}
	for _, id := range []int64{a, b} {
		got, ok, err := f.st.TurnResultForAttempt(t.Context(), id, 1)
		if err != nil || !ok || got.ID != res.ID {
			t.Fatalf("turn %d not mapped to shared result: %v %v", id, ok, err)
		}
	}
	if historyCount(t, f.st) != 1 {
		t.Fatal("coalesced result appended continuity more than once")
	}
	// Overlapping a frozen member with a new one is a conflict, not a second result.
	c := f.turn("telegram", "1:3", 0, 1)
	_, err := f.st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: f.conv, Kind: ExecInteractive, Provider: "claude",
		Members: []ResultMember{{TurnID: a, Attempt: 1}, {TurnID: c, Attempt: 1}}}, "both")
	if !errors.Is(err, ErrResultConflict) {
		t.Fatalf("partial overlap accepted: %v", err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM turn_results`) != 1 {
		t.Fatal("partial overlap left a second result")
	}
}

func TestIsolatedTaskResultHasNoForegroundEffects(t *testing.T) {
	f := newDeliveryFixture(t)
	foreground := f.turn("telegram", "1:1", 0, 1)
	f.freeze(ExecInteractive, "chat answer", ResultMember{TurnID: foreground, Attempt: 1})
	historyBefore := historyCount(t, f.st)
	providerBefore, _ := f.st.GetState(t.Context(), continuityKey("owner", 0, "provider"))
	if _, err := f.st.db.ExecContext(t.Context(), `UPDATE router_state SET value='codex' WHERE key=?`, continuityKey("owner", 0, "provider")); err != nil {
		t.Fatal(err)
	}

	task := f.turn("task", "t1:100", 0, 1)
	res := f.freeze(ExecIsolatedTask, "task output", ResultMember{TurnID: task, Attempt: 1})
	if res.Kind != ExecIsolatedTask {
		t.Fatalf("kind %q", res.Kind)
	}
	if historyCount(t, f.st) != historyBefore {
		t.Fatal("isolated task appended foreground history")
	}
	if v, _ := f.st.GetState(t.Context(), continuityKey("owner", 0, "provider")); v != "codex" || providerBefore != "claude" {
		t.Fatalf("isolated task moved the continuity marker: %q", v)
	}
	if turn, _ := f.st.GetTurn(t.Context(), task); turn.ReplyMsgID != 0 {
		t.Fatalf("isolated task got reply_msg_id %d", turn.ReplyMsgID)
	}
	// A task turn can never be labelled interactive or the other way round.
	other := f.turn("task", "t1:101", 0, 1)
	_, err := f.st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: f.conv, Kind: ExecInteractive, Provider: "claude", Members: []ResultMember{{TurnID: other, Attempt: 1}}}, "x")
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("task turn frozen as interactive: %v", err)
	}
	_, err = f.st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: f.conv, Kind: ExecIsolatedTask, Provider: "claude", Members: []ResultMember{{TurnID: foreground, Attempt: 1}}}, "x")
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("telegram turn frozen as task: %v", err)
	}
}

func TestFreezeRollbackLeavesNothing(t *testing.T) {
	f := newDeliveryFixture(t)
	turn := f.turn("telegram", "1:1", 0, 1)
	// Fail the continuity append, the last step, after the result and members were inserted.
	if _, err := f.st.db.ExecContext(t.Context(), `CREATE TRIGGER boom BEFORE INSERT ON conversation_history BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: f.conv, Kind: ExecInteractive, Provider: "claude", Members: []ResultMember{{TurnID: turn, Attempt: 1}}}, "x")
	if err == nil {
		t.Fatal("expected failure")
	}
	if countRows(t, f.st, `SELECT count(*) FROM turn_results`)+countRows(t, f.st, `SELECT count(*) FROM turn_result_members`) != 0 {
		t.Fatal("partial result persisted")
	}
}

func TestFreezeValidationAndScope(t *testing.T) {
	f := newDeliveryFixture(t)
	turn := f.turn("telegram", "1:1", 0, 1)
	topic := f.turn("telegram", "1:2", 5, 1)
	good := ExecutionRef{Conversation: f.conv, Kind: ExecInteractive, Provider: "claude", Members: []ResultMember{{TurnID: turn, Attempt: 1}}}
	cases := map[string]func(*ExecutionRef){
		"no members":     func(r *ExecutionRef) { r.Members = nil },
		"zero attempt":   func(r *ExecutionRef) { r.Members = []ResultMember{{TurnID: turn}} },
		"attempt 99":     func(r *ExecutionRef) { r.Members = []ResultMember{{TurnID: turn, Attempt: 99}} },
		"attempt beyond": func(r *ExecutionRef) { r.Members = []ResultMember{{TurnID: turn, Attempt: 2}} },
		"negative":       func(r *ExecutionRef) { r.Members = []ResultMember{{TurnID: turn, Attempt: -1}} },
		"duplicate":      func(r *ExecutionRef) { r.Members = append(r.Members, r.Members[0]) },
		"no provider":    func(r *ExecutionRef) { r.Provider = "" },
		"unknown kind":   func(r *ExecutionRef) { r.Kind = "batch" },
		"missing turn":   func(r *ExecutionRef) { r.Members = []ResultMember{{TurnID: 999, Attempt: 1}} },
		"other thread":   func(r *ExecutionRef) { r.Members = []ResultMember{{TurnID: topic, Attempt: 1}} },
		"bad visibility": func(r *ExecutionRef) { r.Conversation.Visibility = "x" },
	}
	for name, mutate := range cases {
		ref := good
		ref.Members = append([]ResultMember(nil), good.Members...)
		mutate(&ref)
		if _, err := f.st.FreezeTurnResult(t.Context(), ref, "x"); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("%s: %v", name, err)
		}
	}
	forged := good
	forged.Conversation.Scope.NamespaceID = "00000000-0000-4000-8000-000000000000"
	if _, err := f.st.FreezeTurnResult(t.Context(), forged, "x"); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("forged namespace accepted: %v", err)
	}
	// A rename during the run must not lose the already produced model text.
	if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.FreezeTurnResult(t.Context(), good, "kept"); err != nil {
		t.Fatalf("revision drift lost a model result: %v", err)
	}
}

func TestFrozenResultsAreImmutable(t *testing.T) {
	f := newDeliveryFixture(t)
	turn := f.turn("telegram", "1:1", 0, 1)
	res := f.freeze(ExecInteractive, "x", ResultMember{TurnID: turn, Attempt: 1})
	for _, stmt := range []string{
		`UPDATE turn_results SET result_text='y'`, `DELETE FROM turn_results`,
		`UPDATE turn_result_members SET attempt=2`, `DELETE FROM turn_result_members`,
	} {
		if _, err := f.st.db.ExecContext(t.Context(), stmt); err == nil {
			t.Errorf("%s succeeded", stmt)
		}
	}
	if got, _ := f.st.GetTurnResult(t.Context(), res.ID); got.Text != "x" {
		t.Fatal("result changed")
	}
	if _, err := f.st.GetTurnResult(t.Context(), "missing"); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("missing result: %v", err)
	}
}

func TestCompareAndSetReplyMemo(t *testing.T) {
	f := newDeliveryFixture(t)
	turn := f.turn("telegram", "1:1", 0, 1)
	task := f.turn("task", "t:1", 0, 1)
	if ok, err := f.st.CompareAndSetReplyMemo(t.Context(), turn, 42); err != nil || !ok {
		t.Fatalf("first set: %v %v", ok, err)
	}
	if ok, err := f.st.CompareAndSetReplyMemo(t.Context(), turn, 43); err != nil || ok {
		t.Fatalf("second set overwrote: %v %v", ok, err)
	}
	if got, _ := f.st.GetTurn(t.Context(), turn); got.ReplyMsgID != 42 {
		t.Fatalf("memo %d", got.ReplyMsgID)
	}
	if _, err := f.st.CompareAndSetReplyMemo(t.Context(), task, 9); !errors.Is(err, ErrReplyMemoUnavailable) {
		t.Fatalf("task memo: %v", err)
	}
	if _, err := f.st.CompareAndSetReplyMemo(t.Context(), 999, 9); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("missing turn: %v", err)
	}
	if _, err := f.st.CompareAndSetReplyMemo(t.Context(), turn, 0); err == nil {
		t.Fatal("zero id accepted")
	}
}
