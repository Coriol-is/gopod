package isolationproxy

import (
	"encoding/json"
	"errors"
)

// This is local native registry discovery, never a hosted search capability.
func validClientSearch(d map[string]any) bool {
	if _, ok := d["description"].(string); !ok {
		return false
	}
	if !onlyKeys(d, "type", "execution", "description", "parameters") || d["execution"] != "client" || !boundedDescription(d) {
		return false
	}
	schema, ok := d["parameters"].(map[string]any)
	if !ok || !onlyKeys(schema, "type", "properties", "required", "additionalProperties") || schema["type"] != "object" || schema["additionalProperties"] != false {
		return false
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "query" {
		return false
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) != 2 {
		return false
	}
	for name, kind := range map[string]string{"limit": "number", "query": "string"} {
		prop, ok := props[name].(map[string]any)
		if !ok || !onlyKeys(prop, "type", "description") || prop["type"] != kind || !boundedDescription(prop) {
			return false
		}
	}
	return true
}
func boundedDescription(m map[string]any) bool {
	if value, exists := m["description"]; exists {
		v, ok := value.(string)
		return ok && len(v) <= 8192
	}
	return true
}
func onlyKeys(m map[string]any, keys ...string) bool {
	for k := range m {
		found := false
		for _, allowed := range keys {
			if k == allowed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (p *Proxy) auditSearchOutputs(input any, enabled bool) error {
	items, ok := input.([]any)
	if !ok {
		return nil
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if ok && m["type"] == "tool_search_call" {
			if !enabled || !validClientSearchCall(m) {
				return errors.New("invalid search call")
			}
			continue
		}
		if !ok || m["type"] != "tool_search_output" {
			if containsSearchOutput(item) {
				return errors.New("nested search output")
			}
			continue
		}
		if !enabled || !onlyKeys(m, "type", "id", "execution", "status", "call_id", "tools") || m["execution"] != "client" || m["status"] != "completed" {
			return errors.New("invalid search output")
		}
		call, ok := m["call_id"].(string)
		if !ok || call == "" || len(call) > 256 {
			return errors.New("invalid search call")
		}
		if id, exists := m["id"]; exists {
			v, ok := id.(string)
			if !ok || len(v) > 256 {
				return errors.New("invalid search id")
			}
		}
		tools, ok := m["tools"].([]any)
		if !ok || len(tools) > 2 {
			return errors.New("invalid search results")
		}
		seen := map[string]bool{}
		for _, tool := range tools {
			ns, ok := tool.(map[string]any)
			if !ok || ns["type"] != "namespace" || ns["name"] != "mcp__group" || !p.toolAllowed(tool, "", Responses, seen) {
				return errors.New("unapproved discovered tool")
			}
			// Scope every declaration, including deferred ones omitted from seen.
			children, _ := ns["tools"].([]any)
			for _, child := range children {
				definition, ok := child.(map[string]any)
				if !ok || definition["name"] != "group_exec" && definition["name"] != "group_request" {
					return errors.New("unapproved discovered scope")
				}
			}
		}
		for name := range seen {
			if !p.requiredTools[name] {
				return errors.New("unapproved discovered scope")
			}
		}
	}
	return nil
}
func containsSearchOutput(value any) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if containsSearchOutput(item) {
				return true
			}
		}
	case map[string]any:
		if v["type"] == "tool_search_output" || v["type"] == "tool_search_call" {
			return true
		}
		for _, item := range v {
			if containsSearchOutput(item) {
				return true
			}
		}
	}
	return false
}

func validClientSearchCall(m map[string]any) bool {
	if !onlyKeys(m, "type", "id", "call_id", "status", "execution", "arguments", "encrypted_content") || m["execution"] != "client" {
		return false
	}
	call, ok := m["call_id"].(string)
	if !ok || call == "" || len(call) > 256 {
		return false
	}
	if encrypted, present := m["encrypted_content"]; present && encrypted != nil {
		return false
	}
	for _, key := range []string{"id"} {
		if value, present := m[key]; present && value != nil {
			v, ok := value.(string)
			if !ok || len(v) > 256 {
				return false
			}
		}
	}
	if status, present := m["status"]; present && status != nil && status != "completed" && status != "in_progress" {
		return false
	}
	args, ok := m["arguments"].(map[string]any)
	if !ok || !onlyKeys(args, "query", "limit") {
		return false
	}
	query, ok := args["query"].(string)
	if !ok || len(query) > 2048 {
		return false
	}
	if value, present := args["limit"]; present && value != nil {
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		limit, err := number.Int64()
		if err != nil || limit < 1 || limit > 100 {
			return false
		}
	}
	return true
}
