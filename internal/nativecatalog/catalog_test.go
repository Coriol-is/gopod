package nativecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSanitizePreservesMetadataAndCapsAuthority(t *testing.T) {
	raw := []byte(`{"models":[{"slug":"gpt-real","display_name":"Actual model","description":"kept","priority":7,"context_window":272000,"max_context_window":400000,"use_responses_lite":true,"supports_search_tool":true,"supported_reasoning_levels":[{"effort":"high","description":"kept"}],"model_messages":{"instructions_template":"actual template","persistent_instructions":"actual instruction"},"base_instructions":"legacy instructions","guardian":{"shell":"PRIVATE_GUARDIAN"},"apply_patch_tool_type":"freeform","tool_mode":"code_mode_only","experimental_supported_tools":["exec_command","network"],"multi_agent_version":"v2","shell_type":"unified_exec","node_repl_disabled":false,"auto_review_model_override":"private-reviewer"}],"etag":"PRIVATE_ETAG","fetched_at":"PRIVATE_TIME","client_version":"0.164.0"}`)
	original := bytes.Clone(raw)
	catalog, err := Sanitize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, original) {
		t.Fatal("source mutated")
	}
	if bytes.Contains(catalog.JSON, []byte("actual template")) || bytes.Contains(catalog.JSON, []byte("legacy instructions")) || bytes.Contains(catalog.JSON, []byte("actual instruction")) {
		t.Fatal("private instruction inherited")
	}
	if catalog.HasModel("unknown") || !catalog.HasModel("gpt-real") {
		t.Fatal("membership")
	}
	var root map[string]any
	if err = json.Unmarshal(catalog.JSON, &root); err != nil {
		t.Fatal(err)
	}
	if len(root) != 1 || bytes.Contains(catalog.JSON, []byte("PRIVATE_")) || bytes.Contains(catalog.JSON, []byte("private-reviewer")) || bytes.Contains(catalog.JSON, []byte("PRIVATE_GUARDIAN")) {
		t.Fatal("cache metadata retained")
	}
	model := root["models"].([]any)[0].(map[string]any)
	for key, want := range caps {
		got, _ := json.Marshal(model[key])
		expected, _ := json.Marshal(want)
		if !bytes.Equal(got, expected) {
			t.Errorf("uncapped %s", key)
		}
	}
	for _, key := range []string{"slug", "display_name", "context_window", "max_context_window", "priority", "use_responses_lite", "supports_search_tool", "supported_reasoning_levels"} {
		var originalRoot map[string]any
		_ = json.Unmarshal(raw, &originalRoot)
		want := originalRoot["models"].([]any)[0].(map[string]any)[key]
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(model[key])
		if !bytes.Equal(a, b) {
			t.Errorf("changed metadata %s", key)
		}
	}
	parsed, err := ParseSanitized(catalog.JSON)
	if err != nil || !bytes.Equal(parsed.JSON, catalog.JSON) {
		t.Fatal("sanitized not stable", err)
	}
	if _, err = ParseSanitized(raw); !errors.Is(err, ErrInvalid) {
		t.Fatal("raw accepted as sanitized")
	}
	for _, tc := range []struct {
		request, defaultModel string
		deny                  bool
	}{{"gpt-real", "", false}, {"", "gpt-real", false}, {"", "", true}, {"unknown", "gpt-real", true}, {" gpt-real", "gpt-real", true}} {
		_, err := catalog.Select(tc.request, tc.defaultModel)
		if (err != nil) != tc.deny {
			t.Fatal("selection", tc, err)
		}
	}
}
func TestCatalogRejectsAmbiguityAndUnboundedInput(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"models":[]}`, `{"models":null}`, `{"models":[null]}`,
		`{"models":[{"slug":"../private"}]}`, `{"models":[{"slug":""}]}`,
		`{"models":[{"slug":"a"},{"slug":"a"}]}`,
		`{"models":[{"slug":"a","slug":"b"}]}`,
		`{"models":[{"slug":"a","model_messages":{"x":1,"x":2}}]}`,
		`{"models":[],"models":[{"slug":"a"}]}`,

		`{"models":[{"slug":"a"}],"auth":{"token":"private"}}`,
		`{"models":[{"slug":"a"}]} {}`,
		`{"models":[{"slug":"` + strings.Repeat("a", 129) + `"}]}`,
		strings.Repeat(" ", MaxBytes+1),
		`{"models":[{"slug":"a","model_messages":` + strings.Repeat("[", 66) + `0` + strings.Repeat("]", 66) + `}]}`,
	} {
		if _, err := Sanitize([]byte(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid accepted (length %d)", len(raw))
		}
	}
	models := make([]any, MaxModels+1)
	for i := range models {
		models[i] = map[string]any{"slug": "a"}
	}
	raw, _ := json.Marshal(map[string]any{"models": models})
	if _, err := Sanitize(raw); err == nil {
		t.Fatal("model count unbounded")
	}
}
func TestParseSanitizedRejectsRecappedAuthority(t *testing.T) {
	base, err := Sanitize([]byte(`{"models":[{"slug":"safe"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]any{"apply_patch_tool_type": "freeform", "tool_mode": "code_mode", "experimental_supported_tools": []string{"native"}, "multi_agent_version": "v2", "shell_type": "shell_command", "node_repl_disabled": false, "guardian": map[string]any{"shell": "PRIVATE_GUARDIAN"}} {
		var v map[string]any
		_ = json.Unmarshal(base.JSON, &v)
		v["models"].([]any)[0].(map[string]any)[key] = bad
		raw, _ := json.Marshal(v)
		if _, err := ParseSanitized(raw); err == nil {
			t.Fatal("uncapped authority accepted", key)
		}
	}
}

func TestUnknownMetadataDroppedButSanitizedRejects(t *testing.T) {
	raw := []byte(`{"models":[{"slug":"safe","future_native_capability":{"PRIVATE_SECRET":true},"model_instructions":"PRIVATE_PERSONA","base_instructions":"PRIVATE_PERSONA","model_messages":{"instructions_template":"PRIVATE_PERSONA"}}]}`)
	catalog, err := Sanitize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(catalog.JSON, []byte("PRIVATE_")) || bytes.Contains(catalog.JSON, []byte("future_native_capability")) {
		t.Fatal("unknown/private metadata retained")
	}
	var v map[string]any
	_ = json.Unmarshal(catalog.JSON, &v)
	v["models"].([]any)[0].(map[string]any)["future_native_capability"] = true
	uncapped, _ := json.Marshal(v)
	if _, err := ParseSanitized(uncapped); err == nil {
		t.Fatal("unknown sanitized field accepted")
	}
}
