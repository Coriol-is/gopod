package runner

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testSessA = "11111111-2222-4333-8444-555555555555"
	testSessB = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

func TestClaudeRunCmdSession(t *testing.T) {
	p := NewClaudeProvider("")
	cases := []struct {
		name string
		s    SessionRef
		want []string
	}{
		{"zero is legacy --continue", SessionRef{},
			[]string{"claude", "--continue", "--dangerously-skip-permissions", "--append-system-prompt", "SYS", "-p", "hi"}},
		{"new session by id", SessionRef{ID: testSessA},
			[]string{"claude", "--session-id", testSessA, "--dangerously-skip-permissions", "--append-system-prompt", "SYS", "-p", "hi"}},
		{"resume by id", SessionRef{ID: testSessA, Resume: true},
			[]string{"claude", "--resume", testSessA, "--dangerously-skip-permissions", "--append-system-prompt", "SYS", "-p", "hi"}},
		{"resume without id falls back to --continue", SessionRef{Resume: true},
			[]string{"claude", "--continue", "--dangerously-skip-permissions", "--append-system-prompt", "SYS", "-p", "hi"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.RunCmd("hi", "SYS", c.s); !reflect.DeepEqual(got, c.want) {
				t.Errorf("RunCmd =\n %q\nwant\n %q", got, c.want)
			}
		})
	}
	// No system prompt: the flag pair is omitted entirely.
	got := p.RunCmd("hi", "", SessionRef{ID: testSessA, Resume: true})
	want := []string{"claude", "--resume", testSessA, "--dangerously-skip-permissions", "-p", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RunCmd without system prompt = %q, want %q", got, want)
	}
}

func TestCodexRunCmdSession(t *testing.T) {
	p := NewCodexProvider("")
	flags := []string{"--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check"}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, s := range parts {
			out = append(out, s...)
		}
		return out
	}
	prompt := []string{"SYS\n\n---\n\nhi"}
	cases := []struct {
		name string
		s    SessionRef
		want []string
	}{
		{"zero is legacy resume --last", SessionRef{},
			cat([]string{"codex", "exec", "resume", "--last"}, flags, prompt)},
		{"resume by id", SessionRef{ID: testSessA, Resume: true},
			cat([]string{"codex", "exec", "resume", testSessA}, flags, prompt)},
		{"new conversation is plain exec", SessionRef{ID: testSessA},
			cat([]string{"codex", "exec"}, flags, prompt)},
		{"resume without id falls back to --last", SessionRef{Resume: true},
			cat([]string{"codex", "exec", "resume", "--last"}, flags, prompt)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.RunCmd("hi", "SYS", c.s); !reflect.DeepEqual(got, c.want) {
				t.Errorf("RunCmd =\n %q\nwant\n %q", got, c.want)
			}
		})
	}
}

