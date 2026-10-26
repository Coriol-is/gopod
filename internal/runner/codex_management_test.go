package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func managementTranscript(account, limits string) string {
	return "{\"id\":1,\"result\":{}}\n{\"method\":\"notice\",\"params\":{\"secret\":\"NEVER-RETAIN\"}}\n{\"id\":2,\"result\":" + account + "}\n{\"id\":3,\"result\":" + limits + "}\n"
}
func TestCodexManagementReadOnlyMethodsAndNulls(t *testing.T) {
	var sent bytes.Buffer
	transcript := managementTranscript(`{"account":{"type":"chatgpt","email":"NEVER-RETAIN"}}`, `{"ordinaryUsageAllowed":false,"accountId":"NEVER-RETAIN","rateLimits":{"spendControlReached":null,"primary":{"usedPercent":100,"resetsAt":1,"windowDurationMins":300},"secondary":null}}`)
	r, err := codexManagementProtocol(t.Context(), &sent, strings.NewReader(transcript))
	if err != nil {
		t.Fatal(err)
	}
	if r.Auth != "authenticated_chatgpt" || r.Quota != "observed" || r.OrdinaryUsageAllowed == nil || *r.OrdinaryUsageAllowed || r.SpendControlReached != nil || r.Secondary != nil || r.Primary == nil || *r.Primary.UsedPercent != 100 {
		t.Fatalf("report=%+v", r)
	}
	// The elapsed reset does not convert an authoritative denial into recovery.
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "NEVER-RETAIN") {
		t.Fatal("account data retained")
	}
	lines := strings.Split(strings.TrimSpace(sent.String()), "\n")
	expected := []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}
	if len(lines) != len(expected) {
		t.Fatal(sent.String())
	}
	for i, line := range lines {
		var msg map[string]any
		if json.Unmarshal([]byte(line), &msg) != nil || msg["method"] != expected[i] {
			t.Fatal(line)
		}
	}
	if !strings.Contains(sent.String(), `"refreshToken":false`) {
		t.Fatal("proactive refresh requested")
	}
}
func TestCodexManagementAPIKeySkipsQuota(t *testing.T) {
	var sent bytes.Buffer
	r, err := codexManagementProtocol(t.Context(), &sent, strings.NewReader(managementTranscript(`{"account":{"type":"apiKey"}}`, `{}`)))
	if err != nil || r.Quota != "not_applicable" || r.Auth != "authenticated_api_key" {
		t.Fatalf("%+v %v", r, err)
	}
	if strings.Contains(sent.String(), "rateLimits/read") {
		t.Fatal("subscription quota called for API key")
	}
}
func TestCodexManagementSchemaErrorsAndMissing(t *testing.T) {
	for _, tt := range []struct{ name, account, limits, want string }{
		{"null account", `{"account":null}`, `{}`, "none"},
		{"missing account", `{}`, `{}`, "schema"},
		{"missing usage", `{"account":{"type":"chatgpt"}}`, `{"rateLimits":{}}`, "none"},
		{"missing snapshot", `{"account":{"type":"chatgpt"}}`, `{}`, "schema"},
		{"bad bool", `{"account":{"type":"chatgpt"}}`, `{"rateLimits":{},"ordinaryUsageAllowed":"NEVER-RETAIN"}`, "schema"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sent bytes.Buffer
			r, _ := codexManagementProtocol(t.Context(), &sent, strings.NewReader(managementTranscript(tt.account, tt.limits)))
			if r.ErrorClass != tt.want {
				t.Fatalf("%+v", r)
			}
			if r.OrdinaryUsageAllowed != nil || r.SpendControlReached != nil {
				t.Fatal("null inferred")
			}
		})
	}
	var sent bytes.Buffer
	r, err := codexManagementProtocol(t.Context(), &sent, strings.NewReader("{\"id\":1,\"error\":{\"message\":\"NEVER-RETAIN\"}}\n"))
	if err != nil || r.ErrorClass != "api_rejection" || strings.Contains(fmt.Sprint(r), "NEVER-RETAIN") {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestCodexManagementBoundsAndCanceled(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 256*1024+1), strings.Repeat("{\"method\":\"noise\"}\n", 65), "{\"id\":9,\"result\":{}}\n"} {
		var sent bytes.Buffer
		_, err := codexManagementProtocol(t.Context(), &sent, strings.NewReader(body))
		if !errors.Is(err, errManagementSchema) {
			t.Fatalf("err=%v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var sent bytes.Buffer
	_, err := codexManagementProtocol(ctx, &sent, strings.NewReader(""))
	if !errors.Is(err, context.Canceled) || sent.Len() != 0 {
		t.Fatal("cancel did not prevent writes")
	}
}
func TestManagementCheckSelectedProviderAndSlot(t *testing.T) {
	r := &Runner{profilePolicy: testPersonalPolicy(), provider: NewClaudeProvider("")}
	r.RestoreChatProvider("owner", NewCodexProvider(""))
	called := 0
	inspect := func(ctx context.Context) (string, error) {
		called++
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > 7*time.Second {
			t.Fatal("work deadline missing")
		}
		return "existing", nil
	}
	probe := func(ctx context.Context, id string) (ManagementReport, error) {
		called++
		if id != "existing" {
			t.Fatal(id)
		}
		return ManagementReport{Auth: "authenticated_chatgpt", Quota: "observed", ErrorClass: "none"}, nil
	}
	got := r.managementCheck(t.Context(), "owner", inspect, probe)
	if called != 2 || got.Provider != "codex" || got.Inference != "unverified" || got.CheckedAt.IsZero() {
		t.Fatalf("%+v called=%d", got, called)
	}
	called = 0
	got = r.managementCheck(t.Context(), "other", inspect, probe)
	if called != 0 || got.ErrorClass != "unsupported" {
		t.Fatalf("%+v", got)
	}
	release, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	got = r.managementCheck(ctx, "owner", inspect, probe)
	release()
	if got.ErrorClass != "timeout" || called != 0 {
		t.Fatalf("%+v", got)
	}
}
