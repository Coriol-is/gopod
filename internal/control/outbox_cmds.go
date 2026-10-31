package control

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

// RegisterOutboxCommands exposes evidence-based resolution through the Router.
func RegisterOutboxCommands(r *Router, st *store.Store) {
	r.Register("outbox", "outbox", "List or settle uncertain deliveries", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		if len(cmd.Args) == 0 || (len(cmd.Args) == 1 && cmd.Args[0] == "list") {
			parts, err := st.ListUncertainParts(ctx)
			if err != nil {
				return Response{}, err
			}
			text := "No uncertain deliveries."
			if len(parts) > 0 {
				var b strings.Builder
				b.WriteString("Uncertain deliveries (up to 100):\n")
				for _, p := range parts {
					fmt.Fprintf(&b, "%s — %s thread %d\n", p.PartID, p.ChatJID, p.ThreadID)
				}
				text = b.String()
			}
			return Response{Text: text + "\nUsage: /outbox settle <part-id> sent:<message-id>|unsent. Unsent cancels this delivery; it does not resend.", Data: map[string]any{"parts": parts}}, nil
		}
		if len(cmd.Args) != 3 || cmd.Args[0] != "settle" {
			return Response{Text: "Usage: /outbox settle <part-id> sent:<message-id>|unsent", Code: 1}, nil
		}
		var id int64
		evidence := cmd.Args[2]
		if evidence != "unsent" {
			if !strings.HasPrefix(evidence, "sent:") {
				return Response{Text: "Evidence must be sent:<positive-message-id> or unsent.", Code: 1}, nil
			}
			var err error
			id, err = strconv.ParseInt(strings.TrimPrefix(evidence, "sent:"), 10, 64)
			if err != nil || id <= 0 {
				return Response{Text: "A positive message id is required.", Code: 1}, nil
			}
		}
		if !cmd.Confirmed {
			return Confirm(cmd, "Settle this uncertain delivery using your evidence? Unsent cancels it without replay."), nil
		}
		if err := st.SettleUncertain(ctx, cmd.Args[1], id); err != nil {
			return Response{}, err
		}
		return Response{Text: "Delivery settled. No replay was requested."}, nil
	})
}
