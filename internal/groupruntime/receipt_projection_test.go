package groupruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/groupsandbox"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestReceiptProjectionDropsLegacyErrorsPromptsAndUnknownText(t *testing.T) {
	scope := store.ScopeRef{JID: "tg:-2", Folder: "group-scope"}
	file := strings.Repeat("a", 32) + ".result.json"
	raw := []byte(`{"status":"error","requestFile":"/private/owner","help":"PRIVATE_HELP","error":"https://api.telegram.org/botPRIVATE_TOKEN/sendMessage","extra":"PRIVATE_EXTRA","op":"list","result":{"tasks":[{"taskId":"digest","chatFolder":"group-scope","chatJid":"tg:-2","status":"paused","scheduleType":"cron","nextRun":123,"prompt":"token=PRIVATE_PROMPT","scheduleValue":"PRIVATE_SCHEDULE","unknown":"PRIVATE_TASK_FIELD"}]}}`)
	out, err := projectReceipt(raw, "tasks", file, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE_", "/private/", "api.telegram.org", "scheduleValue", "chatFolder", "chatJid"} {
		if strings.Contains(out, private) {
			t.Fatalf("receipt disclosed %s: %s", private, out)
		}
	}
	if !strings.Contains(out, `"taskId":"digest"`) || !strings.Contains(out, `"error":"operation_unavailable"`) || !strings.Contains(out, `"status":"paused"`) {
		t.Fatal("safe management metadata lost", out)
	}
	message, err := projectReceipt([]byte(`{"status":"uncertain","error":"PRIVATE_ERROR","result":{"TaskID":"PRIVATE_RESULT"}}`), "messages", file, scope)
	if err != nil || !strings.Contains(message, `"status":"uncertain"`) || !strings.Contains(message, `"delivery":"unconfirmed"`) || strings.Contains(message, "PRIVATE_") {
		t.Fatal("delivery ambiguity lost or error leaked", message, err)
	}
	if _, err = projectReceipt([]byte(`{"status":"ok","result":{"task":{"taskId":"foreign","chatFolder":"owner"}}}`), "tasks", file, scope); err == nil {
		t.Fatal("foreign task metadata projected")
	}
}
func receiptRequestFixture(t *testing.T) (*Manager, store.ScopeRef, runner.NamespacePaths) {
	t.Helper()
	m, scope, paths, _ := reconciliationFixture(t)
	ready, err := m.Store.CompleteGroupEpoch(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Store.SetNamespaceDisabled(t.Context(), ready, false); err != nil {
		t.Fatal(err)
	}
	m.MCP = &groupsandbox.MCP{}
	m.Config.Qualification = "synthetic-ready"
	m.Config.EnabledGroups = []string{ready.JID}
	return m, ready, paths
}
func TestGroupRequestMessageRejectsInternalCapabilitiesBeforePublication(t *testing.T) {
	m, scope, paths := receiptRequestFixture(t)
	request := m.RequestHandler(nil)
	for _, text := range []string{"http://gopod-relay:8787/mcp/" + strings.Repeat("a", 64), "http://group:" + strings.Repeat("b", 64) + "@gopod-relay:8787"} {
		raw, _ := json.Marshal(map[string]any{"kind": "message", "payload": map[string]string{"chatJid": scope.JID, "text": text}})
		if _, err := request(t.Context(), scope, 0, raw); err == nil {
			t.Fatal("capability message published")
		}
	}
	entries, err := os.ReadDir(filepath.Join(paths.IPC, "messages"))
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected message created IPC file")
	}
	raw, _ := json.Marshal(map[string]any{"kind": "message", "payload": map[string]string{"chatJid": scope.JID, "text": "Public reference https://example.org/docs"}})
	if _, err = request(t.Context(), scope, 0, raw); err != nil {
		t.Fatal("ordinary public message denied", err)
	}
	entries, err = os.ReadDir(filepath.Join(paths.IPC, "messages"))
	if err != nil || len(entries) != 1 {
		t.Fatal("positive publication not queued")
	}
}
func TestGroupRequestReceiptProjectsProtectedLegacyFile(t *testing.T) {
	m, scope, paths := receiptRequestFixture(t)
	file := strings.Repeat("c", 32) + ".result.json"
	directory := filepath.Join(paths.IPC, "results", "messages")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, file), []byte(`{"status":"error","error":"PRIVATE_PATH_AND_AUTH_URL","help":"PRIVATE_HELP"}`), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"kind": "receipt", "payload": map[string]string{"channel": "messages", "file": file}})
	out, err := m.RequestHandler(nil)(t.Context(), scope, 0, raw)
	if err != nil || strings.Contains(out, "PRIVATE_") || !strings.Contains(out, "operation_unavailable") {
		t.Fatal("legacy error leaked", out, err)
	}
}

func TestMessageReceiptAcknowledgementRequiresConsistentSuccess(t *testing.T) {
	for _, tc := range []struct{ raw, delivery string }{
		{`{"status":"ok"}`, "acknowledged"},
		{`{"status":"ok","error":"legacy failure"}`, "unconfirmed"},
	} {
		out, err := projectReceipt([]byte(tc.raw), "messages", strings.Repeat("a", 32)+".result.json", store.ScopeRef{})
		if err != nil || !strings.Contains(out, `"delivery":"`+tc.delivery+`"`) {
			t.Fatalf("unexpected acknowledgement: %s %v", out, err)
		}
	}
}

func TestGroupReceiptRetainsSafePromptAndDerivesTime(t *testing.T) {
	scope := store.ScopeRef{JID: "tg:-2", Folder: "group-scope"}
	raw := []byte(`{"status":"ok","result":{"tasks":[{"taskId":"digest","chatFolder":"group-scope","chatJid":"tg:-2","prompt":"Daily group digest","nextRun":1000,"nextRunAt":"UNTRUSTED_TIME"},{"taskId":"sensitive","prompt":"http://gopod-relay:8787/mcp/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}`)
	out, err := projectReceipt(raw, "tasks", strings.Repeat("a", 32)+".result.json", scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Daily group digest", "1970-01-01T00:00:01Z", "[content unavailable]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	for _, secret := range []string{"UNTRUSTED_TIME", "gopod-relay", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/"} {
		if strings.Contains(out, secret) {
			t.Fatalf("disclosed %s", secret)
		}
	}
}
