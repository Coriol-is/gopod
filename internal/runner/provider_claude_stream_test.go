package runner

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// parseAll parses every line of a fixture, failing on a malformed one.
func parseAll(t *testing.T, data []byte) []StreamEvent {
	t.Helper()
	p := NewClaudeProvider("img")
	var evs []StreamEvent
	for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		got, err := p.ParseStreamLine(line)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		evs = append(evs, got...)
	}
	return evs
}

// TestClaudeStreamRecordedRun parses a run recorded from Claude Code
// 2.1.310 (testdata/README.md): init, three tool calls, text deltas,
// result.
func TestClaudeStreamRecordedRun(t *testing.T) {
	evs := parseAll(t, readFixture(t, "claude_stream_tools.jsonl"))

	const sid = "sess-0001"
	const reply = "`note.txt` says \"hello file\". (`echo hi` printed `hi`; the glob `*.txt` matched only `note.txt`.)"
	var tools []ProgressEvent
	var text strings.Builder
	var result *StreamEvent
	for i, ev := range evs {
		switch ev.Kind {
		case StreamInit:
			if i != 0 || ev.SessionID != sid {
				t.Errorf("init event %d: %+v", i, ev)
			}
		case StreamTool:
			tools = append(tools, ProgressEvent{ev.Tool, ev.Detail})
		case StreamText:
			text.WriteString(ev.Text)
		case StreamResult:
			result = &evs[i]
		}
	}
	wantTools := []ProgressEvent{
		{"Bash", "echo hi"},
		{"Read", "/workspace/chat/note.txt"},
		{"Glob", "*.txt"},
	}
	if !reflect.DeepEqual(tools, wantTools) {
		t.Errorf("tools = %+v\nwant %+v", tools, wantTools)
	}
	if text.String() != reply {
		t.Errorf("text deltas = %q", text.String())
	}
	if result == nil || result.Text != reply || result.IsError || result.SessionID != sid {
		t.Errorf("result = %+v", result)
	}
	if evs[len(evs)-1].Kind != StreamResult {
		t.Error("result is not the last event")
	}
}

// TestClaudeStreamRecordedAuthError: a failed authentication arrives
// as a result with is_error and the message in "result", and no text
// deltas (recorded with an invalid API key).
func TestClaudeStreamRecordedAuthError(t *testing.T) {
	evs := parseAll(t, readFixture(t, "claude_stream_auth_error.jsonl"))
	var res []StreamEvent
	for _, ev := range evs {
		if ev.Kind == StreamText || ev.Kind == StreamTool {
			t.Errorf("unexpected %+v", ev)
		}
		if ev.Kind == StreamResult {
			res = append(res, ev)
		}
	}
	if len(res) != 1 || !res[0].IsError || res[0].Text != "Invalid API key · Fix external API key" {
		t.Fatalf("result events = %+v", res)
	}
}

// Tool inputs for the mapped tools not in the recorded run. The line
// shape is the recorded one; the input field names are those of Claude
// Code's tool schemas (constructed, see testdata/README.md).
func TestClaudeStreamToolDetails(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"Bash", `{"command":"go test ./...","description":"run tests"}`, "go test ./..."},
		{"Edit", `{"file_path":"/workspace/chat/a.go","old_string":"x","new_string":"y"}`, "/workspace/chat/a.go"},
		{"MultiEdit", `{"file_path":"/w/b.go","edits":[]}`, "/w/b.go"},
		{"Write", `{"file_path":"/w/c.py","content":"print(1)"}`, "/w/c.py"},
		{"Read", `{"file_path":"/w/d.txt"}`, "/w/d.txt"},
		{"NotebookEdit", `{"notebook_path":"/w/n.ipynb","new_source":"x"}`, "/w/n.ipynb"},
		{"Grep", `{"pattern":"func main","path":"/w"}`, "func main"},
		{"Glob", `{"pattern":"**/*.go"}`, "**/*.go"},
		{"WebSearch", `{"query":"go 1.26 release notes"}`, "go 1.26 release notes"},
		{"WebFetch", `{"url":"https://go.dev/doc","prompt":"summarize"}`, "https://go.dev/doc"},
		{"TodoWrite", `{"todos":[]}`, ""},
		{"mcp__memory__search", `{"q":"x"}`, ""},
		{"Bash", `{"command":42}`, ""},
	}
	p := NewClaudeProvider("img")
	for _, c := range cases {
		line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"` + c.name +
			`","input":` + c.input + `}]},"parent_tool_use_id":null,"session_id":"s"}`
		evs, err := p.ParseStreamLine([]byte(line))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		want := []StreamEvent{{Kind: StreamTool, Tool: c.name, Detail: c.want}}
		if !reflect.DeepEqual(evs, want) {
			t.Errorf("%s: got %+v, want %+v", c.name, evs, want)
		}
	}
}

