package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ExecutionKind is the typed execution class of a frozen result. It decides
// whether freezing touches foreground conversation history.
type ExecutionKind string

const (
	// ExecInteractive is a foreground Telegram turn (possibly several coalesced
	// turns). Freezing appends the assistant continuity exactly once.
	ExecInteractive ExecutionKind = "interactive"
	// ExecIsolatedTask is managed task work. Freezing never appends foreground
	// history, never sets turns.reply_msg_id and never moves a continuity marker.
	ExecIsolatedTask ExecutionKind = "isolated_task"
)

const authorityModelReported = "model_reported"

var (
	// ErrInvalidResult is returned for a malformed freeze request.
	ErrInvalidResult = errors.New("store: invalid turn result")
	// ErrResultConflict is returned when members already belong to a different
	// result, or the same members are frozen again with different content.
	ErrResultConflict = errors.New("store: turn result conflicts with an existing freeze")
	// ErrReplyMemoUnavailable is returned when a task turn would receive a
	// foreground reply memo.
	ErrReplyMemoUnavailable = errors.New("store: reply memo is not available for this turn")
)

// ResultMember is one turn attempt covered by a result. Coalesced interactive
// turns list every member and share the single result.
type ResultMember struct {
	TurnID  int64
	Attempt int
}

// ExecutionRef is the trusted description of the execution being frozen.
type ExecutionRef struct {
	Conversation       ConversationScope
	Kind               ExecutionKind
	Members            []ResultMember
	Provider           string
	Model              string
	ProfileFingerprint string
}

// TurnResult is an immutable model result. Authority is always model_reported:
// a frozen result is neither host verification nor publication nor delivery.
type TurnResult struct {
	ID                 string
	Conversation       ConversationScope
	Kind               ExecutionKind
	Provider           string
	Model              string
	ProfileFingerprint string
	Text               string
	TextHash           string
	Authority          string
	CreatedAt          int64
	Members            []ResultMember
	// Created is false when an identical earlier freeze was returned. Not persisted.
	Created bool
}

func (k ExecutionKind) valid() bool { return k == ExecInteractive || k == ExecIsolatedTask }

// FreezeTurnResult durably records the final model text for the given members
// in one transaction. It must be called before the runner emits its final
// Result or releases the execution slot, and is idempotent for identical input.
//
// Scope: the namespace identity must be recorded for the snapshot (same
// folder, namespace, JID and epoch). The registration revision and retirement
// state are deliberately not re-checked here: a model result that already
// exists must not be lost because a registration was renamed during the run.
// Authorization is re-validated by PublishDelivery.
//
// Interactive freezes append continuity once, via the same transaction; replays
// append nothing. Isolated task freezes never touch foreground state.
func (s *Store) FreezeTurnResult(ctx context.Context, ref ExecutionRef, text string) (TurnResult, error) {
	if err := validateExecutionRef(ref); err != nil {
		return TurnResult{}, err
	}
	var out TurnResult
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var e error
		out, e = freezeTurnResultQ(ctx, q, ref, text)
		return e
	})
	if err != nil {
		return TurnResult{}, err
	}
	return out, nil
}

func validateExecutionRef(ref ExecutionRef) error {
	if !ref.Kind.valid() || !ref.Conversation.validShape() || ref.Provider == "" || len(ref.Members) == 0 {
		return ErrInvalidResult
	}
	seen := make(map[ResultMember]bool, len(ref.Members))
	for _, m := range ref.Members {
		if m.TurnID <= 0 || m.Attempt <= 0 || seen[m] {
			return ErrInvalidResult
		}
		seen[m] = true
	}
	return nil
}

