package store

import (
	"testing"
	"time"
)

func TestPlainFallbackEnforcesClaimScopeAndBudget(t *testing.T) {
	for _, scenario := range []string{"scope", "budget", "expiry", "second"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDeliveryFixture(t)
			_, err := f.st.PublishDelivery(t.Context(), f.publishReq("", "<b>x</b>"))
			if err != nil {
				t.Fatal(err)
			}
			owner, err := f.st.AcquireOutboxOwner(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Release()
			c, ok, err := owner.ClaimNext(t.Context(), time.Now())
			if err != nil || !ok {
				t.Fatal(err)
			}
			switch scenario {
			case "scope":
				err = f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "changed"})
			case "budget":
				_, err = f.st.DB().Exec(`UPDATE outbox_parts SET attempts=? WHERE part_id=?`, MaxPartAttempts, c.PartID)
			case "expiry":
				if _, err = owner.beginPlainFallback(t.Context(), c, c.ExpiresAt); err == nil {
					t.Fatal("expired fallback accepted")
				}
				return
			case "second":
				c, err = owner.BeginPlainFallback(t.Context(), c)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = owner.BeginPlainFallback(t.Context(), c); err == nil {
				t.Fatal("unsafe fallback accepted")
			}
		})
	}
}

func TestPublishRejectsUnvalidatedHTMLAndParseModes(t *testing.T) {
	f := newDeliveryFixture(t)
	for _, p := range []DeliveryPart{
		{Kind: PartText, Payload: []byte("x"), ParseMode: "MarkdownV2"},
		{Kind: PartText, Payload: []byte("<b>broken"), ParseMode: "HTML"},
		{Kind: PartText, Payload: []byte(`<a href="javascript:bad">x</a>`), ParseMode: "HTML"},
	} {
		req := f.publishReq("")
		req.Parts = []DeliveryPart{p}
		if _, err := f.st.PublishDelivery(t.Context(), req); err == nil {
			t.Fatal("unsafe part accepted")
		}
	}
}