func TestClaudeStreamLineEdgeCases(t *testing.T) {
	p := NewClaudeProvider("img")
	for _, bad := range []string{`not json`, `{"type":`, `null`, `{}`, `"text"`} {
		if _, err := p.ParseStreamLine([]byte(bad)); err == nil {
			t.Errorf("%q parsed without error", bad)
		}
	}
	ignored := []string{
		// a user message whose content is a string, not blocks
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		// a subagent's text is not the answer
		`{"type":"stream_event","parent_tool_use_id":"toolu_1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"sub"}}}`,
		`{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}}`,
		`{"type":"system","subtype":"status","status":"requesting"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"some_future_event","x":1}`,
	}
	for _, line := range ignored {
		evs, err := p.ParseStreamLine([]byte(line))
		if err != nil || len(evs) != 0 {
			t.Errorf("%s: got %+v, %v; want no events, no error", line, evs, err)
		}
	}
	evs, err := p.ParseStreamLine([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))
	if err != nil || len(evs) != 1 || !evs[0].IsError || evs[0].Text != "" {
		t.Errorf("result without text: %+v, %v", evs, err)
	}
	evs, _ = p.ParseStreamLine([]byte(`{"type":"result","is_error":true,"result":"Failed to authenticate: OAuth session expired"}`))
	if len(evs) != 1 || !p.IsNotLoggedInError("", evs[0].Text) {
		t.Errorf("auth result not recognised: %+v", evs)
	}
}

func TestClaudeToolDetailCapped(t *testing.T) {
	long := strings.Repeat("é", maxDetailBytes) // 2 bytes per rune
	d := claudeToolDetail("Bash", []byte(`{"command":"`+long+`"}`))
	if len(d) > maxDetailBytes || !strings.HasPrefix(long, d) {
		t.Errorf("detail %d bytes, not a rune-aligned prefix", len(d))
	}
}

func TestClaudeStructuredCmd(t *testing.T) {
	p := NewClaudeProvider("img")
	cmd := withModel(p.RunCmd("hi", "sys", SessionRef{}), p, "opus")
	got := p.StructuredCmd(cmd)
	want := []string{"claude", "--continue", "--dangerously-skip-permissions",
		"--append-system-prompt", "sys", "--model", "opus",
		"--output-format", "stream-json", "--verbose", "--include-partial-messages", "-p", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv =\n%q\nwant\n%q", got, want)
	}
	odd := []string{"claude", "hi"}
	if got := p.StructuredCmd(odd); !reflect.DeepEqual(got, odd) {
		t.Errorf("unrecognised cmd changed: %q", got)
	}
}

// --- readStructuredStream ---------------------------------------------

type streamCollect struct {
	order []string // "text:<t>" and "tool:<name>" in arrival order
}

func (c *streamCollect) read(stdout io.Reader) structuredOutput {
	p := NewClaudeProvider("img")
	return readStructuredStream(stdout, strings.NewReader(""), p.ParseStreamLine,
		func(s string) { c.order = append(c.order, "text:"+s) },
		func(ev ProgressEvent) { c.order = append(c.order, "tool:"+ev.Tool+" "+ev.Detail) })
}

const (
	lineText1  = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look. "}}}`
	lineTool   = `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	lineText2  = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Two files."}}}`
	lineResult = `{"type":"result","subtype":"success","is_error":false,"result":"Two files.","session_id":"s"}`
)

func TestReadStructuredStreamOrderAndResult(t *testing.T) {
	var c streamCollect
	in := strings.Join([]string{lineText1, lineTool, "garbage {", lineText2, lineResult}, "\n") + "\n"
	// OneByteReader splits every line across many reads.
	out := c.read(iotest.OneByteReader(strings.NewReader(in)))
	want := []string{"text:Let me look. ", "tool:Bash ls", "text:Two files."}
	if !reflect.DeepEqual(c.order, want) {
		t.Errorf("order = %q\nwant %q", c.order, want)
	}
	if out.Text != "Two files." || !out.Result || out.Parsed != 4 || out.Malformed != 1 {
		t.Errorf("out = %+v", out)
	}
}

// Without a result event (killed run, truncated output) the reply is
// the answer text since the last tool call, never narration before it.
func TestReadStructuredStreamNoResultKeepsAnswerText(t *testing.T) {
	var c streamCollect
	out := c.read(strings.NewReader(lineText1 + "\n" + lineTool + "\n" + lineText2)) // no trailing newline
	if out.Text != "Two files." || out.Result {
		t.Errorf("out = %+v", out)
	}
}

