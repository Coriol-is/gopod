package ipc

import (
	"testing"
)

func TestRichFieldsRejectedOutsideDurablePersonalRoute(t *testing.T) {
	for _, route := range []string{"disabled", "group", "owner-to-group", "group-to-owner"} {
		for _, field := range []string{"format", "buttons", "sticker"} {
			t.Run(route+"/"+field, func(t *testing.T) {
				f := newDurableFixture(t)
				folder, jid := "owner", "tg:1"
				switch route {
				case "disabled":
					f.store = nil
				case "group":
					folder, jid = "group-a", "tg:-2"
				case "owner-to-group":
					jid = "tg:-2"
				case "group-to-owner":
					folder = "group-a"
				}
				writeIPC(t, f.root, folder, "messages", "rich.json", map[string]any{"chatJid": jid, "text": "**literal**", field: nil})
				w := f.watcher()

				f.tick(w)
				if len(f.sink.callsSnapshot()) != 0 || f.deliveries() != 0 {
					t.Fatal("unsupported request sent")
				}
				if got := f.receipt(folder, "rich.result.json")["error"]; got != "rich_format_unavailable" {
					t.Fatal(got)
				}
			})
		}
	}
}
func TestInvalidFormatsAreTombstoned(t *testing.T) {
	for name, format := range map[string]any{"empty": "", "unknown": "bogus", "number": 42, "null": nil} {
		t.Run(name, func(t *testing.T) {
			f := newDurableFixture(t)
			writeIPC(t, f.root, "owner", "messages", "bad.json", map[string]any{"chatJid": "tg:1", "text": "hello", "format": format})
			w := f.watcher()

			f.tick(w)
			if got := f.receipt("owner", "bad.result.json")["error"]; got != "invalid_format" {
				t.Fatal(got)
			}
			if f.deliveries() != 0 || f.count(`SELECT count(*) FROM ipc_message_requests WHERE outcome='rejected'`) != 1 {
				t.Fatal("not rejected durably")
			}
		})
	}
}
func TestLegacyFormattingRemainsLiteral(t *testing.T) {
	f := newDurableFixture(t)
	f.store = nil
	text := "**bold** <b>html</b> & `literal`"
	f.send("owner", "literal.json", "tg:1", text)
	w := f.watcher()

	f.tick(w)
	calls := f.sink.callsSnapshot()
	if len(calls) != 1 || calls[0].text != text {
		t.Fatal(calls)
	}
}
