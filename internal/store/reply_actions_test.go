package store

import (
	"fmt"
	"testing"
	"time"
)

func actionFixture(t *testing.T) (*deliveryFixture, int64, TurnResult, Delivery, *OutboxOwner) {
	t.Helper()
	f := topicDeliveryFixture(t)
	id := f.turn("telegram", "reply", 7, 1)
	r := f.freeze(ExecInteractive, "# Original source\n", ResultMember{TurnID: id, Attempt: 1})
	req := f.publishReq(r.ID, "rendered response")
	req.Parts[0].ReplyMarkup = fmt.Sprintf(`{"inline_keyboard":[[{"text":"More","callback_data":"mo:%d"}]]}`, id)
	d, err := f.st.PublishDelivery(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	return f, id, r, d, f.owner()
}
func ackAction(t *testing.T, o *OutboxOwner, msg int64) {
	t.Helper()
	c, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = o.AckPart(t.Context(), c, msg); err != nil {
		t.Fatal(err)
	}
}
func TestReplyActionRequiresCompletedAckAndPreservesOriginal(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	before, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !before.Managed || before.Eligible {
		t.Fatal(before, err)
	}
	ackAction(t, o, 55)
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Managed || !got.Eligible || !got.Newest || got.MessageID != 55 || got.Text != "# Original source\n" || got.Conversation != f.conv {
		t.Fatal(got, err)
	}
	path := f.st.path
	o.Release()
	if err = f.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after, err := st.ReplyAction(t.Context(), id)
	if err != nil || after != got {
		t.Fatal(after, err)
	}
}
func TestReplyActionCannotAuthorizeBareCompactedStaleOrUncertain(t *testing.T) {
	for _, scenario := range []string{"bare", "compacted", "stale", "uncertain", "newattempt"} {
		t.Run(scenario, func(t *testing.T) {
			f, id, _, _, o := actionFixture(t)
			c, ok, err := o.ClaimNext(t.Context(), time.Now())
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			switch scenario {
			case "bare":
				c, err = o.BeginKeyboardFallback(t.Context(), c)
				if err != nil {
					t.Fatal(err)
				}
			case "uncertain":
				if err = o.MarkUncertain(t.Context(), c, "timeout", ""); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "uncertain" {
				if err = o.AckPart(t.Context(), c, 55); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "compacted":
				if _, err = f.st.CompactTerminalDeliveries(t.Context(), time.Now().Add(31*24*time.Hour), 24*time.Hour, 1); err != nil {
					t.Fatal(err)
				}
			case "stale":
				if err = f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "successor"}); err != nil {
					t.Fatal(err)
				}
			case "newattempt":
				if _, err = f.st.db.Exec(`UPDATE turns SET attempts=attempts+1 WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			got, err := f.st.ReplyAction(t.Context(), id)
			if !got.Managed || got.Eligible {
				t.Fatal(got, err)
			}
			if scenario != "stale" && err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestReplyActionNewerDurableTextRevokesOlderRow(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	ackAction(t, o, 55)
	next := f.turn("telegram", "next", 7, 1)
	r := f.freeze(ExecInteractive, "new", ResultMember{TurnID: next, Attempt: 1})
	req := f.publishReq(r.ID, "new bare reply")
	if _, err := f.st.PublishDelivery(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	// Pending successors do not retire an already acknowledged reply.
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Newest {
		t.Fatal(got, err)
	}
	ackAction(t, o, 66)
	got, err = f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Eligible || got.Newest {
		t.Fatal(got, err)
	}
}
func TestReplyActionFrozenUnpublishedAndLegacyAreDistinct(t *testing.T) {
	f := topicDeliveryFixture(t)
	id := f.turn("telegram", "unpublished", 7, 1)
	legacy, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || legacy.Managed {
		t.Fatal(legacy, err)
	}
	f.freeze(ExecInteractive, "never delivered", ResultMember{TurnID: id, Attempt: 1})
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Managed || got.Eligible {
		t.Fatal(got, err)
	}
}

func TestReplyActionPartialAckDoesNotGrantAuthority(t *testing.T) {
	f := topicDeliveryFixture(t)
	id := f.turn("telegram", "partial", 7, 1)
	r := f.freeze(ExecInteractive, "source", ResultMember{TurnID: id, Attempt: 1})
	req := f.publishReq(r.ID, "first", "last")
	req.Parts[1].ReplyMarkup = fmt.Sprintf(`{"inline_keyboard":[[{"text":"More","callback_data":"mo:%d"}]]}`, id)
	if _, err := f.st.PublishDelivery(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	o := f.owner()
	ackAction(t, o, 55)
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Managed || got.Eligible {
		t.Fatal(got, err)
	}
	ackAction(t, o, 56)
	got, err = f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Eligible || got.MessageID != 56 {
		t.Fatal(got, err)
	}
}
func TestReplyActionDoesNotCrossTopicOrTaskAuthority(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	ackAction(t, o, 55)
	other := f.conv
	var err error
	f.conv, err = f.st.SnapshotConversation(t.Context(), "tg:1", 8, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	next := f.turn("telegram", "other-topic", 8, 1)
	r := f.freeze(ExecInteractive, "other topic", ResultMember{TurnID: next, Attempt: 1})
	if _, err = f.st.PublishDelivery(t.Context(), f.publishReq(r.ID, "newer")); err != nil {
		t.Fatal(err)
	}
	ackAction(t, o, 88)
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Newest || got.Conversation != other {
		t.Fatal(got, err)
	}
	task := f.turn("task", "task", 8, 1)
	f.freeze(ExecIsolatedTask, "private task source", ResultMember{TurnID: task, Attempt: 1})
	got, err = f.st.ReplyAction(t.Context(), task)
	if err != nil || !got.Managed || got.Eligible || got.Text != "" {
		t.Fatal(got, err)
	}
}
func TestReplyActionTopicDeletionFencesAcknowledgedRow(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	ackAction(t, o, 55)
	beginTopic(t, f.st, f.conv.Scope, 7)
	got, err := f.st.ReplyAction(t.Context(), id)
	if err == nil || !got.Managed || got.Eligible {
		t.Fatal(got, err)
	}
}

func TestReplyActionCompactedSuccessorStillSupersedesOlderRow(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	ackAction(t, o, 55)
	next := f.turn("telegram", "compact-successor", 7, 1)
	r := f.freeze(ExecInteractive, "newer", ResultMember{TurnID: next, Attempt: 1})
	newer, err := f.st.PublishDelivery(t.Context(), f.publishReq(r.ID, "newer"))
	if err != nil {
		t.Fatal(err)
	}
	ackAction(t, o, 66)
	// Select only B for retention. The result must not depend on B's payload or
	// attempt rows, both of which compaction intentionally discards.
	if _, err = f.st.db.Exec(`UPDATE delivery_groups SET updated_at=1 WHERE delivery_id=?`, newer.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.st.CompactTerminalDeliveries(t.Context(), time.Now(), 24*time.Hour, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Eligible || got.Newest {
		t.Fatal(got, err)
	}
}

func TestReplyActionAcknowledgedPlainFallback(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	c, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	c, err = o.BeginPlainFallback(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err = o.AckPart(t.Context(), c, 55); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Eligible || got.MessageID != 55 || got.Text != "# Original source\n" {
		t.Fatal(got, err)
	}
}

func TestReplyActionSelectedInlineCodeRequiresCompleteSuffix(t *testing.T) {
	f, r, p := codePlanFixture(t, 1)
	id := r.Members[0].TurnID
	markup := fmt.Sprintf(`{"inline_keyboard":[[{"text":"More","callback_data":"mo:%d"}]]}`, id)
	for i := range p.Request.CodeAlternatives.Variants {
		p.Request.CodeAlternatives.Variants[i] = []DeliveryPart{{Kind: PartText, Payload: []byte("prefix")}, {Kind: PartText, Payload: []byte("tail"), ReplyMarkup: markup}}
	}
	p.Request.Parts = append(p.Request.Parts[:1], p.Request.CodeAlternatives.Variants[1]...)
	publishCodeFixture(t, f, r, p)
	o := f.owner()
	c, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	handled, err := o.RefuseCodeDocument(t.Context(), c, "class=refused code=document_refused http=400")
	if err != nil || !handled {
		t.Fatal(handled, err)
	}
	ackAction(t, o, 55)
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || got.Eligible {
		t.Fatal(got, err)
	}
	ackAction(t, o, 56)
	got, err = f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Eligible || !got.Newest || got.MessageID != 56 || got.Text != r.Text {
		t.Fatal(got, err)
	}
}
