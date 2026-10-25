package runner

import (
	"context"
	"testing"
	"time"
)

// TestExtractStoresThread: the extraction callback of a turn gets the
// turn's conversation, so its facts are stored in that thread (D023).
// Both Run and RunStream.
func TestExtractStoresThread(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			type call struct {
				chat   string
				thread int
			}
			got := make(chan call, 4)
			h.r.SetExtractFn(func(_ context.Context, chat string, thread int, _, _ string) {
				got <- call{chat, thread}
			})
			wait := func(how string, want int) {
				t.Helper()
				select {
				case c := <-got:
					if c.chat != convChat || c.thread != want {
						t.Errorf("%s: extract got (%s, %d), want (%s, %d)", how, c.chat, c.thread, convChat, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("%s: no extraction", how)
				}
			}
			h.run(t, thread7)
			wait("Run", thread7)
			if res := h.runStream(t, thread9); res.Err != nil {
				t.Fatalf("RunStream: %v", res.Err)
			}
			wait("RunStream", thread9)
		})
	}
}