// runShell executes a provider-built command with HOME pointed at home
// and returns trimmed stdout and whether it exited 0.
func runShell(t *testing.T, home string, cmd []string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath(cmd[0]); err != nil {
		t.Skipf("%s not available: %v", cmd[0], err)
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err == nil
}

func writeFile(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// claudeFixture lays out ~/.claude/projects the way Claude Code 2.1.x
// does: <projects>/<cwd with / . _ replaced by ->/<uuid>.jsonl plus an
// optional <uuid>/ directory for subagent transcripts and tool results.
func claudeFixture(t *testing.T) (home, a, aDir, b string) {
	home = t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-workspace-chat")
	now := time.Now()
	a = filepath.Join(proj, testSessA+".jsonl")
	aDir = filepath.Join(proj, testSessA, "subagents", "agent-1.jsonl")
	b = filepath.Join(proj, testSessB+".jsonl")
	writeFile(t, a, now.Add(-time.Hour))
	writeFile(t, aDir, now.Add(-time.Hour))
	writeFile(t, b, now)
	return home, a, aDir, b
}

// codexFixture lays out ~/.codex/sessions as codex-cli 0.16x does:
// sessions/YYYY/MM/DD/rollout-<local time>-<thread id>.jsonl.
func codexFixture(t *testing.T) (home, a, b string) {
	home = t.TempDir()
	now := time.Now()
	a = filepath.Join(home, ".codex", "sessions", "2026", "10", "23", "rollout-2026-10-23T09-00-00-"+testSessA+".jsonl")
	b = filepath.Join(home, ".codex", "sessions", "2026", "10", "24", "rollout-2026-10-24T09-00-00-"+testSessB+".jsonl")
	writeFile(t, a, now)                 // older file, touched last
	writeFile(t, b, now.Add(-time.Hour)) // newer file, touched earlier
	return home, a, b
}

func TestClaudeClearSessionCmd(t *testing.T) {
	p := NewClaudeProvider("")

	t.Run("by id removes only that session", func(t *testing.T) {
		home, a, aDir, b := claudeFixture(t)
		if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{ID: testSessA})); !ok {
			t.Fatal("clear by id failed")
		}
		if exists(a) || exists(filepath.Dir(filepath.Dir(aDir))) {
			t.Errorf("session %s not removed", testSessA)
		}
		if !exists(b) {
			t.Errorf("unrelated session %s was removed", testSessB)
		}
	})

	t.Run("zero ref wipes all sessions", func(t *testing.T) {
		home, _, _, _ := claudeFixture(t)
		if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{})); !ok {
			t.Fatal("wipe failed")
		}
		if exists(filepath.Join(home, ".claude", "projects")) {
			t.Error("projects dir survived wipe-all")
		}
	})

	t.Run("unknown id is a no-op success", func(t *testing.T) {
		home, a, _, b := claudeFixture(t)
		if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{ID: "99999999-2222-4333-8444-555555555555"})); !ok {
			t.Fatal("clear of unknown id failed")
		}
		if !exists(a) || !exists(b) {
			t.Error("clearing an unknown id removed files")
		}
	})

	for _, bad := range []string{"..", "../..", "a/b", "*", "x y", "$(id)", "abc", testSessA + "0"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			home, a, _, b := claudeFixture(t)
			if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{ID: bad})); ok {
				t.Errorf("invalid id %q accepted", bad)
			}
			if !exists(a) || !exists(b) {
				t.Errorf("invalid id %q removed files", bad)
			}
		})
	}
}

func TestCodexClearSessionCmd(t *testing.T) {
	p := NewCodexProvider("")

	t.Run("by id removes only that rollout", func(t *testing.T) {
		home, a, b := codexFixture(t)
		if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{ID: testSessA})); !ok {
			t.Fatal("clear by id failed")
		}
		if exists(a) {
			t.Errorf("rollout %s not removed", testSessA)
		}
		if !exists(b) {
			t.Errorf("unrelated rollout %s was removed", testSessB)
		}
	})

	t.Run("by id with no sessions dir succeeds", func(t *testing.T) {
		if _, ok := runShell(t, t.TempDir(), p.ClearSessionCmd(SessionRef{ID: testSessA})); !ok {
			t.Fatal("clear on empty home failed")
		}
	})

	t.Run("zero ref wipes all sessions", func(t *testing.T) {
		home, _, _ := codexFixture(t)
		if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{})); !ok {
			t.Fatal("wipe failed")
		}
		if exists(filepath.Join(home, ".codex", "sessions")) {
			t.Error("sessions dir survived wipe-all")
		}
	})

	for _, bad := range []string{"..", "a/b", "*", "x y", "$(id)", "abc", "*" + testSessA[8:]} {
		t.Run("rejects "+bad, func(t *testing.T) {
			home, a, b := codexFixture(t)
			if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{ID: bad})); ok {
				t.Errorf("invalid id %q accepted", bad)
			}
			if !exists(a) || !exists(b) {
				t.Errorf("invalid id %q removed files", bad)
			}
		})
	}
}

func TestClearSessionCmdZeroIsLegacy(t *testing.T) {
	// Byte-identical to the pre-SessionRef commands.
	if got, want := NewClaudeProvider("").ClearSessionCmd(SessionRef{}),
		[]string{"sh", "-c", `rm -rf "$HOME/.claude/projects" "$HOME/.claude/.active_session"`}; !reflect.DeepEqual(got, want) {
		t.Errorf("claude = %q, want %q", got, want)
	}
	if got, want := NewCodexProvider("").ClearSessionCmd(SessionRef{}),
		[]string{"sh", "-c", `rm -rf "$HOME/.codex/sessions" "$HOME/.codex/conversations"`}; !reflect.DeepEqual(got, want) {
		t.Errorf("codex = %q, want %q", got, want)
	}
}

