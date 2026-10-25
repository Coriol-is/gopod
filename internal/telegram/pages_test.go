package telegram

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/control"
)

func TestPages_SplitOnLineBoundaries(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("line %03d %s", i, strings.Repeat("x", 40)))
	}
	text := strings.Join(lines, "\n")
	pages := splitPages(text, runeBudget(1000))
	if len(pages) < 2 {
		t.Fatalf("pages = %d, want several", len(pages))
	}
	for i, p := range pages {
		if n := utf8.RuneCountInString(p); n > 1000 {
			t.Errorf("page %d has %d runes", i, n)
		}
		for _, l := range strings.Split(p, "\n") {
			if !strings.HasPrefix(l, "line ") || len(l) != len(lines[0]) {
				t.Fatalf("page %d has a cut line %q", i, l)
			}
		}
	}
	if strings.Join(pages, "\n") != text {
		t.Error("pages do not add up to the text")
	}

	if p := splitPages("short\ntext", runeBudget(1000)); len(p) != 1 || p[0] != "short\ntext" {
		t.Errorf("short text = %q", p)
	}

	// A code block cut by a page boundary is closed and reopened.
	code := "intro\n```go\n" + strings.Repeat("fmt.Println(1)\n", 100) + "```\nend"
	for i, p := range splitPages(code, runeBudget(400)) {
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("page %d has unbalanced fences:\n%s", i, p)
		}
		if utf8.RuneCountInString(p) > 400 {
			t.Errorf("page %d over the limit", i)
		}
	}

	// A single line longer than a page is cut by runes.
	long := strings.Repeat("я", 2500)
	pages = splitPages(long, runeBudget(1000))
	if len(pages) != 3 || strings.Join(pages, "") != long {
		t.Errorf("long line pages = %d", len(pages))
	}
}

// registerLong wires /long, which answers with n numbered lines.
func registerLong(r *control.Router, n int) {
	r.Register("long", "long", "long output", control.PermPublic,
		func(context.Context, control.Command) (control.Response, error) {
			var sb strings.Builder
			for i := 0; i < n; i++ {
				fmt.Fprintf(&sb, "entry %04d %s\n", i, strings.Repeat("z", 60))
			}
			return control.Response{Text: sb.String()}, nil
		})
}

func TestPages_NavigateEditsSameMessage(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	registerLong(r, 300) // ~22 KB: several pages
	ctx := context.Background()

	b.dispatchSlash(ctx, slashMsg(streamChat, 7, "/long"), "/long")
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sendMessage calls = %d, want one paged message", len(sends))
	}
	first := sends[0]
	if !strings.Contains(first.Form["text"], "entry 0000") || strings.Contains(first.Form["text"], "entry 0299") {
		t.Fatalf("first page text wrong: %.80q", first.Form["text"])
	}
	btns := buttonsOf(t, first)
	var next string
	for _, btn := range btns {
		if len(btn.CallbackData) > cbMaxBytes {
			t.Errorf("page data %q over 64 bytes", btn.CallbackData)
		}
		if btn.Text == btnPageNext {
			next = btn.CallbackData
		}
		if btn.Text == btnPagePrev {
			t.Error("first page offers ‹")
		}
	}
	if !strings.HasPrefix(next, "pg:") {
		t.Fatalf("no › button: %+v", btns)
	}

	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 7, 500, next))
	answeredOnce(t, fake, "q1")
	if n := len(fake.callsTo("sendMessage")); n != 1 {
		t.Fatalf("navigation sent %d new messages", n-1)
	}
	edit := lastCall(t, fake, "editMessageText")
	if edit.Form["message_id"] != "500" || strings.Contains(edit.Form["text"], "entry 0000") {
		t.Fatalf("page 2 edit = %.120v", edit.Form)
	}
	var prev, counter string
	for _, btn := range buttonsOf(t, edit) {
		if btn.Text == btnPagePrev {
			prev = btn.CallbackData
		}
		if strings.HasPrefix(btn.Text, "2/") {
			counter = btn.Text
		}
	}
	if prev == "" || counter == "" {
		t.Fatalf("page 2 keyboard: %s", edit.Form["reply_markup"])
	}

	b.callbackHandler(ctx, b.api, cbUpdate("q2", streamChat, 7, 500, prev))
	answeredOnce(t, fake, "q2")
	if back := lastCall(t, fake, "editMessageText"); !strings.Contains(back.Form["text"], "entry 0000") {
		t.Fatal("‹ did not return to page 1")
	}
}

