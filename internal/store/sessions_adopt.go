package store

import (
	"context"
	"fmt"
	"time"
)

// AdoptionReport is the outcome of AdoptLegacySessions.
type AdoptionReport struct {
	Mode        string // mode after the call
	Sessions    int    // execution_sessions created
	Contexts    int    // native_contexts created
	AlreadyDone bool
}

// SessionMappingMode reports which session table is authoritative and whether
// adoption of the durable mapping has been requested.
func (s *Store) SessionMappingMode(ctx context.Context) (mode string, requested bool, err error) {
	mode, err = s.GetState(ctx, sessionMappingKey)
	if err != nil {
		return "", false, err
	}
	if mode == "" {
		mode = SessionMappingLegacy
	}
	req, err := s.GetState(ctx, sessionMappingReqKey)
	if err != nil {
		return "", false, err
	}
	return mode, req == SessionMappingDurable, nil
}

// RequestSessionAdoption records that the next quiesced controller start should
// adopt legacy sessions into the durable mapping.
func (s *Store) RequestSessionAdoption(ctx context.Context) error {
	return s.SetState(ctx, sessionMappingReqKey, SessionMappingDurable)
}

// AdoptLegacySessions maps every legacy sessions row onto a durable binding and
// native context, then flips the mapping marker, all in one transaction. Any
// row for an unregistered folder rolls the whole adoption back.
func (s *Store) AdoptLegacySessions(ctx context.Context) (AdoptionReport, error) {
	mode, _, err := s.SessionMappingMode(ctx)
	if err != nil {
		return AdoptionReport{}, fmt.Errorf("adopting legacy sessions: %w", err)
	}
	if mode == SessionMappingDurable {
		return AdoptionReport{Mode: SessionMappingDurable, AlreadyDone: true}, nil
	}
	unfinished, err := s.ListUnfinishedTurns(ctx)
	if err != nil {
		return AdoptionReport{}, fmt.Errorf("adopting legacy sessions: %w", err)
	}
	if len(unfinished) > 0 {
		return AdoptionReport{}, ErrAdoptionBusy
	}
	rep := AdoptionReport{Mode: SessionMappingDurable}
	_, err = s.withTx(ctx, func(q taskSQL) error {
		chats, err := registeredByFolderQ(ctx, q)
		if err != nil {
			return err
		}
		rows, err := legacySessionRowsQ(ctx, q)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		for _, r := range rows {
			c, ok := chats[r.folder]
			if !ok {
				return fmt.Errorf("folder %q: %w", r.folder, ErrAdoptionUnregistered)
			}
			b, created, err := resolveConversationQ(ctx, q, c.Scope(), r.thread)
			if err != nil {
				return fmt.Errorf("folder %q thread %d: %w", r.folder, r.thread, err)
			}
			if created {
				rep.Sessions++
			}
			res, err := q.ExecContext(ctx, `INSERT INTO native_contexts(session_id,context_epoch,provider,native_id,updated_at) VALUES(?,?,?,?,?)
 ON CONFLICT(session_id,context_epoch,provider) DO NOTHING`, b.ActiveSessionID, b.ContextEpoch, r.provider, r.sessionID, now)
			if err != nil {
				return fmt.Errorf("inserting native context: %w", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			rep.Contexts += int(n)
		}
		const upsert = `INSERT INTO router_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`
		if _, err = q.ExecContext(ctx, upsert, sessionMappingKey, SessionMappingDurable); err != nil {
			return fmt.Errorf("setting session mapping marker: %w", err)
		}
		if _, err = q.ExecContext(ctx, upsert, sessionMappingReqKey, ""); err != nil {
			return fmt.Errorf("clearing session mapping request: %w", err)
		}
		return nil
	})
	if err != nil {
		return AdoptionReport{}, fmt.Errorf("adopting legacy sessions: %w", err)
	}
	return rep, nil
}

type legacySessionRow struct {
	folder, provider, sessionID string
	thread                      int
}

func legacySessionRowsQ(ctx context.Context, q taskSQL) ([]legacySessionRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT chat_folder,thread_id,provider,session_id FROM sessions ORDER BY chat_folder,thread_id,provider`)
	if err != nil {
		return nil, fmt.Errorf("listing legacy sessions: %w", err)
	}
	defer rows.Close()
	var out []legacySessionRow
	for rows.Next() {
		var r legacySessionRow
		if err = rows.Scan(&r.folder, &r.thread, &r.provider, &r.sessionID); err != nil {
			return nil, fmt.Errorf("scanning legacy session: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// registeredByFolderQ reads registered chats on q; ListRegistered would use a
// second connection and deadlock against the open transaction.
func registeredByFolderQ(ctx context.Context, q taskSQL) (map[string]RegisteredChat, error) {
	rows, err := q.QueryContext(ctx, registeredColumns)
	if err != nil {
		return nil, fmt.Errorf("listing registered chats: %w", err)
	}
	defer rows.Close()
	out := map[string]RegisteredChat{}
	for rows.Next() {
		c, err := scanRegistered(rows)
		if err != nil {
			return nil, err
		}
		out[c.Folder] = c
	}
	return out, rows.Err()
}