func TestLatestSessionIDCmd(t *testing.T) {
	t.Run("claude picks most recently modified", func(t *testing.T) {
		home, _, _, _ := claudeFixture(t)
		got, ok := runShell(t, home, NewClaudeProvider("").LatestSessionIDCmd())
		if !ok || got != testSessB {
			t.Errorf("got %q ok=%v, want %q", got, ok, testSessB)
		}
	})
	t.Run("codex picks most recently modified", func(t *testing.T) {
		// codex 0.164 `exec resume --last` follows the most recently
		// updated rollout, not the newest file name.
		home, _, _ := codexFixture(t)
		got, ok := runShell(t, home, NewCodexProvider("").LatestSessionIDCmd())
		if !ok || got != testSessA {
			t.Errorf("got %q ok=%v, want %q", got, ok, testSessA)
		}
	})
	for _, p := range []AgentProvider{NewClaudeProvider(""), NewCodexProvider("")} {
		t.Run(p.Name()+" empty home prints nothing", func(t *testing.T) {
			got, ok := runShell(t, t.TempDir(), p.LatestSessionIDCmd())
			if !ok || got != "" {
				t.Errorf("got %q ok=%v, want empty success", got, ok)
			}
		})
	}
}

func TestCodexRunFreshCmdEphemeral(t *testing.T) {
	// --ephemeral (codex-cli 0.160 `exec --help`: "Run without persisting
	// session files to disk") keeps system tasks out of ~/.codex/sessions.
	got := NewCodexProvider("").RunFreshCmd("hi")
	want := []string{"codex", "exec", "--ephemeral",
		"--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RunFreshCmd = %q, want %q", got, want)
	}
}

func TestCodexLatestSessionIDLayout(t *testing.T) {
	p := NewCodexProvider("")
	now := time.Now()

	t.Run("any directory depth", func(t *testing.T) {
		home := t.TempDir()
		flat := filepath.Join(home, ".codex", "sessions", "rollout-2026-10-24T09-00-00-"+testSessA+".jsonl")
		deep := filepath.Join(home, ".codex", "sessions", "2026", "10", "24", "x", "rollout-2026-10-24T08-00-00-"+testSessB+".jsonl")
		writeFile(t, flat, now.Add(-time.Hour))
		writeFile(t, deep, now)
		got, ok := runShell(t, home, p.LatestSessionIDCmd())
		if !ok || got != testSessB {
			t.Errorf("got %q ok=%v, want %q", got, ok, testSessB)
		}
		writeFile(t, flat, now.Add(time.Minute))
		if got, _ := runShell(t, home, p.LatestSessionIDCmd()); got != testSessA {
			t.Errorf("after touching flat: got %q, want %q", got, testSessA)
		}
	})

	t.Run("newer RunFresh-style rollout wins", func(t *testing.T) {
		// RunFreshCmd is --ephemeral now, so new system tasks write no
		// rollout. Rollouts left by pre-ephemeral system tasks are
		// indistinguishable from conversations by name, so the newest
		// one is reported: it is exactly what the legacy `exec resume
		// --last` would have resumed, which is what migration adopts.
		home, _, _ := codexFixture(t)
		fresh := "cccccccc-dddd-4eee-8fff-000000000000"
		writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "24",
			"rollout-2026-10-24T10-00-00-"+fresh+".jsonl"), now.Add(time.Minute))
		if got, _ := runShell(t, home, p.LatestSessionIDCmd()); got != fresh {
			t.Errorf("got %q, want %q", got, fresh)
		}
	})

	t.Run("non-rollout files are ignored", func(t *testing.T) {
		home, _, _ := codexFixture(t)
		writeFile(t, filepath.Join(home, ".codex", "sessions", "notes.jsonl"), now.Add(time.Hour))
		if got, _ := runShell(t, home, p.LatestSessionIDCmd()); got != testSessA {
			t.Errorf("got %q, want %q", got, testSessA)
		}
	})
}

func TestClearSessionCmdRejectsIDFragment(t *testing.T) {
	// The last two groups of testSessA: with a loose pattern,
	// rollout-*-<fragment>.jsonl would match testSessA's rollout.
	frag := testSessA[strings.LastIndex(testSessA[:len(testSessA)-13], "-")+1:]
	if frag != "8444-555555555555" {
		t.Fatalf("fragment = %q", frag)
	}
	t.Run("codex", func(t *testing.T) {
		home, a, b := codexFixture(t)
		if _, ok := runShell(t, home, NewCodexProvider("").ClearSessionCmd(SessionRef{ID: frag})); ok {
			t.Error("fragment id accepted")
		}
		if !exists(a) || !exists(b) {
			t.Error("fragment id removed files")
		}
	})
	t.Run("claude", func(t *testing.T) {
		home, a, _, b := claudeFixture(t)
		if _, ok := runShell(t, home, NewClaudeProvider("").ClearSessionCmd(SessionRef{ID: frag})); ok {
			t.Error("fragment id accepted")
		}
		if !exists(a) || !exists(b) {
			t.Error("fragment id removed files")
		}
	})
	if got := NewCodexProvider("").ExtractSessionID(`{"type":"thread.started","thread_id":"` + frag + `"}`); got != "" {
		t.Errorf("ExtractSessionID accepted fragment: %q", got)
	}
}