// When nothing parses, stdout is the reply and is passed on as text.
func TestReadStructuredStreamFallsBackToPlainText(t *testing.T) {
	var c streamCollect
	out := c.read(iotest.HalfReader(strings.NewReader("Plain answer\nsecond line\n")))
	if out.Text != "Plain answer\nsecond line" || out.Parsed != 0 || out.Malformed != 2 {
		t.Errorf("out = %+v", out)
	}
	if !reflect.DeepEqual(c.order, []string{"text:Plain answer\nsecond line\n"}) {
		t.Errorf("order = %q", c.order)
	}
}

// An init line far over bufio's default 4 KiB buffer must not break
// line reassembly (real init lines list hundreds of tools).
func TestReadStructuredStreamLongLine(t *testing.T) {
	var c streamCollect
	init := `{"type":"system","subtype":"init","session_id":"s","tools":["` + strings.Repeat("x", 200<<10) + `"]}`
	out := c.read(strings.NewReader(init + "\n" + lineResult + "\n"))
	if out.Parsed != 2 || out.SessionID != "s" || out.Text != "Two files." {
		t.Errorf("out = %+v", out)
	}
}

// --- fix round 1: raw bound, assistant-text fallback, thinking ------

const (
	lineInit        = `{"type":"system","subtype":"init","session_id":"sess-0001"}`
	lineThinking    = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"secret plan"}}}`
	lineThinkingMsg = `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"thinking","thinking":"secret plan","signature":"sig-placeholder"}]}}`
	lineAsstText    = `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"text","text":"The answer is 42."}]}}`
	lineSubText     = `{"type":"assistant","parent_tool_use_id":"toolu_0001","message":{"content":[{"type":"text","text":"subagent says hi"}]}}`
)

func TestClaudeStreamThinkingNeverReachesChunks(t *testing.T) {
	var c streamCollect
	out := c.read(strings.NewReader(strings.Join([]string{lineInit, lineThinking, lineThinkingMsg, lineText2, lineResult}, "\n")))
	for _, o := range c.order {
		if strings.Contains(o, "secret plan") {
			t.Fatalf("thinking leaked: %q", c.order)
		}
	}
	if out.Text != "Two files." {
		t.Errorf("text = %q", out.Text)
	}
}

func TestReadStructuredStreamReplyPriority(t *testing.T) {
	cases := map[string]struct {
		lines []string
		want  string
	}{
		"no deltas, no result: assistant text": {[]string{lineInit, lineTool, lineAsstText}, "The answer is 42."},
		"truncated result line":                {[]string{lineInit, lineAsstText, `{"type":"result","subtype":"succ`}, "The answer is 42."},
		"result wins over assistant text":      {[]string{lineInit, lineAsstText, lineResult}, "Two files."},
		"assistant text wins over deltas":      {[]string{lineText2, lineAsstText}, "The answer is 42."},
		"narration before a tool loses to deltas after it": {
			[]string{lineAsstText, lineTool, lineText2}, "Two files."},
		"narration is the last resort":    {[]string{lineAsstText, lineTool}, "The answer is 42."},
		"subagent text is not the answer": {[]string{lineInit, lineSubText}, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var col streamCollect
			out := col.read(strings.NewReader(strings.Join(c.lines, "\n")))
			if out.Text != c.want {
				t.Errorf("text = %q, want %q", out.Text, c.want)
			}
			for _, o := range col.order {
				if strings.HasPrefix(o, "text:The answer") {
					t.Errorf("assistant message text sent as a chunk: %q", col.order)
				}
			}
		})
	}
}

// A long transcript (tool results, file contents) is not kept: Raw is
// bounded once stdout proved structured. The plain fallback still keeps
// everything (TestReadStructuredStreamFallsBackToPlainText).
func TestReadStructuredStreamRawBounded(t *testing.T) {
	big := `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + strings.Repeat("x", 8<<10) + `"}]}}`
	var b strings.Builder
	b.WriteString(lineInit + "\n")
	for i := 0; i < 600; i++ { // ~5 MB
		b.WriteString(big + "\n")
	}
	b.WriteString(lineResult + "\n")
	var c streamCollect
	out := c.read(strings.NewReader(b.String()))
	if len(out.Raw) > rawTailBytes {
		t.Errorf("Raw keeps %d bytes, want at most %d", len(out.Raw), rawTailBytes)
	}
	if !strings.HasSuffix(out.Raw, lineResult+"\n") {
		t.Error("Raw is not the tail of stdout")
	}
	if out.Text != "Two files." || out.Parsed != 602 {
		t.Errorf("out: text %q parsed %d", out.Text, out.Parsed)
	}
}
