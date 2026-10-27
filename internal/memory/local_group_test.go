package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type forbiddenGroupEmbedder struct{ calls int }

func (e *forbiddenGroupEmbedder) Model() string {
	panic("local group memory accessed personal embedding model")
}
func (e *forbiddenGroupEmbedder) Dim() int {
	panic("local group memory accessed personal vector dimension")
}
func (e *forbiddenGroupEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	e.calls++
	return nil, errors.New("personal service must not be called")
}
func localMemoryFixture(t *testing.T) (*Memory, *forbiddenGroupEmbedder) {
	t.Helper()
	m := newThreadTestMemory(t)
	e := &forbiddenGroupEmbedder{}
	m.embedder = e
	return m, e
}
func localAdd(t *testing.T, m *Memory, folder string, thread int, kind, content string) int64 {
	t.Helper()
	id, err := m.AddLocalInThread(context.Background(), folder, thread, kind, "", content, "group-local")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGroupLocalMemoryNeverEmbedsAndKeepsNamespaceThreadVisibility(t *testing.T) {
	ctx := context.Background()
	m, embed := localMemoryFixture(t)
	localAdd(t, m, "owner", 0, "fact", "zebra OWNER_PRIVATE")
	localAdd(t, m, "groupB", 0, "fact", "zebra GROUP_B")
	main := localAdd(t, m, "groupA", 0, "fact", "zebra GROUP_MAIN")
	own := localAdd(t, m, "groupA", 7, "fact", "zebra GROUP_THREAD7")
	localAdd(t, m, "groupA", 9, "fact", "zebra GROUP_THREAD9")
	summary := localAdd(t, m, "groupA", 7, "conversation_summary", "zebra SUMMARY7")
	localAdd(t, m, "groupA", 0, "conversation_summary", "zebra SUMMARY_MAIN")
	items, err := m.SearchLocalInThread(ctx, "groupA", 7, "zebra", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := map[int64]bool{}
	for _, item := range items {
		found[item.ID] = true
		if item.ChatFolder != "groupA" || strings.Contains(item.Content, "PRIVATE") || strings.Contains(item.Content, "GROUP_B") || strings.Contains(item.Content, "THREAD9") || strings.Contains(item.Content, "SUMMARY_MAIN") {
			t.Errorf("foreign context: %#v", item)
		}
	}
	for _, id := range []int64{main, own, summary} {
		if !found[id] {
			t.Errorf("missing permitted memory %d", id)
		}
	}
	if _, err = m.store.DB().ExecContext(ctx, `UPDATE memories SET pinned=1 WHERE id=?`, own); err != nil {
		t.Fatal(err)
	}
	context := m.CompileLocalContext(ctx, "groupA", 7, "zebra", DefaultBudget)
	for _, want := range []string{"GROUP_THREAD7", "SUMMARY7"} {
		if !strings.Contains(context, want) {
			t.Errorf("missing local compiler item %s", want)
		}
	}
	for _, forbidden := range []string{"OWNER_PRIVATE", "GROUP_B", "GROUP_THREAD9", "SUMMARY_MAIN", "personalize your response"} {
		if strings.Contains(context, forbidden) {
			t.Errorf("foreign/persona compiler content: %s", forbidden)
		}
	}
	var vectors int
	if err = m.store.DB().QueryRowContext(ctx, `SELECT count(*) FROM memory_vec`).Scan(&vectors); err != nil {
		t.Fatal(err)
	}
	if embed.calls != 0 || vectors != 0 {
		t.Fatalf("personal embedding invoked or vector persisted: calls=%d vectors=%d", embed.calls, vectors)
	}
}
func TestGroupLocalSummaryReplacementRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	m, e := localMemoryFixture(t)
	old := localAdd(t, m, "groupA", 7, "conversation_summary", "oldsummary zebra")
	_, err := m.store.DB().ExecContext(ctx, `CREATE TRIGGER reject_local_summary BEFORE UPDATE ON memories WHEN old.kind='conversation_summary' BEGIN SELECT RAISE(ABORT,'synthetic rejection'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddLocalInThread(ctx, "groupA", 7, "conversation_summary", "", "replacement zebra", "compact"); err == nil {
		t.Fatal("replacement failure ignored")
	}
	var id, count int64
	if err = m.store.DB().QueryRowContext(ctx, `SELECT count(*),max(id) FROM memories WHERE chat_folder='groupA' AND IFNULL(status,'active')='active'`).Scan(&count, &id); err != nil {
		t.Fatal(err)
	}
	if count != 1 || id != old {
		t.Fatalf("old context lost or failed insert retained: count=%d id=%d", count, id)
	}
	items, err := m.SearchLocalInThread(ctx, "groupA", 7, "replacement", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatal("failed summary FTS insert retained")
	}
	if e.calls != 0 {
		t.Fatal("summary invoked embedder")
	}
}
func TestGroupLocalSummarySuccessSupersedesOnlyOwnConversation(t *testing.T) {
	ctx := context.Background()
	m, _ := localMemoryFixture(t)
	old := localAdd(t, m, "groupA", 7, "conversation_summary", "old zebra")
	other := localAdd(t, m, "groupA", 9, "conversation_summary", "sibling zebra")
	next := localAdd(t, m, "groupA", 7, "conversation_summary", "new zebra")
	var status string
	var by int64
	if err := m.store.DB().QueryRowContext(ctx, `SELECT status,superseded_by FROM memories WHERE id=?`, old).Scan(&status, &by); err != nil {
		t.Fatal(err)
	}
	if status != "superseded" || by != next {
		t.Fatal("replacement pointer not atomic")
	}
	if err := m.store.DB().QueryRowContext(ctx, `SELECT IFNULL(status,'active') FROM memories WHERE id=?`, other).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatal("sibling summary superseded")
	}
}
func TestGroupLocalMemoryRejectsGlobalScopeAndKnownSecrets(t *testing.T) {
	ctx := context.Background()
	m, e := localMemoryFixture(t)
	for _, folder := range []string{"", "_global", "../owner", "/owner"} {
		if _, err := m.AddLocalInThread(ctx, folder, 0, "fact", "", "safe text", ""); err == nil {
			t.Errorf("accepted unsafe scope %q", folder)
		}
		if _, err := m.SearchLocalInThread(ctx, folder, 0, "zebra", 5); err == nil {
			t.Errorf("searched unsafe scope %q", folder)
		}
	}
	if _, err := m.SearchLocalInThread(ctx, "groupA", -1, "zebra", 5); err == nil {
		t.Fatal("owner-wide thread sentinel allowed")
	}
	for _, secret := range []string{
		"http://gopod-relay:8787/mcp/" + strings.Repeat("a", 64), "http://group:" + strings.Repeat("b", 64) + "@gopod-relay:8787", "http://host.docker.internal:9877/mcp/" + strings.Repeat("c", 64), "password=sample-secret", "OPENAI_API_KEY=custom-shaped-secret", "Authorization: Bearer synthetic-secret-1234", "sk-ant-abcdefghijklmnopqrstuvwxyz0123456789", "device code: ABCD-EFGH", "https://auth.openai.com/codex/device", "https://example.invalid/callback?code=secret-value", "12345678:abcdefghijklmnopqrstuvwxyz0123456789", "-----BEGIN PRIVATE KEY-----", "https://user:password@example.invalid/path",
	} {
		for _, field := range []string{"content", "title", "source"} {
			title, content, source := "", "ordinary group fact", "group"
			switch field {
			case "content":
				content = secret
			case "title":
				title = secret
			case "source":
				source = secret
			}
			if _, err := m.AddLocalInThread(ctx, "groupA", 0, "fact", title, content, source); !errors.Is(err, ErrGroupMemorySensitive) {
				t.Errorf("secret accepted in %s", field)
			}
		}
	}
	if e.calls != 0 {
		t.Fatal("validation invoked embedder")
	}
}
func TestGroupLocalCompilerBoundsAndFiltersLegacySecrets(t *testing.T) {
	ctx := context.Background()
	m, _ := localMemoryFixture(t)
	for i := 0; i < 20; i++ {
		localAdd(t, m, "groupA", 0, "fact", "zebra "+strings.Repeat("контекст ", 400))
	}
	_, err := m.store.DB().ExecContext(ctx, `INSERT INTO memories(chat_folder,thread_id,kind,content,embed_model,created_at,updated_at,pinned) VALUES ('groupA',0,'fact','zebra password=OLD_SECRET','legacy',0,0,1)`)
	if err != nil {
		t.Fatal(err)
	}
	got := m.CompileLocalContext(ctx, "groupA", 0, "zebra", ContextBudget{Pinned: 10000, Facts: 10000, Fallback: 10000})
	if len(got) == 0 || len(got) > 8192 || strings.Contains(got, "OLD_SECRET") {
		t.Fatalf("unbounded or sensitive group context: bytes=%d", len(got))
	}
}

func TestGroupLocalMemoryAllowsOrdinaryPublicURLs(t *testing.T) {
	m, e := localMemoryFixture(t)
	if _, err := m.AddLocalInThread(context.Background(), "groupA", 0, "fact", "reference", "Public docs: https://example.org/docs/ordinary", "group"); err != nil {
		t.Fatal(err)
	}
	if e.calls != 0 {
		t.Fatal("public URL retention used personal embedder")
	}
}
