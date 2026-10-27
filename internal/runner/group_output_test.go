package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/store"
)

type markedGroupProvider struct{ AgentProvider }

func (markedGroupProvider) GroupExecEnv() []string { return nil }

type sanitizedExecFixture struct {
	agentExecer
	result           *ExecResult
	err, errorStream error
}

func (f sanitizedExecFixture) Exec(ctx context.Context, id string, cmd, env []string) (*ExecResult, error) {
	inner := cmd
	if len(inner) > 4 && inner[0] == "sh" && strings.Contains(inner[2], `echo $$ >`) {
		inner = inner[4:]
	}
	if len(inner) > 1 && (inner[0] == "codex" && inner[1] == "exec" || inner[0] == "claude" && strings.Contains(strings.Join(inner, " "), "-p")) {
		return f.result, f.err
	}
	return f.agentExecer.Exec(ctx, id, cmd, env)
}
func (f sanitizedExecFixture) ExecStream(ctx context.Context, id string, cmd, env []string) (*StreamHandle, error) {
	if f.errorStream != nil {
		return nil, f.errorStream
	}
	return f.agentExecer.ExecStream(ctx, id, cmd, env)
}

func TestGroupCapabilityRedactionAcrossEveryChunkBoundary(t *testing.T) {
	cap := strings.Repeat("secretcap", 8)
	mcp := "http://gopod-relay:8787/mcp/" + cap
	proxy := "http://group:" + cap + "@gopod-relay:8787"
	text := strings.Repeat("Обычный публичный текст. ", 30) + mcp + " next " + proxy + " done https://example.com/public"
	expected := redactGroupCapabilities(text)
	if strings.Contains(expected, cap) || !strings.Contains(expected, "https://example.com/public") {
		t.Fatal("unsafe final redaction")
	}
	for size := 1; size < len(text); size++ {
		var filter groupChunkFilter
		var got strings.Builder
		for start := 0; start < len(text); start += size {
			end := start + size
			if end > len(text) {
				end = len(text)
			}
			chunk := filter.Push(text[start:end])
			if !utf8.ValidString(chunk) {
				t.Fatalf("invalid UTF8 chunk size=%d", size)
			}
			got.WriteString(chunk)
		}
		got.WriteString(filter.Flush())
		if got.String() != expected {
			t.Fatalf("boundary %d corrupted or exposed capability", size)
		}
	}
	if got := groupOutputText(NewCodexProvider(""), text); got != text {
		t.Fatal("private/personal output changed")
	}
}
func TestGroupBufferedAndFreshSanitationPreserveSessionCapture(t *testing.T) {
	cap := strings.Repeat("secretcap", 8)
	secret := "/protected/auth.json " + cap
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprint(fresh), func(t *testing.T) {
			p := markedGroupProvider{NewCodexProvider("")}
			h := newConvHarness(t, p)
			session := "01234567-89ab-cdef-0123-456789abcdef"
			h.r.ex = sanitizedExecFixture{agentExecer: h.home, result: &ExecResult{Stdout: "reply http://gopod-relay:8787/mcp/" + cap, Stderr: "session id: " + session + "\n" + secret}}
			var reply string
			var err error
			if fresh {
				reply, err = h.r.RunFresh(context.Background(), convChat, TierRegistered, nil, "summary")
			} else {
				reply, err = h.r.Run(context.Background(), convChat, thread7, TierRegistered, nil, "prompt")
			}
			if err != nil || strings.Contains(reply, cap) {
				t.Fatalf("reply=%q err=%v", reply, err)
			}
			if !fresh && h.row(thread7) != session {
				t.Fatal("internal Codex stderr was removed before session capture")
			}
			h.r.ex = sanitizedExecFixture{agentExecer: h.home, result: &ExecResult{ExitCode: 9, Stderr: secret}}
			if fresh {
				_, err = h.r.RunFresh(context.Background(), convChat, TierRegistered, nil, "summary")
			} else {
				_, err = h.r.Run(context.Background(), convChat, thread7, TierRegistered, nil, "prompt")
			}
			if !errors.Is(err, ErrGroupOperationUnavailable) || strings.Contains(err.Error(), cap) {
				t.Fatalf("unsafe group error %v", err)
			}
		})
	}
}
func TestGroupStreamHidesStderrAndGenericDockerErrors(t *testing.T) {
	cap := strings.Repeat("secretcap", 8)
	secret := "login-code PRIVATE /protected/auth.json " + cap
	for _, group := range []bool{false, true} {
		t.Run(fmt.Sprint(group), func(t *testing.T) {
			var p AgentProvider = NewCodexProvider("")
			if group {
				p = markedGroupProvider{p}
			}
			r, fx, st := newStopHarness(t, p)
			stream, err := r.startStream(context.Background(), "cid", convKey{convChat, thread7}, TierRegistered, nil, p, "prompt", func(ref SessionRef) []string { return p.RunCmd("hi", "", ref) })
			if err != nil {
				t.Fatal(err)
			}
			process := <-fx.started
			go func() {
				_, _ = io.WriteString(process.stderrW, "session id: 01234567-89ab-cdef-0123-456789abcdef\n"+secret)
				_, _ = io.WriteString(process.stdoutW, "answer http://group:"+cap+"@gopod-relay:8787")
				process.exit(5)
			}()
			var chunks strings.Builder
			for chunk := range stream.Chunks {
				chunks.WriteString(chunk)
			}
			result := <-stream.Result
			if group {
				if result.Stderr != "" || !errors.Is(result.Err, ErrGroupOperationUnavailable) || strings.Contains(chunks.String(), cap) {
					t.Fatalf("group leak: %+v chunks=%q", result, chunks.String())
				}
			} else {
				if !strings.Contains(result.Stderr, secret) || !strings.Contains(result.Err.Error(), secret) {
					t.Fatal("personal stderr behavior changed")
				}
			}
			if st.row(convChat, thread7, "codex") != "01234567-89ab-cdef-0123-456789abcdef" {
				t.Fatal("stream lost internal announced session")
			}
			r.ex = sanitizedExecFixture{agentExecer: fx, errorStream: errors.New(secret)}
			_, err = r.startStream(context.Background(), "cid", convKey{convChat, thread7}, TierRegistered, nil, p, "prompt", func(ref SessionRef) []string { return p.RunCmd("hi", "", ref) })
			if group && (!errors.Is(err, ErrGroupOperationUnavailable) || strings.Contains(err.Error(), cap)) {
				t.Fatalf("startup error leak %v", err)
			}
		})
	}
}
func TestGroupSafeErrorRetainsTypedQuotaAndAuthentication(t *testing.T) {
	for _, original := range []error{&UsageLimitError{Provider: "codex"}, ErrNotLoggedIn, ErrSessionExpired, context.Canceled, context.DeadlineExceeded} {
		wrapped := fmt.Errorf("private error detail: %w", original)
		safe := groupPublicError(wrapped)
		if !errors.Is(safe, original) && !errors.Is(safe, ErrUsageLimit) {
			t.Fatalf("lost classification %v", original)
		}
		if strings.Contains(safe.Error(), "private") {
			t.Fatal("wrapped detail exposed")
		}
	}
}

