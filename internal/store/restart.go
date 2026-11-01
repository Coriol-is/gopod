package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const restartStateKey = "controller:restart"
const restartHighWaterKey = "controller:restart_message"

var ErrRestartPending = errors.New("store: restart pending or cooling down")
var ErrRestartReplay = errors.New("store: restart command already handled")

// RestartContainer is a host-observed container identity, never an agent input.
type RestartContainer struct {
	ID    string   `json:"id"`
	Scope ScopeRef `json:"scope"`
}

// RestartRecord bridges the old process and the new one. Terminal records remain
// for diagnostics; the message high-water mark prevents Telegram replay loops.
type RestartRecord struct {
	MessageID         int                `json:"message_id"`
	ChatID            int64              `json:"chat_id"`
	ThreadID          int                `json:"thread_id"`
	Mode              string             `json:"mode,omitempty"`
	ForceTurns        []int64            `json:"force_turns,omitempty"`
	SuppressedThrough int64              `json:"suppressed_through,omitempty"`
	Phase             string             `json:"phase"`
	CreatedAt         int64              `json:"created_at"`
	Containers        []RestartContainer `json:"containers"`
}

func (s *Store) BeginRestart(ctx context.Context, r RestartRecord) error {
	if r.MessageID <= 0 || r.ChatID <= 0 || r.ThreadID < 0 {
		return errors.New("store: invalid restart identity")
	}
	r.Phase = "requested"
	r.CreatedAt = time.Now().UnixMilli()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.withTx(ctx, func(q taskSQL) error {
		var last string
		if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM router_state WHERE key=?),'')`, restartHighWaterKey).Scan(&last); err != nil {
			return err
		}
		n, _ := strconv.Atoi(last)
		if r.MessageID <= n {
			return ErrRestartReplay
		}
		prev, err := readRestartQ(ctx, q)
		if err != nil {
			return err
		}
		if prev != nil && (restartActive(prev.Phase) || r.CreatedAt-prev.CreatedAt < time.Minute.Milliseconds()) {
			return ErrRestartPending
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, restartHighWaterKey, strconv.Itoa(r.MessageID)); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, restartStateKey, string(raw))
		return err
	})
	if err != nil {
		return fmt.Errorf("store: record restart: %w", err)
	}
	return nil
}

func readRestartQ(ctx context.Context, q taskSQL) (*RestartRecord, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM router_state WHERE key=?),'')`, restartStateKey).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var r RestartRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("store: decode restart: %w", err)
	}
	return &r, nil
}

func (s *Store) ReadRestart(ctx context.Context) (*RestartRecord, error) {
	return readRestartQ(ctx, s.taskDB(ctx))
}

// SetRestartPhase is a CAS: an old request cannot overwrite a later restart.
func (s *Store) SetRestartPhase(ctx context.Context, messageID int, from, to string) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		r, err := readRestartQ(ctx, q)
		if err != nil {
			return err
		}
		if r == nil || r.MessageID != messageID || r.Phase != from {
			return ErrRestartReplay
		}
		r.Phase = to
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `UPDATE router_state SET value=? WHERE key=?`, string(raw), restartStateKey)
		return err
	})
	return err
}

// AbortRestart repairs an unstarted request after a failed/unknown phase commit.
// Only the caller which has not requested process shutdown may use this method.
// Both pre-shutdown phases are accepted; terminal or newer requests are preserved.
func (s *Store) AbortRestart(ctx context.Context, messageID int, terminal string) error {
	if terminal != "aborted" && terminal != "notice_unknown" {
		return errors.New("store: invalid abort phase")
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		r, err := readRestartQ(ctx, q)
		if err != nil {
			return err
		}
		if r == nil || r.MessageID != messageID {
			return ErrRestartReplay
		}
		if r.Phase != "requested" && r.Phase != "restarting" {
			return nil
		}
		r.Phase = terminal
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `UPDATE router_state SET value=? WHERE key=?`, string(raw), restartStateKey)
		return err
	})
	return err
}

func restartActive(phase string) bool {
	switch phase {
	case "requested", "draining", "forcing", "blocked", "restarting":
		return true
	}
	return false
}

