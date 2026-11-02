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
		query string
		want  string
	}{
		{`x"at:y"z`, "x at y z"},
		{`x"at:missing"z`, ""},
		{`"alpha beta"`, "alpha beta"},
		{`at:home`, "at home"},
		{`alpha"OR"beta`, "alpha OR beta"},
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
			if tc.want == "" && len(items) != 0 {
				t.Fatalf("unexpected matches: %v", items)
			}
			if tc.want != "" && !found {
				t.Fatalf("missing %q in %v", tc.want, items)
			}
		})
	}
}
