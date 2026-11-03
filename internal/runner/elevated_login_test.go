package runner

import (
	"context"
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
	f.run = func(context.Context) (*ExecResult, error) { return &ExecResult{Stdout: "Logged in using ChatGPT"}, nil }
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
	if err != nil || len(rows) != 2 {
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