func freezeTurnResultQ(ctx context.Context, q taskSQL, ref ExecutionRef, text string) (TurnResult, error) {
	scope := ref.Conversation.Scope
	var recorded int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM namespace_ownership WHERE folder=? AND namespace_id=? AND chat_jid=? AND namespace_epoch=?`,
		scope.Folder, scope.NamespaceID, scope.JID, scope.NamespaceEpoch).Scan(&recorded); err != nil {
		return TurnResult{}, err
	}
	if recorded != 1 {
		return TurnResult{}, ErrStaleScope
	}
	for _, m := range ref.Members {
		var folder, source string
		var thread, attempts int
		err := q.QueryRowContext(ctx, `SELECT chat_folder,thread_id,source,attempts FROM turns WHERE id=?`, m.TurnID).Scan(&folder, &thread, &source, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return TurnResult{}, fmt.Errorf("%w: turn %d: %w", ErrInvalidResult, m.TurnID, ErrTurnNotFound)
		}
		if err != nil {
			return TurnResult{}, err
		}
		if folder != scope.Folder || thread != ref.Conversation.ThreadID {
			return TurnResult{}, fmt.Errorf("%w: turn %d belongs to another conversation", ErrInvalidResult, m.TurnID)
		}
		if m.Attempt > attempts {
			return TurnResult{}, fmt.Errorf("%w: turn %d has no attempt %d", ErrInvalidResult, m.TurnID, m.Attempt)
		}
		if (ref.Kind == ExecIsolatedTask) != (source == "task") {
			return TurnResult{}, fmt.Errorf("%w: turn %d source %q does not match %s execution", ErrInvalidResult, m.TurnID, source, ref.Kind)
		}
	}

	hash := sha256Hex([]byte(text))
	if existing, found, err := existingResultForMembers(ctx, q, ref); err != nil {
		return TurnResult{}, err
	} else if found {
		if existing.TextHash != hash || existing.Kind != ref.Kind || !sameConversation(existing.Conversation, ref.Conversation) {
			return TurnResult{}, ErrResultConflict
		}
		existing.Created = false
		return existing, nil
	}

	id, err := newUUID()
	if err != nil {
		return TurnResult{}, err
	}
	now := time.Now().UnixMilli()
	if _, err = q.ExecContext(ctx, `INSERT INTO turn_results(result_id,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,execution_kind,provider,model,profile_fingerprint,result_text,text_hash,authority,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, scope.NamespaceID, scope.NamespaceEpoch, scope.RegistrationRevision, scope.JID, scope.Folder,
		ref.Conversation.ThreadID, string(ref.Conversation.Visibility), string(ref.Kind), ref.Provider, ref.Model,
		ref.ProfileFingerprint, text, hash, authorityModelReported, now); err != nil {
		return TurnResult{}, fmt.Errorf("store: freeze result: %w", err)
	}
	for _, m := range ref.Members {
		if _, err = q.ExecContext(ctx, `INSERT INTO turn_result_members(result_id,turn_id,attempt) VALUES(?,?,?)`, id, m.TurnID, m.Attempt); err != nil {
			return TurnResult{}, fmt.Errorf("store: freeze result member: %w", err)
		}
	}
	if ref.Kind == ExecInteractive {
		if err = completeConversationQ(ctx, q, scope.Folder, ref.Conversation.ThreadID, ref.Provider, text); err != nil {
			return TurnResult{}, fmt.Errorf("store: freeze result continuity: %w", err)
		}
	}
	return TurnResult{
		ID: id, Conversation: ref.Conversation, Kind: ref.Kind, Provider: ref.Provider, Model: ref.Model,
		ProfileFingerprint: ref.ProfileFingerprint, Text: text, TextHash: hash, Authority: authorityModelReported,
		CreatedAt: now, Members: sortedMembers(ref.Members), Created: true,
	}, nil
}

func sortedMembers(in []ResultMember) []ResultMember {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b ResultMember) int {
		if a.TurnID != b.TurnID {
			return int(a.TurnID - b.TurnID)
		}
		return a.Attempt - b.Attempt
	})
	return out
}

// existingResultForMembers returns the result already covering exactly the
// requested members. A partial overlap is a conflict, not a new result.
func existingResultForMembers(ctx context.Context, q taskSQL, ref ExecutionRef) (TurnResult, bool, error) {
	ids := map[string]bool{}
	hits := 0
	for _, m := range ref.Members {
		var id string
		err := q.QueryRowContext(ctx, `SELECT result_id FROM turn_result_members WHERE turn_id=? AND attempt=?`, m.TurnID, m.Attempt).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return TurnResult{}, false, err
		}
		ids[id] = true
		hits++
	}
	if hits == 0 {
		return TurnResult{}, false, nil
	}
	if hits != len(ref.Members) || len(ids) != 1 {
		return TurnResult{}, false, ErrResultConflict
	}
	var id string
	for k := range ids {
		id = k
	}
	res, err := readTurnResult(ctx, q, id)
	if err != nil {
		return TurnResult{}, false, err
	}
	if len(res.Members) != len(ref.Members) {
		return TurnResult{}, false, ErrResultConflict
	}
	return res, true, nil
}

