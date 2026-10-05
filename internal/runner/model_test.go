package runner

import (
	"reflect"
	"testing"
)

func TestModelArgsPerProvider(t *testing.T) {
	if got := NewClaudeProvider("").ModelArgs("sonnet"); !reflect.DeepEqual(got, []string{"--model", "sonnet"}) {
		t.Errorf("claude ModelArgs = %v", got)
	}
	if got := NewCodexProvider("").ModelArgs("gpt-5"); !reflect.DeepEqual(got, []string{"-m", "gpt-5"}) {
		t.Errorf("codex ModelArgs = %v", got)
	}
	if got := NewClaudeProvider("").ModelArgs(""); got != nil {
		t.Errorf("empty model must yield no args, got %v", got)
	}
}

func TestWithModelInsertsBeforePrompt(t *testing.T) {
	claude := NewClaudeProvider("")
	cmd := withModel(claude.RunCmd("hi", ""), claude, "opus")
	if cmd[len(cmd)-1] != "hi" || cmd[len(cmd)-2] != "-p" {
		t.Errorf("prompt no longer last: %v", cmd)
	}
	if !containsSeq(cmd, "--model", "opus") {
		t.Errorf("model flag missing: %v", cmd)
	}
	codex := NewCodexProvider("")
	cmd = withModel(codex.RunCmd("hi", ""), codex, "gpt-5")
	if cmd[len(cmd)-1] != "hi" || !containsSeq(cmd, "-m", "gpt-5") {
		t.Errorf("codex cmd = %v", cmd)
	}
	if got := withModel(codex.RunCmd("hi", ""), codex, ""); !reflect.DeepEqual(got, codex.RunCmd("hi", "")) {
		t.Errorf("empty model must leave cmd untouched: %v", got)
	}
}

func TestChatModelState(t *testing.T) {
	r := &Runner{}
	if got := r.ModelForChat("owner"); got != "" {
		t.Errorf("default model = %q, want empty", got)
	}
	r.SetChatModel("owner", "sonnet")
	if got := r.ModelForChat("owner"); got != "sonnet" {
		t.Errorf("ModelForChat = %q", got)
	}
	r.SetChatModel("owner", "")
	if got := r.ModelForChat("owner"); got != "" {
		t.Errorf("cleared model = %q", got)
	}
}

func containsSeq(ss []string, a, b string) bool {
	for i := 0; i+1 < len(ss); i++ {
		if ss[i] == a && ss[i+1] == b {
			return true
		}
	}
	return false
}
