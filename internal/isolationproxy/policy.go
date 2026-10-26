// Package isolationproxy enforces a host-owned HTTPS egress capability for
// trusted group CLI runtimes. Docker isolation must make it the only egress;
// configuring HTTPS_PROXY alone does not enforce a network boundary.
package isolationproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// BodyPolicy identifies a reviewed request envelope; it is not supplied by the model.
type BodyPolicy string

const (
	NoBody            BodyPolicy = "none"
	AnthropicMessages BodyPolicy = "anthropic_messages"
	Responses         BodyPolicy = "responses"
	// ExactJSON permits only explicitly listed top-level fields (e.g. a qualified
	// refresh exchange). No endpoint or auth refresh route is enabled implicitly.
	ExactJSON BodyPolicy = "exact_json"
)

// Route is an exact host/method/path/query grant. Query is the exact raw query,
// not a wildcard. Host is a lowercase DNS name without a port. HTTPS/443 only.
type Route struct {
	Host       string
	Method     string
	Path       string
	Query      string
	Body       BodyPolicy
	JSONFields []string
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 || host != strings.ToLower(host) || net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return strings.Contains(host, ".")
}

func validateRoute(r Route) error {
	if !validHost(r.Host) || (r.Method != http.MethodGet && r.Method != http.MethodPost) || !strings.HasPrefix(r.Path, "/") || strings.ContainsAny(r.Path, "%?#\\") || strings.Contains(r.Path, "..") {
		return errors.New("invalid egress route")
	}
	switch r.Body {
	case NoBody:
		if r.Method != http.MethodGet {
			return errors.New("body-free route must use GET")
		}
	case AnthropicMessages, Responses:
		if r.Method != http.MethodPost {
			return errors.New("inference route must use POST")
		}
	case ExactJSON:
		if r.Method != http.MethodPost || len(r.JSONFields) == 0 {
			return errors.New("JSON route requires explicit fields")
		}
	default:
		return errors.New("unknown body policy")
	}
	if _, err := url.ParseQuery(r.Query); err != nil {
		return errors.New("invalid route query")
	}
	return nil
}

func (p *Proxy) route(r *http.Request, host string) (Route, bool) {
	if r.URL.Host != "" || r.URL.Scheme != "" || !strings.HasPrefix(r.RequestURI, "/") || r.URL.ForceQuery || r.URL.RawPath != "" || r.URL.Opaque != "" || r.URL.User != nil || r.URL.Fragment != "" || r.Host != host && r.Host != host+":443" {
		return Route{}, false
	}
	for _, grant := range p.routes {
		if grant.Host == host && grant.Method == r.Method && grant.Path == r.URL.Path && grant.Query == r.URL.RawQuery {
			return grant, true
		}
	}
	return Route{}, false
}

func (p *Proxy) validateBody(r *http.Request, grant Route) ([]byte, error) {
	if p.requireSubscriptionBearer {
		values := r.Header.Values("Authorization")
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if len(r.Header.Values("X-Api-Key")) != 0 || len(values) != 1 || !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n,") {
			return nil, errors.New("subscription authentication required")
		}
	}
	if r.Header.Get("Content-Encoding") != "" || r.Header.Get("Upgrade") != "" {
		return nil, errors.New("unsupported envelope")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, p.maxRequestBytes+1))
	if err != nil || int64(len(data)) > p.maxRequestBytes {
		return nil, errors.New("request limit")
	}
	if grant.Body == NoBody {
		if len(data) != 0 {
			return nil, errors.New("unexpected body")
		}
		return data, nil
	}
	if media := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]); media != "application/json" {
		return nil, errors.New("JSON required")
	}
	value, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	if grant.Body == ExactJSON {
		allowed := map[string]bool{}
		for _, field := range grant.JSONFields {
			allowed[field] = true
		}
		for field := range value {
			if !allowed[field] {
				return nil, errors.New("unapproved JSON field")
			}
		}
		return data, nil
	}
	// Provider-hosted tools/connectors can bypass local tool controls. Reject
	// their request selectors even if the native CLI would normally omit them.
	for _, field := range []string{"mcp_servers", "connectors", "connector_ids", "apps", "plugins", "include_tools", "computer", "agent", "background", "previous_response_id", "conversation"} {
		if _, ok := value[field]; ok {
			return nil, errors.New("unapproved server capability")
		}
	}
	allowedFields := map[string]bool{}
	fields := []string{"model", "messages", "system", "max_tokens", "tools", "tool_choice", "stream", "thinking", "metadata", "temperature", "top_p", "top_k", "stop_sequences", "service_tier", "context_management", "output_config"}
	if grant.Body == Responses {
		fields = []string{"model", "instructions", "input", "tools", "tool_choice", "parallel_tool_calls", "stream", "reasoning", "text", "max_output_tokens", "temperature", "top_p", "truncation", "include", "store", "metadata", "service_tier", "safety_identifier", "prompt_cache_key", "prompt_cache_retention", "stream_options", "client_metadata"}
	}
	for _, field := range fields {
		allowedFields[field] = true
	}
	for field := range value {
		if !allowedFields[field] {
			return nil, errors.New("unapproved inference field")
		}
	}
	if !localContextOnly(value) {
		return nil, errors.New("unapproved remote context")
	}
	seenTools := map[string]bool{}
	tools, ok := value["tools"]
	if ok {
		array, ok := tools.([]any)
		if !ok {
			return nil, errors.New("invalid tools")
		}
		for _, tool := range array {
			if !p.toolAllowed(tool, "", grant.Body, seenTools) {
				return nil, errors.New("unapproved tool")
			}
		}
	}
	if len(p.requiredTools) > 0 {
		choice := value["tool_choice"]
		if choice == "none" {
			return nil, errors.New("required external tools disabled")
		}
		if fields, ok := choice.(map[string]any); ok && fields["type"] == "none" {
			return nil, errors.New("required external tools disabled")
		}
	}
	for name := range p.requiredTools {
		if !seenTools[name] {
			return nil, errors.New("required external tool unavailable")
		}
	}
	return data, nil
}

