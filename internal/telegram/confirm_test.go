package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
)

// registerModel wires /model with a fake current model and records sets.
func registerModel(r *control.Router, current *string) {
	control.RegisterModelCommand(r,
		func(string) string { return *current },
		func(_, model string) (string, error) {
			*current = model
			return "Model set to " + model + ".", nil
		},
		func(string) []string { return []string{"opus", "sonnet"} },
		func(chatID int64) string {
			if chatID == streamChat {
				return "owner"
			}
			return ""
		})
}

func TestModelPicker_EditsInPlace(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	current := "sonnet"
	registerModel(r, &current)
	ctx := context.Background()

	b.dispatchSlash(ctx, slashMsg(streamChat, 7, "/model"), "/model")
	picker := lastCall(t, fake, "sendMessage")
	var opus string
	for _, btn := range buttonsOf(t, picker) {
		if strings.Contains(btn.Text, "sonnet") && btn.Style != "primary" {
			t.Errorf("current model not primary: %+v", btn)
		}
		if strings.Contains(btn.Text, "opus") {
			opus = btn.CallbackData
		}
	}
	if opus == "" {
		t.Fatalf("no opus button in %s", picker.Form["reply_markup"])
	}
	sends := len(fake.callsTo("sendMessage"))

	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 7, 500, opus))

	answeredOnce(t, fake, "q1")
	if current != "opus" {
		t.Fatalf("model = %q, want opus", current)
	}
	edit := lastCall(t, fake, "editMessageText")
	if edit.Form["message_id"] != "500" || !strings.Contains(edit.Form["text"], "Model set to opus") {
		t.Fatalf("picker not edited to the result: %v", edit.Form)
	}
	if n := len(fake.callsTo("sendMessage")); n != sends {
		t.Errorf("a press sent %d new messages, want an edit only", n-sends)
	}
}

// registerClear wires /clear and counts the clears.
func registerClear(r *control.Router, mu *sync.Mutex, cleared *int) {
	control.RegisterSessionCommands(r,
		func(_ context.Context, _ string, _ int, clear bool) (string, error) {
			if clear {
				mu.Lock()
				*cleared++
				mu.Unlock()
			}
			return "", nil
		},
		func(chatID int64) string {
			if chatID == streamChat {
				return "owner"
			}
			return ""
		})
}

func TestConfirm_YesRunsOnce(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	var mu sync.Mutex
	cleared := 0
	registerClear(r, &mu, &cleared)
	ctx := context.Background()

	b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/clear"), "/clear")
	prompt := lastCall(t, fake, "sendMessage")
	if cleared != 0 {
		t.Fatal("/clear acted before confirmation")
	}
	btns := buttonsOf(t, prompt)
	if len(btns) != 2 {
		t.Fatalf("confirmation buttons = %+v", btns)
	}
	yes, cancel := btns[0], btns[1]
	if !strings.HasPrefix(yes.CallbackData, "cf:") || yes.Style != "danger" ||
		!strings.HasPrefix(cancel.CallbackData, "cx:") {
		t.Fatalf("yes = %+v, cancel = %+v", yes, cancel)
	}

	// Two taps at once: one clear.
	var wg sync.WaitGroup
	for _, id := range []string{"q1", "q2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			b.callbackHandler(ctx, b.api, cbUpdate(id, streamChat, 0, 500, yes.CallbackData))
		}(id)
	}
	wg.Wait()
	answeredOnce(t, fake, "q1")
	answeredOnce(t, fake, "q2")
	if cleared != 1 {
		t.Fatalf("cleared %d times, want 1", cleared)
	}
	edit := lastCall(t, fake, "editMessageText")
	if edit.Form["message_id"] != "500" || !strings.Contains(edit.Form["text"], "Session cleared") {
		t.Fatalf("prompt not edited to the result: %v", edit.Form)
	}
	// Cancel after Yes: the pair is spent.
	b.callbackHandler(ctx, b.api, cbUpdate("q3", streamChat, 0, 500, cancel.CallbackData))
	if toast := answeredOnce(t, fake, "q3"); toast != cbToastExpired {
		t.Errorf("cancel after yes toast = %q", toast)
	}
}

func TestConfirm_CancelEditsToCancelled(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	var mu sync.Mutex
	cleared := 0
	registerClear(r, &mu, &cleared)
	ctx := context.Background()

	b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/clear"), "/clear")
	btns := buttonsOf(t, lastCall(t, fake, "sendMessage"))
	yes, cancel := btns[0], btns[1]

	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 0, 500, cancel.CallbackData))
	answeredOnce(t, fake, "q1")
	edit := lastCall(t, fake, "editMessageText")
	if edit.Form["message_id"] != "500" || !strings.Contains(edit.Form["text"], cmdTextCancelled) {
		t.Fatalf("not edited to cancelled: %v", edit.Form)
	}
	if len(buttonsOf(t, edit)) != 0 {
		t.Errorf("cancelled prompt keeps buttons: %s", edit.Form["reply_markup"])
	}
	// Yes after Cancel does nothing.
	b.callbackHandler(ctx, b.api, cbUpdate("q2", streamChat, 0, 500, yes.CallbackData))
	if toast := answeredOnce(t, fake, "q2"); toast != cbToastExpired {
		t.Errorf("yes after cancel toast = %q", toast)
	}
	if cleared != 0 {
		t.Fatal("cleared after cancel")
	}
}

