package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

type elevatedLoginEngine interface {
	ExecElevatedLogin(context.Context, string, []string, io.ReadCloser, io.Writer) (*ExecResult, error)
}

// ElevatedLogin confines authorization output and stdin to the private login UI.
// Its manager lease excludes inference and is cancelled by Stop/Close/restart.
type ElevatedLogin struct {
	ctx        context.Context
	Provider   AgentProvider
	reader     *bufio.Scanner
	output     *io.PipeReader
	input      *io.PipeWriter
	cancel     context.CancelFunc
	done       chan struct{}
	verified   bool // published by closing done
	manager    *Elevated
	generation string
	once       sync.Once
}

func (l *ElevatedLogin) Context() context.Context { return l.ctx }

func (l *ElevatedLogin) ReadLine() (string, error) {
	if l.reader.Scan() {
		return l.reader.Text(), nil
	}
	if err := l.reader.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}
func (l *ElevatedLogin) WriteStdin(p []byte) (int, error) { return l.input.Write(p) }
func (l *ElevatedLogin) Close() error {
	l.once.Do(func() { l.cancel(); _ = l.output.Close(); _ = l.input.Close() })
	return nil
}
func (l *ElevatedLogin) Verified(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-l.done:
	}
	return l.verified && l.manager.Generation() == l.generation
}

func (e *Elevated) BeginLogin(ctx context.Context) (*ElevatedLogin, error) {
	engine, ok := e.engine.(elevatedLoginEngine)
	if !ok {
		return nil, errors.New("ROOT: вход недоступен; проверьте Docker")
	}
	loginCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	startup := time.AfterFunc(30*time.Second, cancel)
	x, err := e.beginExecution(loginCtx)
	startup.Stop()
	if err != nil {
		cancel()
		return nil, err
	}
	var provider AgentProvider = NewClaudeProvider(x.cfg.Image)
	if x.session.Provider == "codex" {
		provider = NewCodexProvider(x.cfg.Image)
	}
	input, inputWriter := io.Pipe()
	output, outputWriter := io.Pipe()
	l := &ElevatedLogin{ctx: x.ctx, Provider: provider, reader: bufio.NewScanner(output), output: output, input: inputWriter, cancel: cancel, done: make(chan struct{}), manager: e, generation: x.session.ID}
	// Closing both pipes also unblocks a stalled reader/writer on timeout or Close.
	stop := context.AfterFunc(x.ctx, func() { _ = input.Close(); _ = outputWriter.Close() })
	go func() {
		defer func() {
			stop()
			_ = input.Close()
			_ = outputWriter.Close()
			x.finish()
			cancel()
			close(l.done)
		}()
		result, runErr := runElevatedJournaled(x.ctx, e.st, x.session.ID, provider.LoginCmd(), func() (*ExecResult, error) {
			return engine.ExecElevatedLogin(x.ctx, x.id, provider.LoginCmd(), input, outputWriter)
		})
		if result == nil {
			e.mu.Lock()
			e.blocked = true
			e.mu.Unlock()
			return
		}
		if runErr != nil || result.ExitCode != 0 || x.ctx.Err() != nil {
			return
		}
		authCtx, end := context.WithTimeout(x.ctx, 15*time.Second)
		defer end()
		auth, authErr := runElevatedJournaled(authCtx, e.st, x.session.ID, provider.AuthStatusCmd(), func() (*ExecResult, error) {
			return e.engine.ExecElevated(authCtx, x.id, provider.AuthStatusCmd())
		})
		if auth == nil {
			e.mu.Lock()
			e.blocked = true
			e.mu.Unlock()
			return
		}
		if authErr != nil || auth.ExitCode != 0 || x.ctx.Err() != nil {
			return
		}
		status, parseErr := provider.ParseAuthStatus(auth.Stdout + auth.Stderr)
		l.verified = parseErr == nil && status.LoggedIn
	}()
	return l, nil
}
