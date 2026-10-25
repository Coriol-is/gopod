package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Claude Code structured output (`-p --output-format stream-json
// --verbose --include-partial-messages`), as recorded from Claude Code
// 2.1.310 on 2026-10-25 (testdata/claude_stream_*.jsonl). One JSON
// object per line; the fields gopod reads:
//
//	{"type":"system","subtype":"init","session_id":"<uuid>",...}
//	{"type":"stream_event","parent_tool_use_id":null,
//	 "event":{"type":"content_block_delta","index":1,
//	          "delta":{"type":"text_delta","text":"..."}}}
//	{"type":"assistant","parent_tool_use_id":null,
//	 "message":{"content":[{"type":"tool_use","name":"Bash",
//	                        "input":{"command":"echo hi",...}}]}}
//	{"type":"user","message":{"content":[{"type":"tool_result",...}]}}
//	{"type":"result","subtype":"success","is_error":false,
//	 "result":"<final reply>","session_id":"<uuid>"}
//
// Everything else (system status/hook/thinking_tokens events,
// rate_limit_event, thinking and input_json deltas, tool results) is
// valid but ignored. Text blocks of main-agent assistant messages
// become StreamMessageText, the reply's fallback when the result line is
// missing (Claude 2.1.289 and 2.1.310 send one assistant line per
// content block: text, tool_use or thinking). An authentication failure
// is a result with is_error true and the message in "result" (exit
// status 1, empty stderr); the assistant message carrying the same text
// is not streamed as text deltas.

// claudeStreamFlags switch `claude -p` to structured output. --verbose
// is required by stream-json in print mode; --include-partial-messages
// adds the text deltas.
var claudeStreamFlags = []string{
	"--output-format", "stream-json", "--verbose", "--include-partial-messages",
}

// StructuredCmd inserts claudeStreamFlags before the trailing
// `-p <prompt>` of a RunCmd command.
func (p *ClaudeProvider) StructuredCmd(cmd []string) []string {
	n := len(cmd)
	if n < 2 || cmd[n-2] != "-p" {
		return cmd
	}
	out := make([]string, 0, n+len(claudeStreamFlags))
	out = append(out, cmd[:n-2]...)
	out = append(out, claudeStreamFlags...)
	return append(out, cmd[n-2:]...)
}

// claudeStreamLine is the subset of a stream-json line gopod reads.
// Message stays raw: a user message's content may be a string, and
// only assistant messages are decoded further.
type claudeStreamLine struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Event           json.RawMessage `json:"event"`
	Message         json.RawMessage `json:"message"`
	Result          *string         `json:"result"`
	IsError         bool            `json:"is_error"`
}

type claudeStreamDelta struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type claudeAssistantMessage struct {
	Content []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
		Text  string          `json:"text"`
	} `json:"content"`
}

var errNotStreamEvent = errors.New("not a stream-json event")

// ParseStreamLine parses one stream-json line.
func (p *ClaudeProvider) ParseStreamLine(line []byte) ([]StreamEvent, error) {
	var l claudeStreamLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil, fmt.Errorf("claude stream line: %w", err)
	}
	if l.Type == "" {
		return nil, errNotStreamEvent
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			return []StreamEvent{{Kind: StreamInit, SessionID: l.SessionID}}, nil
		}
	case "stream_event":
		// Subagent output (parent_tool_use_id set) is not the answer.
		if l.ParentToolUseID != nil || len(l.Event) == 0 {
			return nil, nil
		}
		var ev claudeStreamDelta
		if err := json.Unmarshal(l.Event, &ev); err != nil {
			return nil, fmt.Errorf("claude stream event: %w", err)
		}
		if ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
			return []StreamEvent{{Kind: StreamText, Text: ev.Delta.Text}}, nil
		}
	case "assistant":
		var m claudeAssistantMessage
		if len(l.Message) == 0 {
			return nil, nil
		}
		if err := json.Unmarshal(l.Message, &m); err != nil {
			return nil, fmt.Errorf("claude assistant message: %w", err)
		}
		var evs []StreamEvent
		for _, c := range m.Content {
			switch {
			case c.Type == "tool_use" && c.Name != "":
				evs = append(evs, StreamEvent{Kind: StreamTool, Tool: c.Name, Detail: claudeToolDetail(c.Name, c.Input)})
			case c.Type == "text" && c.Text != "" && l.ParentToolUseID == nil:
				// The reply's fallback if the result line never comes.
				evs = append(evs, StreamEvent{Kind: StreamMessageText, Text: c.Text})
			}
		}
		return evs, nil
	case "result":
		ev := StreamEvent{Kind: StreamResult, IsError: l.IsError, SessionID: l.SessionID}
		if l.Result != nil {
			ev.Text = *l.Result
		}
		return []StreamEvent{ev}, nil
	}
	return nil, nil
}

// claudeToolInputKey names the input field shown for each tool. Tools
// not listed show their name only.
var claudeToolInputKey = map[string]string{
	"Bash":         "command",
	"Edit":         "file_path",
	"MultiEdit":    "file_path",
	"Write":        "file_path",
	"Read":         "file_path",
	"NotebookEdit": "notebook_path",
	"Grep":         "pattern",
	"Glob":         "pattern",
	"WebSearch":    "query",
	"WebFetch":     "url",
}

// claudeToolDetail returns the shown input of a tool call, capped at
// maxDetailBytes, or "".
func claudeToolDetail(tool string, input json.RawMessage) string {
	key, ok := claudeToolInputKey[tool]
	if !ok || len(input) == 0 {
		return ""
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(in[key], &s) != nil {
		return ""
	}
	return truncateBytes(strings.TrimSpace(s), maxDetailBytes)
}

// Compile-time check.
var _ StructuredStreamer = (*ClaudeProvider)(nil)
