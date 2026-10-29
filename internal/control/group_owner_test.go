package control

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

const (
	testGroupChat int64 = -1003850302017
	testGroupJID        = "tg:-1003850302017"
)

func groupOwnerCaller() Caller {
	return Caller{Source: SourceTelegram, ChatKind: "supergroup", ChatID: testGroupChat, UserID: 100, IsGroupOwner: true}
}

func groupMemberCaller() Caller {
	return Caller{Source: SourceTelegram, ChatKind: "supergroup", ChatID: testGroupChat, UserID: 555}
}

// fullRouter registers every command family that has an owner or chat scope.
func fullRouter(t *testing.T, st *store.Store) *Router {
	t.Helper()
	r := New(silentLog())
	RegisterBuiltins(r)
	RegisterWhoami(r, 100)
	RegisterChatCommands(r, st, 100)
	RegisterLogsCommand(r, func(string, string, int) (string, error) { return "logs", nil })
	RegisterRestartCommand(r, func(context.Context, Caller) error { return nil })
	RegisterTriggerCommand(r, st)
	ok := func(context.Context, string) (string, error) { return "ok", nil }
	RegisterIsolationCommands(r, IsolationActions{Status: ok, Prepare: ok, Enable: ok, Disable: ok})
	RegisterTelegramCommands(r, func() TelegramSnapshot { return TelegramSnapshot{} },
		func(context.Context) TelegramSnapshot { return TelegramSnapshot{} },
		func(context.Context, int64) bool { return false })
	RegisterBackup(r, BackupDeps{})
	lookup := func(int64) string { return "grp" }
	RegisterProviderCommand(r, func(context.Context, string, string) (string, error) { return "switched", nil },
		func(string) string { return "claude" }, []string{"claude", "codex"}, lookup)
	RegisterModelCommand(r, func(string) string { return "" }, func(string, string) (string, error) { return "set", nil }, nil, lookup)
	return r
}

func TestOwnerOnlyCommandsStayDeniedToTheOwnerInGroups(t *testing.T) {
	r := fullRouter(t, testStore(t))
	denied := 0
	for _, info := range r.List() {
		if info.Perm != PermOwnerOnly {
			continue
		}
		denied++
		for _, caller := range []Caller{groupOwnerCaller(), groupMemberCaller()} {
			if _, err := r.Check(Command{Name: info.Name, Caller: caller}); !errors.Is(err, ErrNotAuthorized) {
				t.Errorf("%s allowed from a group (%+v): %v", info.Name, caller, err)
			}
		}
	}
	if denied < 6 {
		t.Fatalf("matrix covered only %d owner-only commands", denied)
	}
}

