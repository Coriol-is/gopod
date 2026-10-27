package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestSnapshotConversationValidation(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	if f.conv.Scope.NamespaceID == "" || f.conv.ThreadID != 0 || f.conv.Visibility != VisibilityConversation {
		t.Fatalf("unexpected snapshot %+v", f.conv)
	}
	if err := f.st.ValidateConversation(ctx, f.conv); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SnapshotConversation(ctx, "tg:1", -1, VisibilityConversation); !errors.Is(err, ErrInvalidConversation) {
		t.Fatalf("negative thread: %v", err)
	}
	if _, err := f.st.SnapshotConversation(ctx, "tg:1", 0, "public"); !errors.Is(err, ErrInvalidConversation) {
		t.Fatalf("unknown visibility: %v", err)
	}
	if _, err := f.st.SnapshotConversation(ctx, "tg:404", 0, VisibilityPrivate); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("unregistered chat: %v", err)
	}
	if err := f.st.ValidateConversation(ctx, ConversationScope{Scope: f.conv.Scope, ThreadID: 0}); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("missing visibility accepted: %v", err)
	}
	if err := f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ValidateConversation(ctx, f.conv); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("stale revision accepted: %v", err)
	}
}

func TestMigrationFromPreChangeFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "production-shaped.sqlite")
	raw, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Shapes predate namespaces and CP-002: no namespace columns, no turns.thread_id.
	_, err = raw.Exec(`
CREATE TABLE registered_chats (jid TEXT PRIMARY KEY,name TEXT,folder TEXT NOT NULL UNIQUE,trigger_pattern TEXT,requires_trigger INTEGER NOT NULL DEFAULT 1,is_owner INTEGER NOT NULL DEFAULT 0,added_at INTEGER NOT NULL);
INSERT INTO registered_chats(jid,name,folder,is_owner,added_at) VALUES('tg:1','Me','owner',1,10),('tg:-2','Team','group-a',0,20);
CREATE TABLE conversation_history (id INTEGER PRIMARY KEY AUTOINCREMENT,chat_folder TEXT NOT NULL,thread_id INTEGER NOT NULL,role TEXT NOT NULL CHECK(role IN ('user','assistant')),content TEXT NOT NULL);
INSERT INTO conversation_history(chat_folder,thread_id,role,content) VALUES('owner',0,'user','hello'),('owner',0,'assistant','hi');
CREATE TABLE turns (id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL,source_id TEXT NOT NULL,chat_folder TEXT NOT NULL,chat_id INTEGER NOT NULL,tg_message_id INTEGER NOT NULL DEFAULT 0,is_owner INTEGER NOT NULL DEFAULT 0,is_voice INTEGER NOT NULL DEFAULT 0,text TEXT NOT NULL,file_path TEXT,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,placeholder_msg_id INTEGER NOT NULL DEFAULT 0,reply_msg_id INTEGER NOT NULL DEFAULT 0,error TEXT,created_at INTEGER NOT NULL,started_at INTEGER,finished_at INTEGER,UNIQUE(source,source_id));
INSERT INTO turns(source,source_id,chat_folder,chat_id,text,status,attempts,reply_msg_id,created_at) VALUES('telegram','1:5','owner',1,'hello','done',1,77,100);`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()

	st, err := Open(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	owner, err := st.SnapshotConversation(t.Context(), "tg:1", 0, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, st, `SELECT count(*) FROM registered_chats`); got != 2 {
		t.Fatalf("registrations changed: %d", got)
	}
	if got := countRows(t, st, `SELECT count(*) FROM conversation_history`); got != 2 {
		t.Fatalf("history changed: %d", got)
	}
	turn, err := st.GetTurn(t.Context(), 1)
	if err != nil || turn.ReplyMsgID != 77 || turn.ThreadID != 0 {
		t.Fatalf("turn changed: %+v %v", turn, err)
	}
	// New tables are usable on the migrated database.
	res, err := st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: owner, Kind: ExecInteractive, Provider: "claude", Members: []ResultMember{{TurnID: 1, Attempt: 1}}}, "answer")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Restart keeps the identities and the frozen result.
	st, err = Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.ValidateConversation(t.Context(), owner); err != nil {
		t.Fatalf("scope identity changed across restart: %v", err)
	}
	again, err := st.GetTurnResult(t.Context(), res.ID)
	if err != nil || again.Text != "answer" || again.Conversation != owner {
		t.Fatalf("result lost across restart: %+v %v", again, err)
	}
}

func TestRestartKeepsDeliveryIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.sqlite")
	st, err := Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	conv, _ := st.SnapshotConversation(t.Context(), "tg:1", 0, VisibilityConversation)
	req := PublishRequest{Conversation: conv, Owner: OwnerLegacyAgentIPC, Parts: textParts("one"), Request: &RequestIdentity{Key: "a.json", PayloadHash: sha256Hex([]byte("a"))}}
	first, err := st.PublishDelivery(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	replay, err := st.PublishDelivery(t.Context(), req)
	if err != nil || replay.ID != first.ID || replay.Created {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
	req.Request = &RequestIdentity{Key: "b.json", PayloadHash: sha256Hex([]byte("b"))}
	second, err := st.PublishDelivery(t.Context(), req)
	if err != nil || second.Sequence != first.Sequence+1 {
		t.Fatalf("sequence did not continue after restart: %+v %v", second, err)
	}
}