// ErrResultNotFound is returned when a result id does not exist.
var ErrResultNotFound = errors.New("store: turn result not found")

// GetTurnResult returns one frozen result with its members.
func (s *Store) GetTurnResult(ctx context.Context, id string) (TurnResult, error) {
	return readTurnResult(ctx, s.taskDB(ctx), id)
}

// TurnResultForAttempt finds the result covering a turn attempt, for recovery
// checks that must run before any runner invocation.
func (s *Store) TurnResultForAttempt(ctx context.Context, turnID int64, attempt int) (TurnResult, bool, error) {
	var id string
	err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT result_id FROM turn_result_members WHERE turn_id=? AND attempt=?`, turnID, attempt).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnResult{}, false, nil
	}
	if err != nil {
		return TurnResult{}, false, err
	}
	res, err := readTurnResult(ctx, s.taskDB(ctx), id)
	return res, err == nil, err
}

func readTurnResult(ctx context.Context, q taskSQL, id string) (TurnResult, error) {
	var r TurnResult
	var vis, kind string
	err := q.QueryRowContext(ctx, `SELECT result_id,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,execution_kind,provider,model,profile_fingerprint,result_text,text_hash,authority,created_at FROM turn_results WHERE result_id=?`, id).
		Scan(&r.ID, &r.Conversation.Scope.NamespaceID, &r.Conversation.Scope.NamespaceEpoch, &r.Conversation.Scope.RegistrationRevision,
			&r.Conversation.Scope.JID, &r.Conversation.Scope.Folder, &r.Conversation.ThreadID, &vis, &kind, &r.Provider, &r.Model,
			&r.ProfileFingerprint, &r.Text, &r.TextHash, &r.Authority, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnResult{}, ErrResultNotFound
	}
	if err != nil {
		return TurnResult{}, err
	}
	r.Conversation.Visibility, r.Kind = Visibility(vis), ExecutionKind(kind)
	rows, err := q.QueryContext(ctx, `SELECT turn_id,attempt FROM turn_result_members WHERE result_id=? ORDER BY turn_id,attempt`, id)
	if err != nil {
		return TurnResult{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var m ResultMember
		if err = rows.Scan(&m.TurnID, &m.Attempt); err != nil {
			return TurnResult{}, err
		}
		r.Members = append(r.Members, m)
	}
	return r, rows.Err()
}

// CompareAndSetReplyMemo records the compatibility reply memo (turns.reply_msg_id)
// only while it is still unset, so it is never the sole dedupe authority and a
// resumed turn cannot overwrite an earlier acknowledgement. It reports whether
// this call set the memo. Task turns never receive a foreground memo.
func (s *Store) CompareAndSetReplyMemo(ctx context.Context, turnID int64, msgID int) (bool, error) {
	if msgID <= 0 {
		return false, fmt.Errorf("store: CompareAndSetReplyMemo: invalid message id %d", msgID)
	}
	db := s.taskDB(ctx)
	var source string
	err := db.QueryRowContext(ctx, `SELECT source FROM turns WHERE id=?`, turnID).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrTurnNotFound
	}
	if err != nil {
		return false, err
	}
	if source == "task" {
		return false, ErrReplyMemoUnavailable
	}
	res, err := db.ExecContext(ctx, `UPDATE turns SET reply_msg_id=? WHERE id=? AND reply_msg_id=0 AND source<>'task'`, msgID, turnID)
	if err != nil {
		return false, fmt.Errorf("store: CompareAndSetReplyMemo: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// sameConversation compares the routing identity and deliberately ignores the
// registration revision, which may legitimately advance between a run and a replay.
func sameConversation(a, b ConversationScope) bool {
	a.Scope.RegistrationRevision, b.Scope.RegistrationRevision = 0, 0
	return a == b
}
