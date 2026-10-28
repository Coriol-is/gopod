package store

import (
	"errors"
	"testing"
)

func TestRecentIPCRequestsReportsDeliveryState(t *testing.T) {
	f := newDeliveryFixture(t)
	mk := func(key, text string) Delivery {
		id := &RequestIdentity{Key: key, PayloadHash: sha256Hex([]byte(text))}
		d, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC, Parts: textParts(text), Request: id})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a := mk("a.json", "a")
	b := mk("b.json", "b")
	if _, err := f.st.RecordRequestRejection(t.Context(), f.conv.Scope, RequestIdentity{Key: "c.json", PayloadHash: sha256Hex([]byte("c"))}, "empty_text"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.ExecContext(t.Context(), `UPDATE delivery_groups SET state='sent', unresolved=0 WHERE delivery_id=?`, a.ID); err != nil {
		t.Fatal(err)
	}

	got, err := f.st.RecentIPCRequests(t.Context(), f.conv.Scope, 10)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]IPCRequestStatus{}
	for _, r := range got {
		by[r.Key] = r
	}
	if len(got) != 2 {
		t.Fatalf("only accepted requests are listed, got %+v", got)
	}
	if r := by["a.json"]; r.DeliveryID != a.ID || r.DeliveryState != DeliverySent || r.Unresolved {
		t.Fatalf("a: %+v", r)
	}
	if r := by["b.json"]; r.DeliveryID != b.ID || r.DeliveryState != DeliveryPending || !r.Unresolved {
		t.Fatalf("b: %+v", r)
	}
	if got, _ = f.st.RecentIPCRequests(t.Context(), f.conv.Scope, 1); len(got) != 1 || got[0].Key != "b.json" {
		t.Fatalf("limit must keep the newest: %+v", got)
	}

	stale := f.conv.Scope
	stale.RegistrationRevision++
	if _, err = f.st.RecentIPCRequests(t.Context(), stale, 10); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("stale scope: %v", err)
	}
}
