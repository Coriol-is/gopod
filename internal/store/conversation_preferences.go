package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ConversationSelection is an immutable resolved setting snapshot. Revision is
// scoped to the namespace epoch, and changes even when a value changes back.
type ConversationSelection struct {
	Scope                             ScopeRef
	ThreadID                          int
	Provider, Model, DefaultProvider  string
	ProviderInherited, ModelInherited bool
	Revision                          int64
}
type conversationPreferences struct {
	Default            string                    `json:"default"`
	Models             map[string]string         `json:"models"`
	Providers          map[int]string            `json:"providers"`
	ConversationModels map[int]map[string]string `json:"conversation_models"`
}

var ErrSelectionChanged = errors.New("store: conversation selection changed")

func preferencesQ(ctx context.Context, q taskSQL, folder, global string) (ScopeRef, conversationPreferences, int64, error) {
	ref, err := snapshotScopeQ(ctx, q, "folder = ?", folder)
	if err != nil {
		return ref, conversationPreferences{}, 0, err
	}
	var raw string
	var revision int64
	err = q.QueryRowContext(ctx, `SELECT payload,revision FROM conversation_preferences WHERE namespace_id=? AND namespace_epoch=?`, ref.NamespaceID, ref.NamespaceEpoch).Scan(&raw, &revision)
	if err != nil && err != sql.ErrNoRows {
		return ref, conversationPreferences{}, 0, err
	}
	p := conversationPreferences{Models: map[string]string{}, Providers: map[int]string{}, ConversationModels: map[int]map[string]string{}}
	if err == sql.ErrNoRows {
		var provider, model string
		if err = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM router_state WHERE key=?),''),COALESCE((SELECT value FROM router_state WHERE key=?),'')`, "provider:"+folder, "model:"+folder).Scan(&provider, &model); err != nil {
			return ref, p, 0, err
		}
		if provider != "" {
			p.Default = provider
		}
		if model != "" {
			modelProvider := p.Default
			if modelProvider == "" {
				modelProvider = global
			}
			p.Models[modelProvider] = model
		}
		revision = 1
		if err = writePreferencesQ(ctx, q, ref, p, revision); err != nil {
			return ref, p, 0, err
		}
	} else {
		p = conversationPreferences{}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return ref, p, 0, fmt.Errorf("decode conversation preferences: %w", err)
		}
	}
	if p.Models == nil || p.Providers == nil || p.ConversationModels == nil {
		return ref, p, 0, errors.New("store: invalid conversation preferences")
	}
	return ref, p, revision, nil
}
func writePreferencesQ(ctx context.Context, q taskSQL, ref ScopeRef, p conversationPreferences, revision int64) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO conversation_preferences(namespace_id,namespace_epoch,payload,revision) VALUES(?,?,?,?) ON CONFLICT(namespace_id,namespace_epoch) DO UPDATE SET payload=excluded.payload,revision=excluded.revision`, ref.NamespaceID, ref.NamespaceEpoch, string(raw), revision)
	return err
}
func resolvedPreference(ref ScopeRef, p conversationPreferences, rev int64, thread int, global string) ConversationSelection {
	effectiveDefault := p.Default
	if effectiveDefault == "" {
		effectiveDefault = global
	}
	s := ConversationSelection{Scope: ref, ThreadID: thread, DefaultProvider: effectiveDefault, Provider: effectiveDefault, ProviderInherited: true, ModelInherited: true, Revision: rev}
	if v, ok := p.Providers[thread]; ok {
		s.Provider = v
		s.ProviderInherited = false
	}
	s.Model = p.Models[s.Provider]
	if m, ok := p.ConversationModels[thread]; ok {
		if v, ok := m[s.Provider]; ok {
			s.Model = v
			s.ModelInherited = false
		}
	}
	return s
}

// ResolveConversationSelection lazily migrates legacy model only to its original
// provider. Scope admission and initialization occur in one store transaction.
func (s *Store) ResolveConversationSelection(ctx context.Context, folder string, thread int, global string) (out ConversationSelection, err error) {
	if thread < 0 {
		return out, errors.New("store: negative conversation thread")
	}
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if e := topicFolderAdmissionQ(ctx, q, folder, thread); e != nil {
			return e
		}
		ref, p, rev, e := preferencesQ(ctx, q, folder, global)
		if e != nil {
			return e
		}
		out = resolvedPreference(ref, p, rev, thread, global)
		return nil
	})
	return
}