type allowedGroupRuntime struct{}

func (allowedGroupRuntime) Authorize(context.Context, store.RegisteredChat) error { return nil }
func (allowedGroupRuntime) Start(context.Context, store.RegisteredChat, AgentProvider, int) (string, AgentProvider, func(), error) {
	panic("group maintenance reached runtime")
}
func TestGroupCompactPreservesContextWithoutInvokingCallback(t *testing.T) {
	h := newConvHarness(t, NewCodexProvider(""))
	h.r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:-2", Folder: convChat}}})
	h.r.SetGroupRuntime(allowedGroupRuntime{})
	called := false
	h.r.SetCompactFn(func(context.Context, string, int) error { called = true; return errors.New("private summary error") })
	_, err := h.r.CompactSession(context.Background(), convChat, thread7, TierOwner, nil)
	if !errors.Is(err, ErrGroupMaintenanceUnavailable) || called || h.ensures != 0 {
		t.Fatalf("unsafe maintenance called=%v ensures=%d err=%v", called, h.ensures, err)
	}
}

type failedGroupRuntime struct{ allowedGroupRuntime }

func (failedGroupRuntime) Start(context.Context, store.RegisteredChat, AgentProvider, int) (string, AgentProvider, func(), error) {
	return "", nil, func() {}, errors.New("Docker private authfile http://group:" + strings.Repeat("secretcap", 8) + "@gopod-relay:8787")
}
func TestGroupRuntimeStartupErrorsAreGenericAcrossAllRunModes(t *testing.T) {
	r := &Runner{}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:-2", Folder: convChat}}})
	r.SetGroupRuntime(failedGroupRuntime{})
	for _, run := range []func() error{
		func() error {
			_, err := r.Run(context.Background(), convChat, thread7, TierRegistered, nil, "prompt")
			return err
		},
		func() error {
			_, err := r.RunFresh(context.Background(), convChat, TierRegistered, nil, "prompt")
			return err
		},
		func() error {
			_, err := r.RunStream(context.Background(), convChat, thread7, TierRegistered, nil, "prompt")
			return err
		},
	} {
		if err := run(); !errors.Is(err, ErrGroupOperationUnavailable) || strings.Contains(err.Error(), "secretcap") {
			t.Fatalf("unsafe startup failure %v", err)
		}
	}
}

type fixedResultExec struct {
	result *ExecResult
	err    error
}

func (f fixedResultExec) Exec(context.Context, string, []string, []string) (*ExecResult, error) {
	return f.result, f.err
}
func (f fixedResultExec) ExecStream(context.Context, string, []string, []string) (*StreamHandle, error) {
	panic("unexpected stream")
}
func TestGroupSessionProbeDiagnosticsNeverLogRawNativeDetails(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			var prov AgentProvider = NewCodexProvider("")
			if group {
				prov = markedGroupProvider{prov}
			}
			var logs strings.Builder
			r := &Runner{log: slog.New(slog.NewTextHandler(&logs, nil))}
			secret := "private-authfile-secret-canary"
			if failure {
				r.ex = fixedResultExec{err: errors.New(secret)}
			} else {
				r.ex = fixedResultExec{result: &ExecResult{ExitCode: 2, Stderr: secret}}
			}
			_ = r.sessionGone(context.Background(), "cid", prov, "01234567-89ab-cdef-0123-456789abcdef")
			if strings.Contains(logs.String(), secret) == group {
				t.Fatalf("group=%v failure=%v unsafe diagnostic=%s", group, failure, logs.String())
			}
		}
	}
}