func TestPages_ExpiredToast(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	registerLong(r, 300)
	at := time.Now()
	fakeClock(b, &at)
	ctx := context.Background()

	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 0, 500, "pg:0123456789ab:1"))
	if toast := answeredOnce(t, fake, "q1"); toast != pagesToastExpired {
		t.Errorf("unknown pages toast = %q", toast)
	}

	b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/long"), "/long")
	var next string
	for _, btn := range buttonsOf(t, lastCall(t, fake, "sendMessage")) {
		if btn.Text == btnPageNext {
			next = btn.CallbackData
		}
	}
	// Pressed in another chat: refused.
	b.callbackHandler(ctx, b.api, cbUpdate("q2", groupChat, 0, 500, next))
	if toast := answeredOnce(t, fake, "q2"); toast != cbToastNotYours {
		t.Errorf("foreign chat toast = %q", toast)
	}
	at = at.Add(pagesTTL + time.Second)
	b.callbackHandler(ctx, b.api, cbUpdate("q3", streamChat, 0, 500, next))
	if toast := answeredOnce(t, fake, "q3"); toast != pagesToastExpired {
		t.Errorf("expired pages toast = %q", toast)
	}
	if n := len(fake.callsTo("editMessageText")); n != 0 {
		t.Errorf("expired press edited %d times", n)
	}
}

func runeBudget(n int) func(string) bool {
	return func(s string) bool { return utf8.RuneCountInString(s) <= n }
}

func TestPages_RenderedBudget(t *testing.T) {
	for name, text := range map[string]string{
		"cyrillic":  strings.Repeat("Русский текст памяти\n", 300),
		"escaped":   strings.Repeat("{\"value\": \"<&>\"}\n", 300),
		"long line": strings.Repeat("я<&>\"", 3000),
		"code":      "intro\n```go\n" + strings.Repeat("fmt.Println(\"я<&>\")\n", 300) + "```\nend",
	} {
		t.Run(name, func(t *testing.T) {
			b, fake, _ := newCommandFixture(t)
			b.sendCommandResponse(context.Background(), streamChat, 0, 0, control.Response{Text: text})
			if n := len(fake.callsTo("sendMessage")); n != 1 {
				t.Fatalf("sent %d messages", n)
			}
			b.btnMu.Lock()
			defer b.btnMu.Unlock()
			for _, reply := range b.pages {
				for i, p := range reply.pages {
					if !utf8.ValidString(p) {
						t.Fatalf("page %d invalid UTF-8", i)
					}
					if n := len(markdownToTelegramHTML(p)); n > pageMaxHTMLBytes {
						t.Fatalf("page %d: %d HTML bytes", i, n)
					}
					if n := len(markdownToTelegramHTML(threadGoneNote + p)); n > 4000 {
						t.Fatalf("fallback page %d: %d HTML bytes", i, n)
					}
				}
			}
		})
	}
}

func TestPages_NoEmptyFenceAtBoundary(t *testing.T) {
	text := "intro\n```go\n" + strings.Repeat("x", 2990) + "\n" + strings.Repeat("y", 2990) + "\n```"
	for i, p := range splitPages(text, runeBudget(3000)) {
		if strings.Contains(p, "```go\n```") {
			t.Fatalf("page %d contains an empty code block: %q", i, p)
		}
		if strings.Count(p, "```")%2 != 0 {
			t.Fatalf("page %d unbalanced: %q", i, p)
		}
	}
}

func TestPages_ForeignPressKeepsPageUsable(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	registerLong(r, 300)
	ctx := context.Background()
	b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/long"), "/long")
	next := buttonData(t, lastCall(t, fake, "sendMessage"), btnPageNext)
	b.callbackHandler(ctx, b.api, cbUpdate("foreign", groupChat, 0, 500, next))
	if toast := answeredOnce(t, fake, "foreign"); toast != cbToastNotYours {
		t.Fatalf("toast %q", toast)
	}
	if len(fake.callsTo("editMessageText")) != 0 {
		t.Fatal("foreign page press edited")
	}
	b.callbackHandler(ctx, b.api, cbUpdate("owner", streamChat, 0, 500, next))
	answeredOnce(t, fake, "owner")
	if len(fake.callsTo("editMessageText")) != 1 {
		t.Fatal("owner cannot navigate after foreign press")
	}
}

func TestPages_CounterNotModifiedStillAnswers(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	registerLong(r, 300)
	ctx := context.Background()
	b.dispatchSlash(ctx, slashMsg(streamChat, 0, "/long"), "/long")
	counter := buttonsOf(t, lastCall(t, fake, "sendMessage"))[0].CallbackData
	old := fake.respond
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "editMessageText" {
			return apiError(400, "Bad Request: message is not modified")
		}
		return old(method, form)
	}
	b.callbackHandler(ctx, b.api, cbUpdate("counter", streamChat, 0, 500, counter))
	answeredOnce(t, fake, "counter")
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("counter sent another message")
	}
}

func TestPages_TopicGoneKeepsNavigation(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	registerLong(r, 300)
	old := fake.respond
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "sendMessage" && form["message_thread_id"] == "7" {
			return apiError(400, "Bad Request: message thread not found")
		}
		return old(method, form)
	}
	b.dispatchSlash(context.Background(), slashMsg(streamChat, 7, "/long"), "/long")
	fallback := lastCall(t, fake, "sendMessage")
	buttonData(t, fallback, btnPageNext)
	if len(fallback.Form["text"]) > 4000 {
		t.Fatal("fallback split page budget")
	}
}