// Decode with duplicate-key rejection. encoding/json normally accepts the last
// duplicate while an upstream can choose the first, invalidating validation.
func decodeObject(data []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := decodeUnique(d, 0)
	if err != nil {
		return nil, errors.New("invalid JSON")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("JSON object required")
	}
	return m, nil
}
func decodeUnique(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON depth")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	if delim == '{' {
		m := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, errors.New("JSON key")
			}
			if _, exists := m[key]; exists {
				return nil, errors.New("duplicate JSON key")
			}
			v, err := decodeUnique(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		_, err = d.Token()
		return m, err
	}
	if delim == '[' {
		a := []any{}
		for d.More() {
			v, err := decodeUnique(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err = d.Token()
		return a, err
	}
	return nil, errors.New("unexpected JSON delimiter")
}

func (p *Proxy) toolAllowed(value any, namespace string, policy BodyPolicy, seen map[string]bool) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	name, ok := m["name"].(string)
	if !ok || name == "" {
		return false
	}
	kind, _ := m["type"].(string)
	if policy == Responses && kind == "namespace" && namespace == "" {
		for k := range m {
			if k != "type" && k != "name" && k != "description" && k != "tools" {
				return false
			}
		}
		tools, ok := m["tools"].([]any)
		if !ok || len(tools) == 0 {
			return false
		}
		for _, tool := range tools {
			if !p.toolAllowed(tool, name, policy, seen) {
				return false
			}
		}
		return true
	}
	if policy == Responses && kind != "function" {
		return false
	}
	if policy == AnthropicMessages && kind != "" && kind != "custom" {
		return false
	}
	allowedKeys := map[string]bool{"name": true, "description": true, "type": true, "input_schema": true, "parameters": true, "strict": true, "cache_control": true, "defer_loading": true}
	for k := range m {
		if !allowedKeys[k] {
			return false
		}
	}
	if namespace != "" {
		name = namespace + "." + name
	}
	name = canonicalToolName(name)
	if !p.allowedTools[name] {
		return false
	}
	if deferred, _ := m["defer_loading"].(bool); !deferred {
		seen[name] = true
	}
	return true
}

// An allowed inference route must not become an account file/history retrieval
// API. Text and data URLs are data; server-stored references are capabilities.
func localContextOnly(value any) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if !localContextOnly(item) {
				return false
			}
		}
	case map[string]any:
		if kind, _ := v["type"].(string); kind == "item_reference" || kind == "mcp_call" || kind == "mcp_list_tools" {
			return false
		}
		for key, item := range v {
			switch key {
			case "file_id", "connector_id", "server_url", "conversation_id", "previous_response_id":
				return false
			case "image_url":
				image, ok := item.(string)
				if !ok || !strings.HasPrefix(image, "data:") {
					return false
				}
			case "url":
				return false
			}
			if !localContextOnly(item) {
				return false
			}
		}
	}
	return true
}

// Normalize native flat MCP names and Responses namespace function names to
// one logical identity. It does not turn arbitrary native tools into MCP grants.
func canonicalToolName(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	if strings.ContainsAny(name, "./") {
		return strings.ReplaceAll(name, "/", ".")
	}
	if i := strings.LastIndex(name, "__"); i > len("mcp__") && i+2 < len(name) {
		return name[:i] + "." + name[i+2:]
	}
	return name
}
