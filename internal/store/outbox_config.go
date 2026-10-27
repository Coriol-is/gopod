package store

import (
	"errors"
	"sync"
)

// Default admission quotas from the CP-002 spec (policy, not existing behavior).
const (
	DefaultMaxUnresolvedGlobal       = 10000
	DefaultMaxUnresolvedPerNamespace = 1000
	DefaultMaxRejectionsGlobal       = 50000
	DefaultMaxRejectionsPerNamespace = 5000
)

// ErrOutboxQuotaExceeded rejects a new publication. Quota pressure never evicts
// unresolved evidence.
var ErrOutboxQuotaExceeded = errors.New("store: outbox admission quota exceeded")

// OutboxLimits bounds unresolved (pending, sending, partial, uncertain) delivery
// groups. A zero field means the default.
type OutboxLimits struct {
	MaxUnresolvedGlobal       int
	MaxUnresolvedPerNamespace int
	// Rejection tombstones that may be stored; see RecordRequestRejection.
	MaxRejectionsGlobal       int
	MaxRejectionsPerNamespace int
}

type outboxConfig struct {
	mu     sync.RWMutex
	limits OutboxLimits
	notify func(deliveryID string)
}

// SetOutboxLimits replaces the admission quotas. Negative values are rejected.
func (s *Store) SetOutboxLimits(l OutboxLimits) error {
	if l.MaxUnresolvedGlobal < 0 || l.MaxUnresolvedPerNamespace < 0 || l.MaxRejectionsGlobal < 0 || l.MaxRejectionsPerNamespace < 0 {
		return errors.New("store: negative outbox limit")
	}
	s.outbox.mu.Lock()
	defer s.outbox.mu.Unlock()
	s.outbox.limits = l
	return nil
}

func (s *Store) outboxLimits() OutboxLimits {
	s.outbox.mu.RLock()
	l := s.outbox.limits
	s.outbox.mu.RUnlock()
	if l.MaxUnresolvedGlobal == 0 {
		l.MaxUnresolvedGlobal = DefaultMaxUnresolvedGlobal
	}
	if l.MaxUnresolvedPerNamespace == 0 {
		l.MaxUnresolvedPerNamespace = DefaultMaxUnresolvedPerNamespace
	}
	if l.MaxRejectionsGlobal == 0 {
		l.MaxRejectionsGlobal = DefaultMaxRejectionsGlobal
	}
	if l.MaxRejectionsPerNamespace == 0 {
		l.MaxRejectionsPerNamespace = DefaultMaxRejectionsPerNamespace
	}
	return l
}

// SetPublishNotifier registers the in-memory dispatcher wake-up. It is called
// after the publishing transaction committed, only for a newly created
// delivery, and must not block. A publication
// that enlists in a caller-owned transaction does not notify: that caller wakes
// the dispatcher with NotifyPublished after its own commit.
func (s *Store) SetPublishNotifier(fn func(deliveryID string)) {
	s.outbox.mu.Lock()
	s.outbox.notify = fn
	s.outbox.mu.Unlock()
}

// NotifyPublished invokes the registered notifier. Call it only after commit.
func (s *Store) NotifyPublished(deliveryID string) {
	s.outbox.mu.RLock()
	fn := s.outbox.notify
	s.outbox.mu.RUnlock()
	if fn != nil {
		fn(deliveryID)
	}
}
