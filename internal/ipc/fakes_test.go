package ipc

import (
	"context"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// fakeMessageSink records every Send call and replays a scripted
// response per attempt. If responses runs out, returns nil.
type fakeMessageSink struct {
	mu        sync.Mutex
	calls     []sentMsg
	responses []error // consumed left-to-right; nil = success
	panic     bool
}

type sentMsg struct {
	chatJID string
	text    string
}

func (f *fakeMessageSink) Send(_ context.Context, chatJID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panic {
		panic("fakeMessageSink: scripted panic")
	}
	f.calls = append(f.calls, sentMsg{chatJID, text})
	if len(f.responses) == 0 {
		return nil
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return r
}

func (f *fakeMessageSink) callsSnapshot() []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sentMsg, len(f.calls))
	copy(out, f.calls)
	return out
}

// fakeTaskSink records Schedule + Cancel calls.
type fakeTaskSink struct {
	mu           sync.Mutex
	scheduled    []store.TaskRecord
	cancelled    []string
	scheduleErrs []error
	cancelErrs   []error
}

func (f *fakeTaskSink) Schedule(_ context.Context, t store.TaskRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scheduled = append(f.scheduled, t)
	if len(f.scheduleErrs) == 0 {
		return nil
	}
	r := f.scheduleErrs[0]
	f.scheduleErrs = f.scheduleErrs[1:]
	return r
}

func (f *fakeTaskSink) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	if len(f.cancelErrs) == 0 {
		return nil
	}
	r := f.cancelErrs[0]
	f.cancelErrs = f.cancelErrs[1:]
	return r
}

// fakeClock is a simple monotonic clock test seam.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// staticResolver builds a FolderResolver from a static map.
func staticResolver(m map[string]string) FolderResolver {
	return func(folder string) (string, bool) {
		jid, ok := m[folder]
		return jid, ok
	}
}
