package control

import (
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

var pickerModes = []string{"owner", "mention", "owner+mention", "all"}

func TestTriggerShowOffersOneButtonPerModeWithCurrentMarked(t *testing.T) {
	st := testStore(t)
	registerGroup(t, st)
	if err := st.SetGroupTrigger(t.Context(), testGroupJID, store.TriggerOwnerMention, 1); err != nil {
		t.Fatal(err)
	}
	r := fullRouter(t, st)
	resp, err := r.Dispatch(t.Context(), Command{Name: "trigger", Caller: groupOwnerCaller()})
	if err != nil || !strings.Contains(resp.Text, "Trigger mode: owner+mention") {
		t.Fatalf("show: %+v %v", resp, err)
	}
	if len(resp.Buttons) != len(pickerModes) {
		t.Fatalf("rows = %d, want %d", len(resp.Buttons), len(pickerModes))
	}
	for i, mode := range pickerModes {
		row := resp.Buttons[i]
		if len(row) != 1 || row[0].Command != "trigger" || len(row[0].Args) != 1 || row[0].Args[0] != mode {
			t.Fatalf("row %d = %+v", i, row)
		}
		marked := strings.HasPrefix(row[0].Label, "✓ ")
		if marked != (mode == "owner+mention") || !strings.HasSuffix(row[0].Label, mode) {
			t.Errorf("row %d label %q", i, row[0].Label)
		}
		if marked != (row[0].Style == StylePrimary) {
			t.Errorf("row %d style %q", i, row[0].Style)
		}
	}
}

func TestTriggerShowFromPrivateChatCarriesTheGroupID(t *testing.T) {
	st := testStore(t)
	registerGroup(t, st)
	r := fullRouter(t, st)
	private := Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, IsOwner: true}
	resp, _ := r.Dispatch(t.Context(), Command{Name: "trigger", Args: []string{testGroupJID}, Caller: private})
	if len(resp.Buttons) != len(pickerModes) {
		t.Fatalf("rows = %d: %+v", len(resp.Buttons), resp)
	}
	for i, mode := range pickerModes {
		args := resp.Buttons[i][0].Args
		if len(args) != 2 || args[0] != "-1003850302017" || args[1] != mode {
			t.Fatalf("row %d args = %v", i, args)
		}
	}
	// A press re-dispatches the same command: it sets the mode for that group.
	btn := resp.Buttons[3][0]
	if set, _ := r.Dispatch(t.Context(), Command{Name: btn.Command, Args: btn.Args, Caller: private}); set.Code != 0 || set.Buttons != nil {
		t.Fatalf("press: %+v", set)
	}
	if mode, _ := st.GroupTrigger(t.Context(), testGroupJID); mode != store.TriggerAll {
		t.Fatalf("stored %q", mode)
	}
}

func TestTriggerPressByMemberIsDeniedWithoutChange(t *testing.T) {
	st := testStore(t)
	registerGroup(t, st)
	r := fullRouter(t, st)
	if _, err := r.Dispatch(t.Context(), Command{Name: "trigger", Args: []string{"all"}, Caller: groupMemberCaller()}); err == nil {
		t.Fatal("member press must be refused")
	}
	if mode, _ := st.GroupTrigger(t.Context(), testGroupJID); mode != store.TriggerMention {
		t.Fatalf("mode changed to %q", mode)
	}
}

func TestTriggerUsageHasNoButtons(t *testing.T) {
	st := testStore(t)
	registerGroup(t, st)
	r := fullRouter(t, st)
	private := Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, IsOwner: true}
	if resp, _ := r.Dispatch(t.Context(), Command{Name: "trigger", Caller: private}); resp.Buttons != nil {
		t.Fatalf("usage offered buttons: %+v", resp)
	}
}
