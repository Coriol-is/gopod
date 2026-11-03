package runner

import (
	"testing"
	"time"
)

func TestStrictMemoryNeverBypassesUnknownOrLowMemory(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func() (int64, bool)
	}{
		{"missing", nil}, {"unreadable", func() (int64, bool) { return 0, false }}, {"low", func() (int64, bool) { return 100, true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGate(1, 1536, tc.read, nil)
			g.strictMemory = true
			w := &gateWaiter{class: ClassInteractive, since: time.Now().Add(-time.Hour)}
			g.waiting[ClassInteractive] = append(g.waiting[ClassInteractive], w)
			if reason, _, ok := g.tryAdmit(w); ok || reason != WaitMemory {
				t.Fatalf("admitted unsafe memory: %v %v", reason, ok)
			}
			g.readMem = func() (int64, bool) { return 1600, true }
			if _, _, ok := g.tryAdmit(w); !ok {
				t.Fatal("did not admit after memory recovered")
			}
		})
	}
}

func TestStrictMemoryReservesGrowthWithoutRecountingResidentUsage(t *testing.T) {
	available := int64(1754)
	g := newGate(2, 1536, func() (int64, bool) { return available, true }, nil)
	g.strictMemory = true
	g.growthReserveMB = 1024
	first := &gateWaiter{class: ClassInteractive}
	second := &gateWaiter{class: ClassInteractive}
	g.waiting[ClassInteractive] = []*gateWaiter{first, second}
	if _, _, ok := g.tryAdmit(first); !ok {
		t.Fatal("first admission denied")
	}
	if reason, _, ok := g.tryAdmit(second); ok || reason != WaitMemory {
		t.Fatal("second start ignored reserved growth")
	}
	available = 2700
	if _, _, ok := g.tryAdmit(second); !ok {
		t.Fatal("two agents not admitted with enough headroom")
	}
}
