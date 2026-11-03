package runner

import (
	"context"
	"errors"
	"sync"
)

var ErrConversationQuiescing = errors.New("conversation is quiescing")

type conversationActivityKey struct {
	folder string
	thread int
}
type conversationActivity struct {
	blocked    bool
	generation uint64
	sequence   uint64
	active     map[uint64]context.CancelFunc
	changed    chan struct{}
}

// ConversationGate joins host-owned work by exact conversation. Closing a lane
// atomically forbids new work and cancels admitted contexts; Wait joins leases,
// not database status. Callers must separately verify native/remote uncertainty.
// Its zero value is ready for use. A timed-out Wait does not reopen the lane.
type ConversationGate struct {
	mu         sync.Mutex
	lanes      map[conversationActivityKey]*conversationActivity
	generation uint64
}

func (g *ConversationGate) lane(key conversationActivityKey) *conversationActivity {
	if g.lanes == nil {
		g.lanes = make(map[conversationActivityKey]*conversationActivity)
	}
	s := g.lanes[key]
	if s == nil {
		s = &conversationActivity{active: make(map[uint64]context.CancelFunc), changed: make(chan struct{})}
		g.lanes[key] = s
	}
	return s
}
func signalConversation(s *conversationActivity) { close(s.changed); s.changed = make(chan struct{}) }
func (g *ConversationGate) Enter(ctx context.Context, folder string, thread int) (context.Context, func(), error) {
	g.mu.Lock()
	key := conversationActivityKey{folder, thread}
	s := g.lane(key)
	if s.blocked {
		g.mu.Unlock()
		return ctx, nil, ErrConversationQuiescing
	}
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return ctx, nil, err
	}
	child, cancel := context.WithCancel(ctx)
	s.sequence++
	id := s.sequence
	s.active[id] = cancel
	signalConversation(s)
	g.mu.Unlock()
	var once sync.Once
	return child, func() {
		once.Do(func() {
			cancel()
			g.mu.Lock()
			delete(s.active, id)
			signalConversation(s)
			if !s.blocked && len(s.active) == 0 {
				delete(g.lanes, key)
			}
			g.mu.Unlock()
		})
	}, nil
}
func (g *ConversationGate) Block(folder string, thread int) uint64 {
	return g.close(folder, thread, true)
}

// Seal closes new transport admission without canceling already issued requests.
func (g *ConversationGate) Seal(folder string, thread int) uint64 {
	return g.close(folder, thread, false)
}
func (g *ConversationGate) close(folder string, thread int, cancelActive bool) uint64 {
	g.mu.Lock()
	s := g.lane(conversationActivityKey{folder, thread})
	s.blocked = true
	g.generation++
	s.generation = g.generation
	generation := s.generation
	cancels := make([]context.CancelFunc, 0, len(s.active))
	for _, cancel := range s.active {
		cancels = append(cancels, cancel)
	}
	signalConversation(s)
	g.mu.Unlock()
	if cancelActive {
		for _, cancel := range cancels {
			cancel()
		}
	}
	return generation
}
func (g *ConversationGate) Wait(ctx context.Context, folder string, thread int) error {
	for {
		g.mu.Lock()
		s := g.lane(conversationActivityKey{folder, thread})
		if !s.blocked {
			g.mu.Unlock()
			return errors.New("conversation wait requires closed admission")
		}
		if len(s.active) == 0 {
			g.mu.Unlock()
			return nil
		}
		wake := s.changed
		g.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Reopen is only for a durable definite refusal, after all old leases joined.
func (g *ConversationGate) Reopen(folder string, thread int, generation uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.lane(conversationActivityKey{folder, thread})
	if !s.blocked || generation == 0 || s.generation != generation {
		return errors.New("stale conversation fence")
	}
	if len(s.active) > 0 {
		return errors.New("conversation still active")
	}
	s.blocked = false
	signalConversation(s)
	delete(g.lanes, conversationActivityKey{folder, thread})
	return nil
}
