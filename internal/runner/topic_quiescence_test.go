package runner

import "testing"

func TestRetiredTopicCountersPreserveSiblings(t *testing.T) {
	r := &Runner{turnCount: map[convKey]int{{"owner", 7}: 3, {"owner", 8}: 4, {"owner", 0}: 5, {"other", 7}: 6}}
	r.ForgetRetiredConversationCounters("owner", 7)
	r.ForgetRetiredConversationCounters("owner", 7)
	r.ForgetRetiredConversationCounters("owner", 0)
	if _, exists := r.turnCount[convKey{"owner", 7}]; exists {
		t.Fatal("retired topic count retained")
	}
	for k, want := range map[convKey]int{{"owner", 8}: 4, {"owner", 0}: 5, {"other", 7}: 6} {
		if got := r.turnCount[k]; got != want {
			t.Fatalf("%v count=%d want=%d", k, got, want)
		}
	}
}
