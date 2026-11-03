package telegram

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestOutboxHealthLifecycleConcurrentRead(t *testing.T) {
	f := newOutboxFixture(t)
	d := NewOutboxDispatcher(f.owner, &fakeTransport{}, fastCfg(), nil)
	if d.Running() {
		t.Fatal("unstarted dispatcher healthy")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var reads sync.WaitGroup
	reads.Add(1)
	go func() {
		defer reads.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = d.Running()
			}
		}
	}()
	defer reads.Wait()
	defer cancel()
	if err := d.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer d.Stop(time.Second)
	if !d.Running() {
		t.Fatal("started dispatcher not running")
	}
	if err := d.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	if d.Running() {
		t.Fatal("stopped dispatcher healthy")
	}
}
