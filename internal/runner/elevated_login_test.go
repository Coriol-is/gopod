package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type loginEngineFake struct {
	*elevatedFake
	login func(context.Context, io.ReadCloser, io.Writer) (*ExecResult, error)
}

func (f *loginEngineFake) ExecElevatedLogin(ctx context.Context, _ string, _ []string, in io.ReadCloser, out io.Writer) (*ExecResult, error) {
	return f.login(ctx, in, out)
}

func TestElevatedLoginSettlesWithoutRecordingCredentials(t *testing.T) {
	e, f := elevatedFixture(t)
	calls := 0
	f.run = func(context.Context) (*ExecResult, error) {
		calls++
		if calls == 1 {
			return &ExecResult{ExitCode: 1, Stderr: "Not logged in"}, nil
		}
		return &ExecResult{Stdout: "Logged in using ChatGPT"}, nil
	}
	engine := &loginEngineFake{elevatedFake: f, login: func(ctx context.Context, in io.ReadCloser, out io.Writer) (*ExecResult, error) {
		_, _ = io.WriteString(out, "https://auth.openai.com/codex/device\nSYNTHETIC-CODE\n")
		b := make([]byte, 64)
		n, _ := in.Read(b)
		if string(b[:n]) != "private-credential\n" {
			t.Error("stdin not delivered")
		}
		return &ExecResult{ExitCode: 0}, nil
	}}
	e.engine = engine
	if _, err := e.Start(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	l, err := e.BeginLogin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err = e.Run(t.Context(), "must not run"); err == nil {
		t.Fatal("inference admitted during login")
	}
	for i := 0; i < 2; i++ {
		if _, err = l.ReadLine(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = l.WriteStdin([]byte("private-credential\n")); err != nil {
		t.Fatal(err)
	}
	if _, err = l.ReadLine(); err != io.EOF {
		t.Fatal("missing EOF", err)
	}
	if !l.Verified(t.Context()) {
		t.Fatal("auth not verified")
	}
	rows, err := e.st.ListElevatedExecs(t.Context(), 10)
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		if strings.Contains(row.Argv, "private-credential") || strings.Contains(row.Argv, "SYNTHETIC") {
			t.Fatal("credential in journal")
		}
	}
	if err = e.Close(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	if l.Verified(t.Context()) {
		t.Fatal("closed generation verifies")
	}
}

func TestElevatedLoginStopSettlesAndUncertainRemainsBlocked(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "uncertain"}[uncertain], func(t *testing.T) {
			e, f := elevatedFixture(t)
			f.run = func(context.Context) (*ExecResult, error) { return &ExecResult{Stdout: `{"loggedIn":false}`}, nil }
			e.engine = &loginEngineFake{elevatedFake: f, login: func(ctx context.Context, _ io.ReadCloser, out io.Writer) (*ExecResult, error) {
				_, _ = io.WriteString(out, "login-started\n")
				<-ctx.Done()
				if uncertain {
					return nil, ErrExecutionUnproven
				}
				return &ExecResult{ExitCode: 137}, nil
			}}
			if _, err := e.Start(t.Context(), "claude"); err != nil {
				t.Fatal(err)
			}
			l, err := e.BeginLogin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = l.ReadLine(); err != nil {
				t.Fatal(err)
			}
			if !e.StopAnswer() {
				t.Fatal("login not cancellable")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if l.Verified(ctx) {
				t.Fatal("cancelled login verified")
			}
			select {
			case <-l.Context().Done():
			case <-time.After(time.Second):
				t.Fatal("stop did not cancel login UI")
			}
			e.mu.Lock()
			blocked, busy := e.blocked, e.busy
			e.mu.Unlock()
			if blocked != uncertain || busy {
				t.Fatal(blocked, busy)
			}
			if err = e.Close(t.Context(), "test"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestElevatedLoginProtectsExistingAuthAndUncertainStatus(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		result     bool
		want       error
	}{
		{"authenticated", "Logged in using ChatGPT", true, ErrElevatedAlreadyAuthenticated},
		{"authenticated-json", `{"loggedIn":true,"detail":"not logged in previously"}`, true, ErrElevatedAlreadyAuthenticated},
		{"unknown", "unexpected", true, nil},
		{"unproven", "", false, ErrExecutionUnproven},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f := elevatedFixture(t)
			f.run = func(context.Context) (*ExecResult, error) {
				if !tc.result {
					return nil, ErrExecutionUnproven
				}
				return &ExecResult{Stdout: tc.text}, nil
			}
			e.engine = &loginEngineFake{elevatedFake: f, login: func(context.Context, io.ReadCloser, io.Writer) (*ExecResult, error) {
				t.Error("login must not touch credentials")
				return &ExecResult{}, nil
			}}
			if _, err := e.Start(t.Context(), "codex"); err != nil {
				t.Fatal(err)
			}
			if l, err := e.BeginLogin(t.Context()); l != nil || err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatal(l, err)
			}
			e.mu.Lock()
			busy, blocked := e.busy, e.blocked
			e.mu.Unlock()
			if busy || blocked != !tc.result {
				t.Fatal(busy, blocked)
			}
			if err := e.Close(t.Context(), "test"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestElevatedLoginRejectsAmbiguousNativeStatus(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			text string
			exit int
		}{
			{`{}`, 0}, {`null`, 0}, {`{"error":"failed"}`, 1}, {`{"loggedIn":null}`, 0},
			{`{"loggedIn":false}`, 137}, {`{"loggedIn":false}`, -1},
		} {
			t.Run(provider+tc.text+fmt.Sprint(tc.exit), func(t *testing.T) {
				e, f := elevatedFixture(t)
				f.run = func(context.Context) (*ExecResult, error) {
					return &ExecResult{Stdout: tc.text, ExitCode: tc.exit}, nil
				}
				e.engine = &loginEngineFake{elevatedFake: f, login: func(context.Context, io.ReadCloser, io.Writer) (*ExecResult, error) {
					t.Error("unsafe login launched")
					return &ExecResult{}, nil
				}}
				if _, err := e.Start(t.Context(), provider); err != nil {
					t.Fatal(err)
				}
				if l, err := e.BeginLogin(t.Context()); err == nil || l != nil {
					t.Fatal(l, err)
				}
				if err := e.Close(t.Context(), "test"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Telegram's timeout watcher closes the handle when Context is cancelled.
// Finishing exec must leave the output readable until the consumer verifies it.
func TestElevatedLoginCompletionDoesNotTriggerConsumerCancellation(t *testing.T) {
	e, f := elevatedFixture(t)
	calls := 0
	f.run = func(context.Context) (*ExecResult, error) {
		calls++
		if calls == 1 {
			return &ExecResult{ExitCode: 1, Stderr: "Not logged in"}, nil
		}
		return &ExecResult{Stdout: "Logged in using ChatGPT"}, nil
	}
	e.engine = &loginEngineFake{elevatedFake: f, login: func(context.Context, io.ReadCloser, io.Writer) (*ExecResult, error) { return &ExecResult{}, nil }}
	if _, err := e.Start(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	l, err := e.BeginLogin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	watcherDone := make(chan struct{})
	go func() { <-l.Context().Done(); _ = l.Close(); close(watcherDone) }()
	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("completion stalled")
	}
	if err := l.Context().Err(); err != nil {
		t.Fatalf("successful exec cancelled UI before verification: %v", err)
	}
	if _, err := l.ReadLine(); err != io.EOF {
		t.Fatalf("expected clean EOF: %v", err)
	}
	if !l.Verified(t.Context()) {
		t.Fatal("completed login not verified")
	}
	_ = l.Close()
	select {
	case <-watcherDone:
	case <-time.After(time.Second):
		t.Fatal("consumer cleanup leaked watcher")
	}
	if err := e.Close(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
}