func TestExtractSessionID(t *testing.T) {
	codex := NewCodexProvider("")

	// Recorded from `codex exec --json` (codex-cli 0.164.0, empty
	// CODEX_HOME, no auth): the first event names the thread.
	f, err := os.Open(filepath.Join("testdata", "codex_exec_json_thread_started.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ids []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if id := codex.ExtractSessionID(sc.Text()); id != "" {
			ids = append(ids, id)
		}
	}
	if want := []string{"01a16df7-698c-7ad3-b9f7-1e54b7681b73"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("json events: ids = %q, want %q", ids, want)
	}

	// Recorded stderr header of plain `codex exec` (same version).
	hdr, err := os.ReadFile(filepath.Join("testdata", "codex_exec_plain_header.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, line := range strings.Split(string(hdr), "\n") {
		if id := codex.ExtractSessionID(line); id != "" {
			ids = append(ids, id)
		}
	}
	if want := []string{"01a16df7-d74d-76d1-90d4-e2b3d3ad2717"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("plain header: ids = %q, want %q", ids, want)
	}

	for _, line := range []string{
		"",
		`{"type":"turn.started"}`,
		`{"type":"thread.started"}`,
		`{"type":"thread.started","thread_id":"../etc"}`,
		"session id: not/an/id",
		"session id: abc",
		"hello session id: x",
		"{not json",
	} {
		if got := codex.ExtractSessionID(line); got != "" {
			t.Errorf("ExtractSessionID(%q) = %q, want empty", line, got)
		}
	}

	if got := NewClaudeProvider("").ExtractSessionID(`{"type":"thread.started","thread_id":"x"}`); got != "" {
		t.Errorf("claude ExtractSessionID = %q, want empty", got)
	}
}

// shellExit runs cmd like runShell and returns its exit code.
func shellExit(t *testing.T, home string, cmd []string) int {
	t.Helper()
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		t.Fatalf("running %q: %v", cmd, err)
		return -1
	}
}

func TestSessionExistsCmd(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	const unknown = "99999999-2222-4333-8444-555555555555"
	claudeHome, _, _, _ := claudeFixture(t)
	codexHome, _, _ := codexFixture(t)
	cases := []struct {
		name string
		p    AgentProvider
		home string
		id   string
		want int
	}{
		{"claude present", NewClaudeProvider(""), claudeHome, testSessA, 0},
		{"claude missing", NewClaudeProvider(""), claudeHome, unknown, 1},
		{"claude empty home", NewClaudeProvider(""), t.TempDir(), testSessA, 1},
		{"claude invalid id", NewClaudeProvider(""), claudeHome, "*", 2},
		{"codex present", NewCodexProvider(""), codexHome, testSessB, 0},
		{"codex missing", NewCodexProvider(""), codexHome, unknown, 1},
		{"codex empty home", NewCodexProvider(""), t.TempDir(), testSessA, 1},
		{"codex invalid id", NewCodexProvider(""), codexHome, "*" + testSessA[8:], 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shellExit(t, c.home, c.p.SessionExistsCmd(c.id)); got != c.want {
				t.Errorf("exit = %d, want %d", got, c.want)
			}
		})
	}
	// The cleared session is missing afterwards: the two commands agree
	// on where a session lives.
	for _, p := range providersUnderTest() {
		home := claudeHome
		id := testSessB
		if p.Name() == "codex" {
			home, id = codexHome, testSessA
		}
		if _, ok := runShell(t, home, p.ClearSessionCmd(SessionRef{ID: id})); !ok {
			t.Fatalf("%s: clear failed", p.Name())
		}
		if got := shellExit(t, home, p.SessionExistsCmd(id)); got != 1 {
			t.Errorf("%s: exists after clear = %d, want 1", p.Name(), got)
		}
	}
}

func TestAnnouncesSessionID(t *testing.T) {
	if NewClaudeProvider("").AnnouncesSessionID() {
		t.Error("claude takes the caller's id; it must not announce one")
	}
	if !NewCodexProvider("").AnnouncesSessionID() {
		t.Error("codex picks its own thread id; it must announce it")
	}
}
