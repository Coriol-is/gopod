package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Only these audited getMe observations are retained. Missing is not false.
var capabilityFields = []string{
	"supports_inline_queries", "supports_guest_queries", "can_connect_to_business",
	"has_topics_enabled", "allows_users_to_create_topics", "can_join_groups", "can_read_all_group_messages",
}

type capabilityResult struct {
	settings   map[string]string
	username   string
	valid      bool
	class      string
	retryAfter time.Duration
}
type capabilityFetcher func(context.Context) capabilityResult

// newCapabilityFetcher deliberately exposes neither raw API errors nor bodies.
// The token-bearing URL must never escape this closure through an error/log.
func newCapabilityFetcher(token string, client *http.Client) capabilityFetcher {
	if client == nil {
		client = http.DefaultClient
	}
	// Diagnostics never follow redirects, even when the shared polling client does.
	diagnosticClient := *client
	diagnosticClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	expectedID, _ := strconv.ParseInt(strings.SplitN(token, ":", 2)[0], 10, 64)
	return func(ctx context.Context) capabilityResult {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/getMe", nil)
		if err != nil {
			return capabilityResult{class: "transport"}
		}
		resp, err := diagnosticClient.Do(req)
		if err != nil {
			return capabilityResult{class: capabilityErrorClass(ctx, err)}
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
		if err != nil {
			return capabilityResult{class: capabilityErrorClass(ctx, err)}
		}
		if len(data) > 1<<20 {
			return capabilityResult{class: "schema"}
		}
		var envelope struct {
			OK         bool            `json:"ok"`
			ErrorCode  int             `json:"error_code"`
			Result     json.RawMessage `json:"result"`
			Parameters struct {
				RetryAfter int64 `json:"retry_after"`
			} `json:"parameters"`
		}
		decodeErr := json.Unmarshal(data, &envelope)
		code := resp.StatusCode
		if envelope.ErrorCode != 0 {
			code = envelope.ErrorCode
		}
		switch {
		case code == 429:
			seconds := envelope.Parameters.RetryAfter
			if seconds < 0 {
				seconds = 0
			}
			if seconds > 300 {
				seconds = 300
			}
			return capabilityResult{class: "rate_limit", retryAfter: time.Duration(seconds) * time.Second}
		case code == 401 || code == 403:
			return capabilityResult{class: "authentication"}
		case resp.StatusCode < 200 || resp.StatusCode >= 300:
			return capabilityResult{class: "api_rejection"}
		case decodeErr != nil:
			return capabilityResult{class: "schema"}
		case !envelope.OK:
			return capabilityResult{class: "api_rejection"}
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(envelope.Result, &fields) != nil {
			return capabilityResult{class: "schema"}
		}
		var id int64
		var isBot bool
		var username string
		if json.Unmarshal(fields["id"], &id) != nil || id <= 0 || id != expectedID ||
			json.Unmarshal(fields["is_bot"], &isBot) != nil || !isBot ||
			json.Unmarshal(fields["username"], &username) != nil || !validCapabilityUsername(username) {
			return capabilityResult{class: "schema"}
		}
		result := capabilityResult{settings: make(map[string]string), username: username, valid: true}
		for _, name := range capabilityFields {
			result.settings[name] = "unknown"
			raw, exists := fields[name]
			if !exists {
				continue
			}
			var value *bool
			if json.Unmarshal(raw, &value) != nil || value == nil {
				result.class = "schema"
				continue
			}
			if *value {
				result.settings[name] = "enabled"
			} else {
				result.settings[name] = "disabled"
			}
		}
		return result
	}
}

func validCapabilityUsername(s string) bool {
	if len(s) < 5 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// Collapse wrapped HTTP/body errors to bounded classes before leaving transport.
func capabilityErrorClass(ctx context.Context, err error) string {
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout"
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "transport"
}