// UpdateRestartTargets records the post-drain inventory, never a stale preflight list.
func (s *Store) UpdateRestartTargets(ctx context.Context, id int, targets []RestartContainer) error {
	return s.editRestart(ctx, id, func(r *RestartRecord) error {
		if r.Phase != "draining" && r.Phase != "forcing" && r.Phase != "blocked" {
			return ErrRestartReplay
		}
		r.Containers = targets
		return nil
	})
}
func (s *Store) editRestart(ctx context.Context, id int, fn func(*RestartRecord) error) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		r, e := readRestartQ(ctx, q)
		if e != nil {
			return e
		}
		if r == nil || r.MessageID != id {
			return ErrRestartReplay
		}
		if e = fn(r); e != nil {
			return e
		}
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `UPDATE router_state SET value=? WHERE key=?`, string(b), restartStateKey)
		return e
	})
	return err
}

// RecordForcedTurns stores non-replay intent BEFORE process cancellation. A started
// turn remains execution-unknown until the runner proves exit; pending tasks stop.
func (s *Store) RecordForcedTurns(ctx context.Context, id int, ids []int64) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		r, e := readRestartQ(ctx, q)
		if e != nil {
			return e
		}
		if r == nil || r.MessageID != id || r.Mode != "force" {
			return ErrRestartReplay
		}
		rows, e := q.QueryContext(ctx, `SELECT id FROM turns WHERE source='task' AND status IN ('pending','running','interrupted') AND chat_folder IN (SELECT folder FROM registered_chats WHERE jid=?)`, fmt.Sprintf("tg:%d", r.ChatID))
		if e != nil {
			return e
		}
		for rows.Next() {
			var v int64
			if e = rows.Scan(&v); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		seen := map[int64]bool{}
		for _, v := range r.ForceTurns {
			seen[v] = true
		}
		for _, v := range ids {
			if v <= 0 || seen[v] {
				continue
			}
			seen[v] = true
			r.ForceTurns = append(r.ForceTurns, v)
			var runID string
			e = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT run_id FROM task_attempts WHERE turn_id=?),'')`, v).Scan(&runID)
			if e != nil {
				return e
			}
			if runID != "" {
				run, e := readRunQ(ctx, q, runID)
				if e != nil {
					return e
				}
				if !run.State.terminal() {
					if _, e = requestRunStopQ(ctx, q, run, "forced_restart", time.Now().UnixMilli()); e != nil {
						return e
					}
				}
				continue
			}
			_, e = q.ExecContext(ctx, `UPDATE turns SET status=CASE WHEN attempts=0 THEN 'stopped' ELSE 'failed' END,error=CASE WHEN attempts=0 THEN 'forced restart: cancelled before start' ELSE 'execution_unknown: forced restart cancellation requested' END,finished_at=? WHERE id=? AND status IN ('pending','running','interrupted')`, time.Now().UnixMilli(), v)
			if e != nil {
				return e
			}
		}
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `UPDATE router_state SET value=? WHERE key=?`, string(b), restartStateKey)
		return e
	})
	return err
}

func (s *Store) SettleForcedTurns(ctx context.Context, id int) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		r, e := readRestartQ(ctx, q)
		if e != nil {
			return e
		}
		if r == nil || r.MessageID != id {
			return ErrRestartReplay
		}
		for _, v := range r.ForceTurns {
			_, e = q.ExecContext(ctx, `UPDATE turns SET status='stopped',error='forced restart: execution terminated',finished_at=? WHERE id=? AND NOT EXISTS(SELECT 1 FROM task_attempts WHERE turn_id=?)`, time.Now().UnixMilli(), v, v)
			if e != nil {
				return e
			}
		}
		return nil
	})
	return err
}

const forceSuppressionKey = "controller:force_suppressed_through"

func (s *Store) RecordRestartSuppression(ctx context.Context, id int, through int64) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		r, e := readRestartQ(ctx, q)
		if e != nil {
			return e
		}
		if r == nil || r.MessageID != id || r.Mode != "force" {
			return ErrRestartReplay
		}
		r.SuppressedThrough = through
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `UPDATE router_state SET value=? WHERE key=?`, string(b), restartStateKey); e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=CAST(MAX(CAST(value AS INTEGER),CAST(excluded.value AS INTEGER)) AS TEXT)`, forceSuppressionKey+":tg:"+strconv.FormatInt(r.ChatID, 10), strconv.FormatInt(through, 10))
		return e
	})
	return err
}
