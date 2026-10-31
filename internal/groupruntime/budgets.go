package groupruntime

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const defaultConcurrentGroups = 1
const defaultTurnsPerGroupDay = 96

// group_exec can take 20s, followed by up to 8s cleanup. Revocation must
// allow that whole lifecycle to settle before native resources are removed.
const groupDrainTimeout = 35 * time.Second

type turnBudget struct {
	Day   string
	Turns int
}

func (c Config) concurrentGroups() int {
	if c.MaxConcurrentGroups > 0 {
		return c.MaxConcurrentGroups
	}
	return defaultConcurrentGroups
}
func (c Config) turnsPerGroupDay() int {
	if c.MaxTurnsPerGroupDay > 0 {
		return c.MaxTurnsPerGroupDay
	}
	return defaultTurnsPerGroupDay
}

// Caller holds m.mu. One bounded durable counter per namespace survives process
// restart. A corrupt/unreadable counter denies admission rather than resetting it.
func (m *Manager) readBudget(ctx context.Context, namespace string, now time.Time) (turnBudget, error) {
	if m.Store == nil || namespace == "" || !m.Config.validAdmission() {
		return turnBudget{}, ErrUnavailable
	}
	raw, err := m.Store.GetState(ctx, "group_turn_budget:"+namespace)
	if err != nil {
		return turnBudget{}, ErrUnavailable
	}
	b := turnBudget{Day: now.UTC().Format("2006-01-02")}
	if raw != "" {
		b = turnBudget{}
		if json.Unmarshal([]byte(raw), &b) != nil || b.Turns < 0 {
			return turnBudget{}, ErrUnavailable
		}
		if _, err := time.Parse("2006-01-02", b.Day); err != nil {
			return turnBudget{}, ErrUnavailable
		}
		// A clock rollback must not mint a fresh allowance.
		if b.Day < now.UTC().Format("2006-01-02") {
			b = turnBudget{Day: now.UTC().Format("2006-01-02")}
		}
	}
	return b, nil
}

// Authorize is read-only; repeated output/tool validation never spends a turn.
// A reserved turn remains valid even after it spent the last available unit.
func (m *Manager) authorizeBudget(ctx context.Context, namespace string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeGroups[namespace] {
		return nil
	}
	b, err := m.readBudget(ctx, namespace, now)
	if err != nil || b.Turns >= m.Config.turnsPerGroupDay() || len(m.activeGroups) >= m.Config.concurrentGroups() {
		return ErrUnavailable
	}
	return nil
}

func (m *Manager) reserveTurn(ctx context.Context, namespace string, now time.Time) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.readBudget(ctx, namespace, now)
	if err != nil || b.Turns >= m.Config.turnsPerGroupDay() || m.activeGroups[namespace] || len(m.activeGroups) >= m.Config.concurrentGroups() {
		return nil, ErrUnavailable
	}
	b.Turns++
	raw, err := json.Marshal(b)
	if err != nil || m.Store.SetState(ctx, "group_turn_budget:"+namespace, string(raw)) != nil {
		return nil, ErrUnavailable
	}
	if m.activeGroups == nil {
		m.activeGroups = map[string]bool{}
	}
	m.activeGroups[namespace] = true
	var once sync.Once
	return func() { once.Do(func() { m.mu.Lock(); delete(m.activeGroups, namespace); m.mu.Unlock() }) }, nil
}