// ChangeConversationSelection compares the full scope and settings revision. Kind
// is provider, default_provider, model, or inherit_model. Empty provider inherits.
func (s *Store) ChangeConversationSelection(ctx context.Context, expected ConversationSelection, kind, value string) (out ConversationSelection, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if e := validateScopeQ(ctx, q, expected.Scope); e != nil {
			return e
		}
		if e := topicFolderAdmissionQ(ctx, q, expected.Scope.Folder, expected.ThreadID); e != nil {
			return e
		}
		ref, p, rev, e := preferencesQ(ctx, q, expected.Scope.Folder, expected.DefaultProvider)
		if e != nil {
			return e
		}
		current := resolvedPreference(ref, p, rev, expected.ThreadID, expected.DefaultProvider)
		if rev != expected.Revision || current != expected {
			return ErrSelectionChanged
		}
		switch kind {
		case "provider":
			if value == "" {
				delete(p.Providers, expected.ThreadID)
			} else {
				p.Providers[expected.ThreadID] = value
			}
		case "default_provider":
			if value == "" {
				return errors.New("store: empty default provider")
			}
			p.Default = value
		case "model":
			m := p.ConversationModels[expected.ThreadID]
			if m == nil {
				m = map[string]string{}
				p.ConversationModels[expected.ThreadID] = m
			}
			m[expected.Provider] = value
		case "inherit_model":
			delete(p.ConversationModels[expected.ThreadID], expected.Provider)
		default:
			return errors.New("store: unknown selection change")
		}
		if e = writePreferencesQ(ctx, q, ref, p, rev+1); e != nil {
			return e
		}
		out = resolvedPreference(ref, p, rev+1, expected.ThreadID, expected.DefaultProvider)
		return nil
	})
	return
}

// retireConversationPreferencesQ participates in the topic retirement transaction.
func retireConversationPreferencesQ(ctx context.Context, q taskSQL, ref ScopeRef, thread int) error {
	var raw string
	var rev int64
	err := q.QueryRowContext(ctx, `SELECT payload,revision FROM conversation_preferences WHERE namespace_id=? AND namespace_epoch=?`, ref.NamespaceID, ref.NamespaceEpoch).Scan(&raw, &rev)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var p conversationPreferences
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	delete(p.Providers, thread)
	delete(p.ConversationModels, thread)
	return writePreferencesQ(ctx, q, ref, p, rev+1)
}

// BindTurnSelection persists the selected harness before any native execution.
// Rebinding an existing attempt returns its original snapshot, never current settings.
func (s *Store) BindTurnSelection(ctx context.Context, turnID int64, attempt int, selected ConversationSelection) (out ConversationSelection, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		var folder, status string
		var thread, currentAttempt int
		if e := q.QueryRowContext(ctx, `SELECT chat_folder,thread_id,status,attempts FROM turns WHERE id=?`, turnID).Scan(&folder, &thread, &status, &currentAttempt); e != nil {
			return e
		}
		if folder != selected.Scope.Folder || thread != selected.ThreadID || status != TurnRunning || attempt <= 0 || attempt != currentAttempt {
			return errors.New("store: invalid selection attempt")
		}
		if e := validateScopeQ(ctx, q, selected.Scope); e != nil {
			return e
		}
		raw, e := json.Marshal(selected)
		if e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO turn_provider_selections(turn_id,attempt,payload) VALUES(?,?,?) ON CONFLICT(turn_id,attempt) DO NOTHING`, turnID, attempt, string(raw)); e != nil {
			return e
		}
		var saved string
		if e = q.QueryRowContext(ctx, `SELECT payload FROM turn_provider_selections WHERE turn_id=? AND attempt=?`, turnID, attempt).Scan(&saved); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(saved), &out); e != nil {
			return e
		}
		var boundProvider, boundModel string
		bindingErr := q.QueryRowContext(ctx, `SELECT provider,model FROM attempt_bindings WHERE turn_id=?`, turnID).Scan(&boundProvider, &boundModel)
		if bindingErr != nil && bindingErr != sql.ErrNoRows {
			return bindingErr
		}
		if bindingErr == nil && (boundProvider != out.Provider || boundModel != out.Model) {
			return errors.New("store: durable binding conflicts with conversation selection")
		}
		if out.Scope != selected.Scope {
			return ErrStaleScope
		}
		return nil
	})
	return
}
func (s *Store) TurnSelection(ctx context.Context, turnID int64, attempt int) (out ConversationSelection, err error) {
	var raw string
	err = s.taskDB(ctx).QueryRowContext(ctx, `SELECT payload FROM turn_provider_selections WHERE turn_id=? AND attempt=?`, turnID, attempt).Scan(&raw)
	if err != nil {
		return out, fmt.Errorf("reading turn selection %s: %w", strconv.FormatInt(turnID, 10), err)
	}
	err = json.Unmarshal([]byte(raw), &out)
	return
}

// ResolveDefaultSelection deliberately ignores main/thread-zero overrides.
// Unscoped maintenance uses this API rather than pretending main is a wildcard.
func (s *Store) ResolveDefaultSelection(ctx context.Context, folder, global string) (out ConversationSelection, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		ref, p, rev, e := preferencesQ(ctx, q, folder, global)
		if e != nil {
			return e
		}
		out = resolvedPreference(ref, p, rev, -1, global)
		return nil
	})
	return
}
