package telegram

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
)

type delayedDeviceLogin struct {
	reads atomic.Int64
	eof   chan struct{}
	once  sync.Once
}

func (e *delayedDeviceLogin) ReadLine() (string, error) {
	switch e.reads.Add(1) {
	case 1:
		return "https://auth.openai.com/codex/device", nil
	case 2:
		return "ABCD-EFGHI", nil
	}
	<-e.eof
	return "", io.EOF
}
func (e *delayedDeviceLogin) Close() error                     { e.once.Do(func() { close(e.eof) }); return nil }
func (e *delayedDeviceLogin) WriteStdin(p []byte) (int, error) { return len(p), nil }

func TestDeviceLoginCompletionOutlivesIngressReturn(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	ctx, release, err := b.topicIngress.Enter(t.Context(), buildChatJID(streamChat), 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx = withTopicOutputOrigin(ctx, streamChat, 7)
	loginCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	exec := &delayedDeviceLogin{eof: make(chan struct{})}
	checked := make(chan error, 1)
	b.loginAuthCheck = func(ctx context.Context, _ *loginSession) bool { checked <- ctx.Err(); return ctx.Err() == nil }
	session := &loginSession{chatID: streamChat, thread: 7, private: true, ctx: loginCtx, cancel: cancel, exec: exec}
	if !b.publishLogin(ctx, threadedMsg(7, "/login"), session, runner.NewCodexProvider("test")) {
		t.Fatal("publish failed")
	}
	release() // callback/slash handler has returned while native EOF is delayed.
	select {
	case <-checked:
		t.Fatal("verified before EOF")
	default:
	}
	_ = exec.Close()
	select {
	case err := <-checked:
		if err != nil {
			t.Fatalf("completion inherited caller cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no verification")
	}
	waitFor(t, "completion notice", func() bool {
		for _, c := range fake.callsTo("sendMessage") {
			if c.Form["text"] == "Authentication verified. Send a message to start chatting." {
				return true
			}
		}
		return false
	})
}

func TestDeviceLoginCancellationDoesNotProveNativeExit(t *testing.T) {
	b, _, _ := diagnosticFixture(t)
	ctx, release, err := b.topicIngress.Enter(t.Context(), buildChatJID(streamChat), 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx = withTopicOutputOrigin(ctx, streamChat, 7)
	loginCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	exec := &delayedDeviceLogin{eof: make(chan struct{})}
	b.loginAuthCheck = func(ctx context.Context, _ *loginSession) bool { return ctx.Err() == nil }
	session := &loginSession{chatID: streamChat, thread: 0, private: true, ctx: loginCtx, cancel: cancel, exec: exec}
	if !b.publishLogin(ctx, threadedMsg(0, "/login"), session, runner.NewCodexProvider("test")) {
		t.Fatal("publish failed")
	}
	release()
	b.topicIngress.Block(buildChatJID(streamChat), 7)
	timeout, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := b.topicIngress.Wait(timeout, buildChatJID(streamChat), 7); err != nil {
		t.Fatal(err)
	}
	b.topicMu.Lock()
	uncertain := b.topicUncertain[chatThread{streamChat, 7}]
	b.topicMu.Unlock()
	if !uncertain {
		t.Fatal("closing login attachment incorrectly proved native exit")
	}
}
