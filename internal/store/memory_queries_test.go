package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSearchMemoryTextTreatsEmbeddedQuotesAsLiteral(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.sqlite"), silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Exercise the real FTS index and its tokenizer, not just sanitizer output.
	for _, content := range []string{"x at y z", "alpha beta", "at home", "alpha OR beta"} {
		_, err := s.InsertMemory(ctx, MemoryWrite{
			Folder: "chat", Kind: "fact", Source: "manual", Content: content,
			Vector: MemoryVectorBlob(make([]float32, 1024)),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query     string
		want      string
		wantCount int
	}{
		{`x"at:y"z`, "x at y z", 1},
		{`x"at:missing"z`, "", 0},
		{`"alpha beta"`, "alpha beta", 2},
		{`at:home`, "at home", 1},
		{`alpha"OR"beta`, "alpha OR beta", 1},
	} {
		t.Run(tc.query, func(t *testing.T) {
			items, err := s.SearchMemoryText(ctx, "chat", 0, tc.query, 10)
			if err != nil {
				t.Fatalf("SearchMemoryText: %v", err)
			}
			found := false
			for _, item := range items {
				if item.Content == tc.want {
					found = true
				}
			}
			if len(items) != tc.wantCount {
				t.Fatalf("unexpected matches: %v", items)
			}
			if tc.want != "" && !found {
				t.Fatalf("missing %q in %v", tc.want, items)
			}
		})
	}
}