func TestConfirm_ExpiresAfter5Minutes(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	var mu sync.Mutex
	cleared := 0
	registerClear(r, &mu, &cleared)
	at := time.Now()
	fakeClock(b, &at)
	ctx := context.Background()

	b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/clear"), "/clear")
	yes := buttonsOf(t, lastCall(t, fake, "sendMessage"))[0]
	at = at.Add(confirmTTL + time.Second)
	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 0, 500, yes.CallbackData))
	if toast := answeredOnce(t, fake, "q1"); toast != cbToastExpired {
		t.Errorf("toast = %q", toast)
	}
	if cleared != 0 {
		t.Fatal("expired confirmation ran")
	}
}

func TestConfirm_YesRechecksPerm(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	ran := 0
	r.Register("wipe", "wipe", "owner wipe", control.PermOwnerOnly,
		func(_ context.Context, cmd control.Command) (control.Response, error) {
			if !cmd.Confirmed {
				return control.Confirm(cmd, "Wipe?"), nil
			}
			ran++
			return control.Response{Text: "wiped"}, nil
		})
	// A Yes button for an owner-only command, shown in a non-owner chat.
	rows := b.commandRows(groupChat, control.Confirm(control.Command{Name: "wipe"}, "Wipe?").Buttons)

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", groupChat, 0, 900, rows[0][0].CallbackData))

	if toast := answeredOnce(t, fake, "q1"); toast != "Not authorized." {
		t.Errorf("toast = %q", toast)
	}
	if ran != 0 {
		t.Fatal("confirmed owner-only command ran in a non-owner chat")
	}
	edit := lastCall(t, fake, "editMessageText")
	if len(buttonsOf(t, edit)) != 0 || edit.Form["text"] != "Not authorized." {
		t.Fatalf("refusal edit: %v", edit.Form)
	}
}

func TestConfirm_ForeignPressDoesNotSpendPair(t *testing.T) {
	for _, choice := range []int{0, 1} {
		t.Run(fmt.Sprint(choice), func(t *testing.T) {
			b, fake, r := newCommandFixture(t)
			var mu sync.Mutex
			cleared := 0
			registerClear(r, &mu, &cleared)
			ctx := context.Background()
			b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/clear"), "/clear")
			buttons := buttonsOf(t, lastCall(t, fake, "sendMessage"))
			b.callbackHandler(ctx, b.api, cbUpdate("foreign", groupChat, 0, 500, buttons[choice].CallbackData))
			if toast := answeredOnce(t, fake, "foreign"); toast != cbToastNotYours {
				t.Fatalf("toast %q", toast)
			}
			if len(fake.callsTo("editMessageText")) != 0 || cleared != 0 {
				t.Fatal("foreign press acted")
			}
			b.callbackHandler(ctx, b.api, cbUpdate("owner", streamChat, 0, 500, buttons[0].CallbackData))
			answeredOnce(t, fake, "owner")
			if cleared != 1 {
				t.Fatal("foreign press spent confirmation")
			}
		})
	}
}

func TestConfirm_HandlerFailureRemovesButtonsAndAnswersEarly(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	r.Register("fail", "fail", "", control.PermChatLocal, func(_ context.Context, cmd control.Command) (control.Response, error) {
		if !cmd.Confirmed {
			return control.Confirm(cmd, "Proceed?"), nil
		}
		answeredOnce(t, fake, "yes") // must be answered before the handler runs
		return control.Response{}, fmt.Errorf("private failure details")
	})
	ctx := context.Background()
	b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/fail"), "/fail")
	yes := buttonsOf(t, lastCall(t, fake, "sendMessage"))[0]
	b.callbackHandler(ctx, b.api, cbUpdate("yes", streamChat, 0, 500, yes.CallbackData))
	answeredOnce(t, fake, "yes")
	edit := lastCall(t, fake, "editMessageText")
	if len(buttonsOf(t, edit)) != 0 || edit.Form["text"] != cbToastFailed {
		t.Fatalf("failure edit: %v", edit.Form)
	}
}

func TestConfirm_TopicGoneKeepsButtons(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	var mu sync.Mutex
	cleared := 0
	registerClear(r, &mu, &cleared)
	old := fake.respond
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "sendMessage" && form["message_thread_id"] == "7" {
			return apiError(400, "Bad Request: message thread not found")
		}
		return old(method, form)
	}
	b.dispatchSlash(context.Background(), slashMsg(streamChat, 7, "/clear"), "/clear")
	fallback := lastCall(t, fake, "sendMessage")
	if fallback.Form["message_thread_id"] != "" && fallback.Form["message_thread_id"] != "0" {
		t.Fatalf("not main fallback: %v", fallback.Form)
	}
	if len(buttonsOf(t, fallback)) != 2 {
		t.Fatalf("confirmation lost buttons: %v", fallback.Form)
	}
	if cleared != 0 {
		t.Fatal("fallback cleared without confirmation")
	}
}
