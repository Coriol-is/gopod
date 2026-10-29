package telegram

import (
	"strings"
	"testing"
	"time"
)

func TestRichFormatting(t *testing.T) {
	for _, c := range []struct{ input, want string }{
		{"[link](https://example.com/a_b)", `<a href="https://example.com/a_b">link</a>`},
		{"# Title", "<b>Title</b>"}, {"- one\n- two", "• one\n• two"},
		{"1. one\n2. two", "1. one\n2. two"}, {"> quote", "<blockquote>quote</blockquote>"},
		{"~~old~~ ||secret||", "<s>old</s> <tg-spoiler>secret</tg-spoiler>"},
		{"`~~literal~~`", "<code>~~literal~~</code>"},
	} {
		if got := markdownToTelegramHTML(c.input); got != c.want {
			t.Errorf("%q: %q != %q", c.input, got, c.want)
		}
	}
}

func TestPrepareRichText(t *testing.T) {
	for _, c := range []struct{ format, input, want, mode string }{
		{"", "**bold**", "<b>bold</b>", "HTML"},
		{"plain", "**bold**", "**bold**", ""},
		{"html", `<b onclick="bad">ok</b>`, "<b>ok</b>", "HTML"},
		{"html", `<a href="javascript:bad">link</a>`, "link", "HTML"},
		{"html", `<b>broken`, "<b>broken", ""},
		{"html", `<b><i>x</b></i>`, `<b><i>x</b></i>`, ""},
		{"html", `<script>x</script>`, `&lt;script&gt;x&lt;/script&gt;`, "HTML"},
		{"html", `<blockquote expandable>hi &amp; bye</blockquote>`, `<blockquote expandable>hi &amp; bye</blockquote>`, "HTML"},
	} {
		parts, err := PrepareIPCText(c.input, c.format)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) != 1 || string(parts[0].Payload) != c.want || parts[0].ParseMode != c.mode {
			t.Errorf("%+v: %+v", c, parts)
		}
	}
	if _, err := PrepareIPCText("x", "bad"); err == nil {
		t.Fatal("accepted bad format")
	}
	parts, err := PrepareIPCText("<b>"+strings.Repeat("я &amp; ", 1500)+"</b>", "html")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if len(p.Payload) > 3500 {
			t.Fatal("oversized chunk")
		}
		if _, ok := sanitizeTelegramHTML(string(p.Payload)); !ok {
			t.Fatal("invalid chunk")
		}
	}
}

func TestOutboxPlainFallback(t *testing.T) {
	f := newOutboxFixture(t)
	delivery := f.publish(f.conv, "<b>hello &amp; bye</b>")
	tr := &fakeTransport{script: func(n int, req SendRequest) Outcome {
		if n == 1 {
			return Outcome{Kind: OutcomeRefused, Code: "parse_entities", HTTPStatus: 400}
		}
		if req.Text != "hello & bye" || req.ParseMode != "" {
			t.Errorf("fallback %+v", req)
		}
		return ack(n)
	}}
	d := NewOutboxDispatcher(f.owner, tr, fastCfg(), nil)
	if _, err := d.step(t.Context(), t.Context()); err != nil {
		t.Fatal(err)
	}
	if tr.callCount() != 2 || f.diag(delivery.ID).State != "sent" {
		t.Fatal(tr.callCount(), f.diag(delivery.ID))
	}
	if !strings.Contains(f.diag(delivery.ID).Summary(), "sent as plain text") {
		t.Fatal(f.diag(delivery.ID).Summary())
	}
	if _, err := d.step(t.Context(), t.Context()); err != nil {
		t.Fatal(err)
	}
	if tr.callCount() != 2 {
		t.Fatal("sent twice")
	}
	var n int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM outbox_attempts`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

func TestPlainFallbackNeverRetriesAmbiguousOrRefusedAgain(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second Outcome
		calls         int
		state         string
	}{
		{"timeout", Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}, Outcome{}, 1, "uncertain"},
		{"other refusal", Outcome{Kind: OutcomeRefused, Code: "bad_request", HTTPStatus: 400}, Outcome{}, 1, "failed"},
		{"fallback timeout", Outcome{Kind: OutcomeRefused, Code: "parse_entities", HTTPStatus: 400}, Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}, 2, "uncertain"},
		{"fallback refused", Outcome{Kind: OutcomeRefused, Code: "parse_entities", HTTPStatus: 400}, Outcome{Kind: OutcomeRefused, Code: "parse_entities", HTTPStatus: 400}, 2, "failed"},
		{"fallback rate limit", Outcome{Kind: OutcomeRefused, Code: "parse_entities", HTTPStatus: 400}, Outcome{Kind: OutcomeRateLimited, Code: "rate_limited"}, 2, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := f.publish(f.conv, "<b>hello</b>")
			tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
				if n == 1 {
					return tc.first
				}
				return tc.second
			}}
			d := NewOutboxDispatcher(f.owner, tr, fastCfg(), nil)
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			if tr.callCount() != tc.calls || f.diag(delivery.ID).State != tc.state {
				t.Fatal(tr.callCount(), f.diag(delivery.ID))
			}
		})
	}
}

func TestPlainFallbackCrashBeforeAckNeverReplays(t *testing.T) {
	f := newOutboxFixture(t)
	delivery := f.publish(f.conv, "<b>hello</b>")
	c, ok, err := f.owner.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatal(err)
	}
	next, err := f.owner.BeginPlainFallback(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	tr := &fakeTransport{}
	if tr.Attempt(t.Context(), SendRequest{Text: "hello"}).Kind != OutcomeAck {
		t.Fatal("fake acceptance failed")
	}
	// Simulate process death after acceptance, before any AckPart call.
	if n, err := f.owner.SweepSending(t.Context()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, ok, err = f.owner.ClaimNext(t.Context(), time.Now()); err != nil || ok {
		t.Fatal("replayed", err)
	}
	if f.diag(delivery.ID).State != "uncertain" || !next.PlainFallback {
		t.Fatal(f.diag(delivery.ID))
	}
	var hash string
	if err := f.st.DB().QueryRow(`SELECT plan_hash FROM delivery_groups WHERE delivery_id=?`, delivery.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != delivery.PlanHash {
		t.Fatal("plan changed")
	}
}
