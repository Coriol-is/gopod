package control

import (
	"context"
	"fmt"
	"strings"
)

// MemoryOps is the interface control needs from the memory layer.
// Defined here (consumer-side) to avoid importing internal/memory.
type MemoryOps interface {
	Add(ctx context.Context, chatFolder, kind, title, content, source string) (int64, error)
	SearchText(ctx context.Context, chatFolder, query string, k int) ([]MemoryResult, error)
}

// MemoryResult is a search result for display.
type MemoryResult struct {
	ID      int64
	Kind    string
	Title   string
	Content string
	Score   float64
}

// RegisterMemoryCommands adds /remember and /recall to the Router.
// Both are ChatLocal — any registered chat can manage its own memories.
func RegisterMemoryCommands(r *Router, ops MemoryOps, chatFolderLookup func(chatID int64) string) {
	r.Register("remember", "remember", "save a fact to memory", PermChatLocal,
		rememberHandler(ops, chatFolderLookup))
	r.Register("recall", "recall", "search your memories", PermChatLocal,
		recallHandler(ops, chatFolderLookup))
}

func rememberHandler(ops MemoryOps, lookup func(int64) string) Handler {
	return func(ctx context.Context, cmd Command) (Response, error) {
		folder := lookup(cmd.Caller.ChatID)
		if folder == "" {
			return Response{Text: "This chat is not registered.", Code: 1}, nil
		}
		if len(cmd.Args) == 0 {
			return Response{
				Text: "Usage: /remember <fact or preference to save>\n\n" +
					"Examples:\n" +
					"  /remember I prefer Python over JavaScript\n" +
					"  /remember My timezone is GMT+3\n" +
					"  /remember Project deadline is April 30\n",
				Code: 1,
			}, nil
		}

		content := strings.Join(cmd.Args, " ")
		id, err := ops.Add(ctx, folder, "fact", "", content, "user")
		if err != nil {
			return Response{}, fmt.Errorf("remember: %w", err)
		}
		return Response{
			Text: fmt.Sprintf("Remembered (id %d): %s", id, truncateStr(content, 80)),
		}, nil
	}
}

func recallHandler(ops MemoryOps, lookup func(int64) string) Handler {
	return func(ctx context.Context, cmd Command) (Response, error) {
		folder := lookup(cmd.Caller.ChatID)
		if folder == "" {
			return Response{Text: "This chat is not registered.", Code: 1}, nil
		}
		if len(cmd.Args) == 0 {
			return Response{
				Text: "Usage: /recall <search query>\n\n" +
					"Examples:\n" +
					"  /recall programming language preference\n" +
					"  /recall timezone\n" +
					"  /recall project deadline\n",
				Code: 1,
			}, nil
		}

		query := strings.Join(cmd.Args, " ")
		results, err := ops.SearchText(ctx, folder, query, 5)
		if err != nil {
			return Response{}, fmt.Errorf("recall: %w", err)
		}
		if len(results) == 0 {
			return Response{Text: "No memories found for: " + query}, nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Memories matching %q:\n\n", query)
		for i, r := range results {
			if r.Title != "" {
				fmt.Fprintf(&sb, "%d. [%s] %s: %s (score: %.3f)\n", i+1, r.Kind, r.Title, truncateStr(r.Content, 60), r.Score)
			} else {
				fmt.Fprintf(&sb, "%d. [%s] %s (score: %.3f)\n", i+1, r.Kind, truncateStr(r.Content, 80), r.Score)
			}
		}
		return Response{Text: sb.String()}, nil
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
