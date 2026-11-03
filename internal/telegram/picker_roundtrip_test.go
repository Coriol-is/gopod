package telegram

import (
	"context"
	"testing"

	"github.com/Coriol-is/gopod/internal/control"
)

// Exercise the actual slash -> callback -> edit -> callback path: successful
// selections must leave usable buttons in the same message, not just text.
func TestSettingPickerRetainsChoicesAfterSelection(t *testing.T) {
	for _, command := range []string{"silent", "model"} {
		t.Run(command, func(t *testing.T) {
			b, fake, r := newCommandFixture(t)
			current, selected, reset := "off", "on", "off"
			folder := func(int64) string { return "owner" }
			if command == "silent" {
				control.RegisterSilentCommand(r, func(string) bool { return current == "on" },
					func(_ string, on bool) error {
						current = "off"
						if on {
							current = "on"
						}
						return nil
					}, folder)
			} else {
				current, selected, reset = "", "sonnet", "default"
				control.RegisterModelCommand(r, func(string) string { return current },
					func(_, model string) (string, error) { current = model; return "Model updated", nil },
					func(string) []string { return []string{"sonnet", "haiku"} }, folder)
			}
			ctx := context.Background()
			b.dispatchSlash(ctx, slashMsg(streamChat, 7, "/"+command), "/"+command)
			initial := lastCall(t, fake, "sendMessage")
			b.callbackHandler(ctx, b.api, cbUpdate("select", streamChat, 7, 500, buttonData(t, initial, selected)))
			answeredOnce(t, fake, "select")
			edit := lastCall(t, fake, "editMessageText")
			if edit.Form["message_id"] != "500" || current != selected {
				t.Fatalf("selection: state=%q edit=%v", current, edit.Form)
			}
			buttonData(t, edit, "✓ "+selected)
			b.callbackHandler(ctx, b.api, cbUpdate("reset", streamChat, 7, 500, buttonData(t, edit, reset)))
			answeredOnce(t, fake, "reset")
			edit = lastCall(t, fake, "editMessageText")
			want := reset
			if command == "model" {
				want = ""
			}
			if edit.Form["message_id"] != "500" || current != want {
				t.Fatalf("reset: state=%q edit=%v", current, edit.Form)
			}
			buttonData(t, edit, "✓ "+reset)
			if len(fake.callsTo("sendMessage")) != 1 {
				t.Fatal("picker interaction sent an extra message instead of editing")
			}
		})
	}
}
