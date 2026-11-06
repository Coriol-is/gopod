package telegram

import "context"

// Message edits share a gate across repair, final transport and callbacks. The
// map holds only active/waiting calls; unrelated messages never share a gate.
type messageEditKey struct {
	chat    int64
	message int
}
type messageEditGate struct {
	token chan struct{}
	refs  int
}

func (b *Bot) lockMessageEdit(ctx context.Context, chat int64, message int) (func(), error) {
	key := messageEditKey{chat, message}
	b.controlMu.Lock()
	if b.messageEdits == nil {
		b.messageEdits = make(map[messageEditKey]*messageEditGate)
	}
	g := b.messageEdits[key]
	if g == nil {
		g = &messageEditGate{token: make(chan struct{}, 1)}
		g.token <- struct{}{}
		b.messageEdits[key] = g
	}
	g.refs++
	b.controlMu.Unlock()
	drop := func() {
		b.controlMu.Lock()
		g.refs--
		if g.refs == 0 {
			delete(b.messageEdits, key)
		}
		b.controlMu.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-g.token:
		if err := ctx.Err(); err != nil {
			g.token <- struct{}{}
			drop()
			return nil, err
		}
		release := func() { g.token <- struct{}{}; drop() }
		if b.store != nil {
			if err := b.store.ControlRepairBarrier(ctx, chat, message); err != nil {
				release()
				return nil, &ownershipSendError{cause: err}
			}
		}
		return release, nil
	}
}
