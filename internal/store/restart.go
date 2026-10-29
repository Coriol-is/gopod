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
	MessageID  int                `json:"message_id"`
	ChatID     int64              `json:"chat_id"`
	ThreadID   int                `json:"thread_id"`
	Phase      string             `json:"phase"`
	CreatedAt  int64              `json:"created_at"`
	Containers []RestartContainer `json:"containers"`
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
		if prev != nil && (prev.Phase == "restarting" || prev.Phase == "requested" || r.CreatedAt-prev.CreatedAt < time.Minute.Milliseconds()) {
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
