package runner

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"unicode/utf8"
)

// Structured agent output.
//
// A provider that can print its run as line-delimited JSON events
// (Claude Code's `--output-format stream-json`) implements
// StructuredStreamer. The runner then reads stdout line by line,
// forwards answer text deltas on AgentStream.Chunks and tool calls on
// AgentStream.Progress, and takes the final reply from the run's result
// event. A provider without the capability (Codex) stays on plain text.

// StructuredStreamer is an optional AgentProvider capability.
type StructuredStreamer interface {
	// StructuredCmd turns a RunCmd command (after withModel) into one
	// that prints line-delimited JSON events. A command it does not
	// recognise is returned unchanged; its plain output then reaches
	// the user through the parser's text fallback.
	StructuredCmd(cmd []string) []string

	// ParseStreamLine parses one line of that output into zero or more
	// events. An error means the line is not an event; the reader skips
	// it.
	ParseStreamLine(line []byte) ([]StreamEvent, error)
}

// StreamEventKind says what a StreamEvent carries.
type StreamEventKind int

const (
	// StreamInit: the run started; SessionID is the agent's session.
	StreamInit StreamEventKind = iota + 1
	// StreamText: a delta of the main agent's reply text (Text).
	StreamText
	// StreamTool: the agent called a tool (Tool, Detail).
	StreamTool
	// StreamResult: the run ended; Text is the final reply, IsError
	// reports a failed run (Text then holds the error message).
	StreamResult
	// StreamMessageText: the complete text (Text) of a main-agent
	// message. Not streamed to the user (the deltas are); kept as the
	// reply's fallback when the result event is missing.
	StreamMessageText
)

// StreamEvent is one parsed event of a structured agent run.
type StreamEvent struct {
	Kind      StreamEventKind
	Text      string
	Tool      string
	Detail    string
	IsError   bool
	SessionID string
}

// ProgressEvent is one step of the agent's work, shown while it runs
// instead of its output.
type ProgressEvent struct {
	// Tool is the tool name as the agent reports it ("Bash", "Read").
	Tool string
	// Detail is the tool's main input: the command, path, pattern,
	// query or URL; "" for other tools. Never tool output. Not
	// redacted: the consumer redacts before showing it.
	Detail string
}

// maxDetailBytes caps ProgressEvent.Detail; displays cut it far shorter.
const maxDetailBytes = 512

// structuredOutput is what readStructuredStream collected.
type structuredOutput struct {
	// Text is the final reply, by priority: the result event's text;
	// the last main-agent message text since the last tool call; the
	// text deltas since the last tool call; the last main-agent message
	// text at all. When no line parsed at all, the whole stdout.
	Text string
	// Raw is all of stdout while no line has parsed (the plain-text
	// fallback), otherwise at most the last rawTailBytes of it, for
	// diagnostics. Never searched for auth errors: in structured mode
	// it is the transcript, tool results and file contents included.
	Raw string
	// Stderr is the exec's stderr.
	Stderr string
	// Parsed counts lines that parsed as events; Malformed those that
	// did not.
	Parsed, Malformed int
	// Result reports whether a result event arrived.
	Result bool
	// SessionID is the session the agent announced at init, if any.
	SessionID string
}

// rawTailBytes bounds Raw once stdout has proved to be structured.
const rawTailBytes = 64 << 10

// readStructuredStream reads an exec's stdout as line-delimited events
// until EOF. Answer text deltas go to onText and tool calls to
// onTool, in stdout order, from the calling goroutine.
//
// It is tolerant: a line that does not parse is skipped, and when no
// line parses at all (an agent CLI that ignored the flags, or printed
// only an error) stdout is treated as plain text: it is passed to
// onText once and becomes Text, so the answer is never lost. Lines may
// be split across reads; they are reassembled before parsing.
//
// Stderr is drained concurrently, for the reason given at
// readExecStream.
func readStructuredStream(stdout, stderr io.Reader, parse func([]byte) ([]StreamEvent, error), onText func(string), onTool func(ProgressEvent)) structuredOutput {
	stderrCh := make(chan string, 1)
	go func() {
		var b boundedBuffer
		_, _ = io.Copy(&b, stderr)
		stderrCh <- b.String()
	}()

	var (
		out       structuredOutput
		raw       []byte        // see structuredOutput.Raw
		answer    boundedBuffer // main agent text deltas since the last tool call
		msgSince  string        // last main-agent message text since the last tool call
		lastMsg   string        // last main-agent message text
		resultTxt string
	)
	br := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, err := readBoundedLine(br)
		if len(line) > 0 {
			raw = append(raw, line...)
			if len(raw) > maxAgentOutputBytes {
				raw = append(raw[:0], raw[len(raw)-maxAgentOutputBytes:]...)
			}
			if out.Parsed > 0 && len(raw) > 2*rawTailBytes {
				raw = append(raw[:0], raw[len(raw)-rawTailBytes:]...)
			}
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				evs, perr := parse(trimmed)
				if perr != nil {
					out.Malformed++
				} else {
					out.Parsed++
					for _, ev := range evs {
						switch ev.Kind {
						case StreamInit:
							out.SessionID = ev.SessionID
						case StreamText:
							answer.WriteString(ev.Text)
							onText(ev.Text)
						case StreamMessageText:
							msgSince, lastMsg = ev.Text, ev.Text
						case StreamTool:
							// Text before a tool call was narration, not
							// the answer.
							answer.Reset()
							msgSince = ""
							onTool(ProgressEvent{Tool: ev.Tool, Detail: ev.Detail})
						case StreamResult:
							out.Result = true
							resultTxt = ev.Text
						}
					}
				}
			}
		}
		if err != nil {
			break // EOF or a closed stream
		}
	}
	if out.Parsed > 0 && len(raw) > rawTailBytes {
		raw = raw[len(raw)-rawTailBytes:]
	}
	out.Raw = string(raw)
	out.Stderr = <-stderrCh

	if out.Parsed == 0 {
		out.Text = strings.TrimSpace(out.Raw)
		if out.Text != "" {
			onText(out.Raw)
		}
		return out
	}
	for _, t := range []string{resultTxt, msgSince, answer.String(), lastMsg} {
		if t = strings.TrimSpace(t); t != "" {
			out.Text = t
			break
		}
	}
	return out
}

// truncateBytes cuts s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Drain an oversized event without retaining it or parsing a truncated JSON
// prefix. The next complete event can still supply the final answer.
func readBoundedLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	oversized := false
	for {
		fragment, err := br.ReadSlice('\n')
		if !oversized {
			if len(line)+len(fragment) > maxAgentOutputBytes {
				oversized = true
				line = nil
			} else {
				line = append(line, fragment...)
			}
		}
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}
