package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
)

func TestProbeReportNeverSerializesNativeSecrets(t *testing.T) {
	secrets := []string{
		"sk-synthetic-secret-0123456789",
		"https://auth.example/oauth/authorize?code=synthetic-secret-code",
		"http://gopod-relay:8787/mcp/abcdefghijklmnopqrstuvwxyz012345",
		"/home/private-owner/native/auth.json",
		"RAW_NATIVE_OUTPUT_MUST_NOT_ESCAPE",
	}
	result := &runner.ExecResult{ExitCode: 19, Stdout: "GROUP_CANARY_OK " + strings.Join(secrets, " "), Stderr: "egress unavailable; config error; mcp failed; 403 forbidden; 401 unauthorized; TLS certificate; connection; 429 quota"}
	report := probeReport("codex", result)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("native secret serialized")
		}
	}
	// Assert the entire schema, not just a few forbidden substrings: no future
	// output/stderr/error field may silently extend this diagnostic contract.
	var got map[string]any
	if err = json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 12 {
		t.Fatalf("unexpected diagnostic schema size: %d", len(got))
	}
	if got["provider"] != "codex" || got["exit"] != float64(19) || got["inference_qualified"] != false {
		t.Fatal("invalid structural status")
	}
	for _, key := range []string{"relay_denied", "marker", "config_error", "mcp_error", "forbidden", "unauthorized", "tls_error", "connection_error", "rate_limit"} {
		if got[key] != true {
			t.Errorf("missing structural diagnostic %s", key)
		}
	}
}

func TestProbeReportOrdinaryOutputDoesNotClaimQualification(t *testing.T) {
	got := probeReport("claude", &runner.ExecResult{ExitCode: 0, Stdout: "ordinary response", Stderr: "ordinary notice"})
	for key, value := range got {
		switch key {
		case "provider":
			if value != "claude" {
				t.Fatal("provider lost")
			}
		case "exit":
			if value != 0 {
				t.Fatal("exit changed")
			}
		default:
			if value != false {
				t.Errorf("unexpected positive claim %s", key)
			}
		}
	}
}