func TestGroupOwnerPermissionIsLimitedToTheListedCommands(t *testing.T) {
	r := fullRouter(t, testStore(t))
	var got []string
	for _, info := range r.List() {
		if info.Perm == PermGroupOwner {
			got = append(got, info.Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "isolation,trigger" {
		t.Fatalf("group-owner commands = %v", got)
	}
	for _, name := range got {
		if _, err := r.Check(Command{Name: name, Caller: groupOwnerCaller()}); err != nil {
			t.Errorf("%s denied to the group owner: %v", name, err)
		}
		if _, err := r.Check(Command{Name: name, Caller: groupMemberCaller()}); !errors.Is(err, ErrNotAuthorized) {
			t.Errorf("%s allowed to a group member: %v", name, err)
		}
	}
	// A positive chat id can never carry the group flag, even when set.
	spoof := Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 555, IsGroupOwner: true}
	if _, err := r.Check(Command{Name: "trigger", Caller: spoof}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("group flag honoured outside a group: %v", err)
	}
}

func registerGroup(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.RegisterChat(context.Background(), store.RegisteredChat{JID: testGroupJID, Folder: "grp", AddedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerShowSetFromGroupWithoutConfirmation(t *testing.T) {
	st := testStore(t)
	registerGroup(t, st)
	r := fullRouter(t, st)
	show, err := r.Dispatch(t.Context(), Command{Name: "trigger", Caller: groupOwnerCaller()})
	if err != nil || show.Code != 0 || !strings.Contains(show.Text, "Trigger mode: mention") || !strings.Contains(show.Text, "privacy mode") {
		t.Fatalf("show: %+v %v", show, err)
	}
	set, err := r.Dispatch(t.Context(), Command{Name: "trigger", Args: []string{"OWNER+mention"}, Caller: groupOwnerCaller()})
	if err != nil || set.Code != 0 || set.Buttons != nil {
		t.Fatalf("set: %+v %v", set, err)
	}
	if mode, _ := st.GroupTrigger(t.Context(), testGroupJID); mode != store.TriggerOwnerMention {
		t.Fatalf("stored %q", mode)
	}
	bad, _ := r.Dispatch(t.Context(), Command{Name: "trigger", Args: []string{"everyone"}, Caller: groupOwnerCaller()})
	if bad.Code == 0 {
		t.Fatal("bad mode accepted")
	}
}

func TestTriggerFromGroupIgnoresForeignGroupArgument(t *testing.T) {
	st := testStore(t)
	registerGroup(t, st)
	if err := st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:-200", Folder: "other", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	r := fullRouter(t, st)
	resp, _ := r.Dispatch(t.Context(), Command{Name: "trigger", Args: []string{"tg:-200", "all"}, Caller: groupOwnerCaller()})
	if resp.Code == 0 {
		t.Fatal("group owner set another group")
	}
	if mode, _ := st.GroupTrigger(t.Context(), "tg:-200"); mode != store.TriggerMention {
		t.Fatalf("other group changed to %q", mode)
	}
}

func TestTriggerFromOwnerPrivateChatNamesTheGroup(t *testing.T) {
	st := testStore(t)
	registerGroup(t, st)
	r := fullRouter(t, st)
	private := Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, IsOwner: true}
	if resp, _ := r.Dispatch(t.Context(), Command{Name: "trigger", Args: []string{testGroupJID, "all"}, Caller: private}); resp.Code != 0 {
		t.Fatalf("set: %+v", resp)
	}
	if mode, _ := st.GroupTrigger(t.Context(), testGroupJID); mode != store.TriggerAll {
		t.Fatalf("stored %q", mode)
	}
	if resp, _ := r.Dispatch(t.Context(), Command{Name: "trigger", Caller: private}); resp.Code == 0 {
		t.Fatal("private /trigger without a group id must explain usage")
	}
	if resp, _ := r.Dispatch(t.Context(), Command{Name: "trigger", Args: []string{"tg:-999", "all"}, Caller: private}); resp.Code == 0 {
		t.Fatal("unregistered group accepted")
	}
}

type isolationRecorder struct{ calls []string }

func (i *isolationRecorder) actions() IsolationActions {
	rec := func(op string) func(context.Context, string) (string, error) {
		return func(_ context.Context, target string) (string, error) {
			i.calls = append(i.calls, op+" "+target)
			return op + " ok", nil
		}
	}
	return IsolationActions{Status: rec("status"), Prepare: rec("prepare"), Enable: rec("enable"), Disable: rec("disable")}
}

func TestIsolationFromGroupOwnerStatusAndDisableAreImmediate(t *testing.T) {
	rec := &isolationRecorder{}
	r := New(silentLog())
	RegisterIsolationCommands(r, rec.actions())
	for _, op := range []string{"status", "disable"} {
		resp, err := r.Dispatch(t.Context(), Command{Name: "isolation", Args: []string{op}, Caller: groupOwnerCaller()})
		if err != nil || resp.Code != 0 || resp.Private {
			t.Fatalf("%s: %+v %v", op, resp, err)
		}
	}
	if strings.Join(rec.calls, ",") != "status "+testGroupJID+",disable "+testGroupJID {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestIsolationEnablePrepareFromGroupAskPrivateConfirmation(t *testing.T) {
	for _, op := range []string{"enable", "prepare"} {
		for _, confirmed := range []bool{false, true} {
			rec := &isolationRecorder{}
			r := New(silentLog())
			RegisterIsolationCommands(r, rec.actions())
			resp, err := r.Dispatch(t.Context(), Command{Name: "isolation", Args: []string{op}, Confirmed: confirmed, Caller: groupOwnerCaller()})
			if err != nil || resp.Code != CodeConfirm || !resp.Private || resp.Ack == "" || len(rec.calls) != 0 {
				t.Fatalf("%s confirmed=%v: %+v %v calls=%v", op, confirmed, resp, err, rec.calls)
			}
			yes := resp.Buttons[0][0]
			if !yes.Confirmed || yes.Command != "isolation" || strings.Join(yes.Args, " ") != op+" "+testGroupJID {
				t.Fatalf("yes button %+v", yes)
			}
			// Pressed in the owner's private chat the button runs the existing path.
			private := Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, IsOwner: true}
			done, err := r.Dispatch(t.Context(), Command{Name: "isolation", Args: yes.Args, Confirmed: true, Caller: private})
			if err != nil || done.Code != 0 || len(rec.calls) != 1 || rec.calls[0] != op+" "+testGroupJID {
				t.Fatalf("private yes: %+v %v calls=%v", done, err, rec.calls)
			}
		}
	}
}

func TestIsolationFromGroupCannotTargetAnotherGroupOrBeUsedByMembers(t *testing.T) {
	rec := &isolationRecorder{}
	r := New(silentLog())
	RegisterIsolationCommands(r, rec.actions())
	other := Command{Name: "isolation", Args: []string{"disable", "-200"}, Caller: groupOwnerCaller()}
	if resp, _ := r.Dispatch(t.Context(), other); resp.Code == 0 || len(rec.calls) != 0 {
		t.Fatalf("foreign target: %+v %v", resp, rec.calls)
	}
	member := Command{Name: "isolation", Args: []string{"disable"}, Caller: groupMemberCaller()}
	if _, err := r.Dispatch(t.Context(), member); !errors.Is(err, ErrNotAuthorized) || len(rec.calls) != 0 {
		t.Fatalf("member: %v %v", err, rec.calls)
	}
}

func TestProviderAndModelInGroupAreOwnerOnlyAndScopedToThatGroup(t *testing.T) {
	var switched []string
	var models []string
	r := New(silentLog())
	lookup := func(id int64) string {
		if id == testGroupChat {
			return "grp"
		}
		return "elsewhere"
	}
	RegisterProviderCommand(r, func(_ context.Context, folder, name string) (string, error) {
		switched = append(switched, folder+"="+name)
		return "switched", nil
	}, func(string) string { return "claude" }, []string{"claude", "codex"}, lookup)
	RegisterModelCommand(r, func(string) string { return "" }, func(folder, model string) (string, error) {
		models = append(models, folder+"="+model)
		return "set", nil
	}, nil, lookup)

	resp, err := r.Dispatch(t.Context(), Command{Name: "provider", Args: []string{"codex"}, Caller: groupOwnerCaller()})
	if err != nil || resp.Code != 0 || len(switched) != 1 || switched[0] != "grp=codex" {
		t.Fatalf("provider: %+v %v %v", resp, err, switched)
	}
	if resp, err = r.Dispatch(t.Context(), Command{Name: "model", Args: []string{"opus"}, Caller: groupOwnerCaller()}); err != nil || resp.Code != 0 || len(models) != 1 || models[0] != "grp=opus" {
		t.Fatalf("model: %+v %v %v", resp, err, models)
	}
	for _, name := range []string{"provider", "model"} {
		_, err := r.Dispatch(t.Context(), Command{Name: name, Args: []string{"codex"}, Confirmed: true, Caller: groupMemberCaller()})
		if !errors.Is(err, ErrNotAuthorized) {
			t.Errorf("%s by a member: %v", name, err)
		}
	}
	if len(switched) != 1 || len(models) != 1 {
		t.Fatal("a member changed the group")
	}
	// The private owner chat still confirms a provider switch.
	private := Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, IsOwner: true}
	if resp, _ = r.Dispatch(t.Context(), Command{Name: "provider", Args: []string{"codex"}, Caller: private}); resp.Code != CodeConfirm {
		t.Fatalf("private owner lost confirmation: %+v", resp)
	}
}

func TestHelpShowsGroupCommandsOnlyToTheGroupOwner(t *testing.T) {
	r := fullRouter(t, testStore(t))
	owner, _ := r.Dispatch(t.Context(), Command{Name: "help", Caller: groupOwnerCaller()})
	member, _ := r.Dispatch(t.Context(), Command{Name: "help", Caller: groupMemberCaller()})
	if !strings.Contains(owner.Text, "/trigger") || !strings.Contains(owner.Text, "/isolation") || strings.Contains(owner.Text, "/logs") || strings.Contains(owner.Text, "/restart") {
		t.Fatalf("owner help: %s", owner.Text)
	}
	if strings.Contains(member.Text, "/trigger") || strings.Contains(member.Text, "/isolation") {
		t.Fatalf("member help: %s", member.Text)
	}
}
