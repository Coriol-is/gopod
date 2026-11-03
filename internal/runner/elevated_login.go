package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

var ErrElevatedAlreadyAuthenticated = errors.New("ROOT: профиль уже авторизован")

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
	// Native login may delete existing credentials before showing its prompt.
	// Check under the same lease; never launch login over a valid profile.
	authCtx, endAuth := context.WithTimeout(x.ctx, 15*time.Second)
	auth, authErr := runElevatedJournaled(authCtx, e.st, x.session.ID, provider.AuthStatusCmd(), func() (*ExecResult, error) {
		return e.engine.ExecElevated(authCtx, x.id, provider.AuthStatusCmd())
	})
	endAuth()
	var preflightErr error
	if auth == nil {
		e.mu.Lock()
		e.blocked = true
		e.mu.Unlock()
		preflightErr = ErrExecutionUnproven
	} else if authErr != nil || x.ctx.Err() != nil {
		preflightErr = errors.New("ROOT: проверка авторизации не завершена")
	} else {
		status, parseErr := parseElevatedLoginPreflight(provider, auth)
		if parseErr != nil {
			preflightErr = errors.New("ROOT: статус авторизации не распознан")
		} else if status.LoggedIn {
			preflightErr = ErrElevatedAlreadyAuthenticated
		}
	}
	if preflightErr != nil {
		x.finish()
		cancel()
		return nil, preflightErr
	}
	input, inputWriter := io.Pipe()
	output, outputWriter := io.Pipe()
	// The UI lifetime ends on consumer cleanup or cancellation, not exec completion.
	l := &ElevatedLogin{ctx: loginCtx, Provider: provider, reader: bufio.NewScanner(output), output: output, input: inputWriter, cancel: cancel, done: make(chan struct{}), manager: e, generation: x.session.ID}
	// Closing both pipes also unblocks a stalled reader/writer on timeout or Close.
	stop := context.AfterFunc(x.ctx, func() { cancel(); _ = input.Close(); _ = outputWriter.Close() })
	go func() {
		defer func() {
			stop()
			_ = input.Close()
			_ = outputWriter.Close()
			x.finish()
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

// Only explicit native states authorize a potentially destructive login.
func parseElevatedLoginPreflight(provider AgentProvider, result *ExecResult) (AuthStatus, error) {
	if result.ExitCode != 0 && result.ExitCode != 1 {
		return AuthStatus{}, errors.New("unexpected auth status exit")
	}
	text := strings.TrimSpace(result.Stdout + result.Stderr)
	if provider.Name() == "codex" {
		switch strings.ToLower(text) {
		case "not logged in", "logged in using chatgpt", "logged in using api key":
			return provider.ParseAuthStatus(text)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return AuthStatus{}, err
	}
	raw, ok := fields["loggedIn"]
	if !ok || (string(raw) != "true" && string(raw) != "false") {
		return AuthStatus{}, errors.New("missing explicit auth state")
	}
	var status AuthStatus
	if err := json.Unmarshal([]byte(text), &status); err != nil {
		return AuthStatus{}, err
	}
	return status, nil
}
