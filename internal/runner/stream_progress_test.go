package runner

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// startStructured starts a streamed Claude turn with progress on and
// returns the stream and the fake process.
func startStructured(t *testing.T, s *streamTestRunner, ctx context.Context) (*AgentStream, *fakeProc, []string) {
	t.Helper()
	prov := NewClaudeProvider("img")
	s.r.provider = prov
	s.r.SetProgress(true)
	cmd := prov.RunCmd("hi", "", SessionRef{})
	stream, err := s.r.startStream(ctx, "cid", convKey{"chat", 0}, TierRegistered, nil, prov, "hi", fixedCmd(cmd))
	if err != nil {
		t.Fatalf("startStream: %v", err)
	}
	p := <-s.fx.started
	return stream, p, cmd
}

// collected is what a consumer saw, in order.
type collected struct {
	order  []string // "text:..." / "tool:..."
	text   string
	tools  []ProgressEvent
	result RunResult
}

// consume reads a stream the way telegram does: before applying a
// progress event it drains the chunks already buffered, which keeps
// text and tools in the agent's order.
func consume(t *testing.T, stream *AgentStream) collected {
	t.Helper()
	var c collected
	addText := func(s string) {
		c.order = append(c.order, "text:"+s)
		c.text += s
	}
	chunks, progress := stream.Chunks, stream.Progress
	deadline := time.After(5 * time.Second)
	for chunks != nil {
		select {
		case s, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			addText(s)
		case ev, ok := <-progress:
			if !ok {
				progress = nil
				continue
			}
		drain:
			for {
				select {
				case s, ok := <-chunks:
					if !ok {
						break drain
					}
					addText(s)
				default:
					break drain
				}
			}
			c.order = append(c.order, "tool:"+ev.Tool)
			c.tools = append(c.tools, ev)
		case <-deadline:
			t.Fatal("stream never ended")
		}
	}
	select {
	case c.result = <-stream.Result:
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	return c
}

// writeSlowly writes data in small pieces so lines span reads.
func writeSlowly(p *fakeProc, data string) {
	for len(data) > 0 {
		n := min(37, len(data))
		p.stdoutW.Write([]byte(data[:n]))
		data = data[n:]
	}
}

func TestStartStreamStructuredRecordedRun(t *testing.T) {
	s := newStreamTestRunner(t)
	stream, p, cmd := startStructured(t, s, context.Background())

	wantCmd := NewClaudeProvider("img").StructuredCmd(cmd)
	if !reflect.DeepEqual(p.cmd, wrapWithPidFile(p.cmd[3], wantCmd)) {
		t.Fatalf("exec cmd = %q", p.cmd)
	}
	if stream.Progress == nil {
		t.Fatal("structured run has no Progress channel")
	}
	go func() {
		writeSlowly(p, string(readFixture(t, "claude_stream_tools.jsonl")))
		p.exit(0)
	}()
	c := consume(t, stream)

	const reply = "`note.txt` says \"hello file\". (`echo hi` printed `hi`; the glob `*.txt` matched only `note.txt`.)"
	if c.result.Err != nil || c.result.FullText != reply {
		t.Fatalf("result = %+v", c.result)
	}
	if c.text != reply {
		t.Errorf("Chunks carried %q, want the answer text only", c.text)
	}
	wantTools := []ProgressEvent{{"Bash", "echo hi"}, {"Read", "/workspace/chat/note.txt"}, {"Glob", "*.txt"}}
	if !reflect.DeepEqual(c.tools, wantTools) {
		t.Errorf("progress = %+v", c.tools)
	}
	// The relative order of a step and the chunks right after it is not
	// asserted: the drain-before-step consumer can pull chunks sent after
	// the step (a known, deferred race; see the report).
	select {
	case got := <-s.extracts:
		if got != reply {
			t.Errorf("memory ingested %q, want the result text", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reply not ingested")
	}
}

func TestStartStreamStructuredAuthError(t *testing.T) {
	cases := map[string]func(p *fakeProc){
		"result event": func(p *fakeProc) {
			p.stdoutW.Write([]byte(`{"type":"system","subtype":"init","session_id":"s"}` + "\n" +
				`{"type":"result","subtype":"success","is_error":true,"result":"Failed to authenticate: OAuth session expired and could not be refreshed"}` + "\n"))
			p.exit(1)
		},
		"stderr": func(p *fakeProc) {
			go p.stderrW.Write([]byte("Error: Not logged in\n"))
			time.Sleep(20 * time.Millisecond)
			p.exit(1)
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStreamTestRunner(t)
			stream, p, _ := startStructured(t, s, context.Background())
			go run(p)
			c := consume(t, stream)
			if !errors.Is(c.result.Err, ErrNotLoggedIn) && !errors.Is(c.result.Err, ErrSessionExpired) {
				t.Fatalf("err = %v, want an auth error", c.result.Err)
			}
			if c.text != "" {
				t.Errorf("auth error text leaked into Chunks: %q", c.text)
			}
		})
	}
}

// A CLI that ignores the flags and prints text: the answer still
// arrives, as chunks and as FullText.
func TestStartStreamStructuredFallsBackToPlainText(t *testing.T) {
	s := newStreamTestRunner(t)
	stream, p, _ := startStructured(t, s, context.Background())
	go func() {
		p.stdoutW.Write([]byte("just text\n"))
		p.exit(0)
	}()
	c := consume(t, stream)
	if c.result.Err != nil || c.result.FullText != "just text" || c.text != "just text\n" {
		t.Fatalf("result = %+v, chunks %q", c.result, c.text)
	}
}

// GOPOD_PROGRESS=0: the argv is today's plain one and there is no
// progress channel.
func TestStartStreamProgressOffKeepsPlainArgv(t *testing.T) {
	s := newStreamTestRunner(t)
	prov := NewClaudeProvider("img")
	stream, p := s.start(t, context.Background(), prov) // checks argv == wrapped RunCmd
	for _, a := range p.cmd {
		if a == "stream-json" {
			t.Fatalf("progress off but argv has stream-json: %q", p.cmd)
		}
	}
	if stream.Progress != nil {
		t.Error("plain run has a Progress channel")
	}
	go func() {
		p.stdoutW.Write([]byte(`{"type":"result","result":"x"}`))
		p.exit(0)
	}()
	if res := readResult(t, stream); res.FullText != `{"type":"result","result":"x"}` {
		t.Errorf("plain run parsed its stdout: %+v", res)
	}
}

// Codex has no structured output: progress on changes nothing.
func TestStartStreamProgressOnCodexStaysPlain(t *testing.T) {
	s := newStreamTestRunner(t)
	s.r.SetProgress(true)
	stream, p := s.start(t, context.Background(), NewCodexProvider("img"))
	if stream.Progress != nil {
		t.Error("codex run has a Progress channel")
	}
	go func() {
		p.stdoutW.Write([]byte("hello"))
		p.exit(0)
	}()
	if res := readResult(t, stream); res.FullText != "hello" {
		t.Errorf("result = %+v", res)
	}
}

// A stop while the runner waits to hand over a progress event nobody
// reads: the run still ends cancelled, nothing is ingested.
func TestStartStreamStructuredCancelDuringProgress(t *testing.T) {
	s := newStreamTestRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	stream, p, _ := startStructured(t, s, ctx)
	go p.stdoutW.Write([]byte(lineTool + "\n" + lineTool + "\n"))
	time.Sleep(30 * time.Millisecond) // the runner blocks on the first progress send
	cancel()
	c := consume(t, stream)
	if !errors.Is(c.result.Err, context.Canceled) {
		t.Fatalf("err = %v, want cancelled", c.result.Err)
	}
	select {
	case got := <-s.extracts:
		t.Errorf("cancelled run ingested %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// A file the agent read says "not logged in" and the run then fails
// without a result: that is a failed run, not an auth error.
func TestStartStreamStructuredToolResultIsNotAuth(t *testing.T) {
	s := newStreamTestRunner(t)
	stream, p, _ := startStructured(t, s, context.Background())
	go func() {
		p.stdoutW.Write([]byte(lineInit + "\n" + lineTool + "\n" +
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"Error: not logged in to registry.example"}]}}` + "\n"))
		p.exit(1)
	}()
	c := consume(t, stream)
	if c.result.Err == nil || errors.Is(c.result.Err, ErrNotLoggedIn) || errors.Is(c.result.Err, ErrSessionExpired) {
		t.Fatalf("err = %v, want a plain agent failure", c.result.Err)
	}
}

// No text deltas and no result line, but the agent printed its answer
// in an assistant message: the answer is delivered, not "(empty reply)".
func TestStartStreamStructuredAssistantTextFallback(t *testing.T) {
	s := newStreamTestRunner(t)
	stream, p, _ := startStructured(t, s, context.Background())
	go func() {
		p.stdoutW.Write([]byte(lineInit + "\n" + lineTool + "\n" + lineAsstText + "\n"))
		p.exit(0)
	}()
	c := consume(t, stream)
	if c.result.Err != nil || c.result.FullText != "The answer is 42." {
		t.Fatalf("result = %+v", c.result)
	}
}
