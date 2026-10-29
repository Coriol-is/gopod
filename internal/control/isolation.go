package control

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Registered groups are prepared and enabled automatically (D047); prepare and
// enable remain for manual repair. disable is the per-group owner kill switch:
// it revokes new admissions at once and automatic preparation never undoes it.
//
// IsolationActions are controller-owned operations, never agent-provided policy.
// Targets are canonical negative Telegram group JIDs; Status may receive "" to
// return a metadata-only inventory. Implementations serialize lifecycle changes,
// validate scope/CAS and keep secrets and private paths out of returned text.
type IsolationActions struct {
	Status  func(context.Context, string) (string, error)
	Prepare func(context.Context, string) (string, error)
	Enable  func(context.Context, string) (string, error)
	Disable func(context.Context, string) (string, error)
}

func RegisterIsolationCommands(r *Router, actions IsolationActions) {
	r.Register("isolation", "isolation", "(owner) group isolation: status, repair, per-group disable", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		usage := func() (Response, error) {
			return Response{Code: 1, Text: "Usage: /isolation [status|prepare|enable|disable] [group_chat_id]"}, nil
		}
		op := "status"
		if len(cmd.Args) > 0 {
			op = cmd.Args[0]
		}
		if len(cmd.Args) > 2 {
			return usage()
		}
		var action func(context.Context, string) (string, error)
		switch op {
		case "status":
			action = actions.Status
		case "prepare":
			action = actions.Prepare
		case "enable":
			action = actions.Enable
		case "disable":
			action = actions.Disable
		default:
			return usage()
		}
		target := ""
		if len(cmd.Args) == 2 {
			text := strings.TrimPrefix(cmd.Args[1], "tg:")
			id, err := strconv.ParseInt(text, 10, 64)
			if err != nil || id >= 0 || text != strconv.FormatInt(id, 10) {
				return usage()
			}
			target = "tg:" + text
		}
		if op != "status" && target == "" {
			return usage()
		}
		if action == nil {
			return Response{Code: 1, Text: "Group isolation operation is unavailable."}, nil
		}
		if op != "status" && !cmd.Confirmed {
			return Confirm(cmd, fmt.Sprintf("Confirm group isolation %s for %s? Existing data is preserved; this operation does not grant personal services.", op, target)), nil
		}
		text, err := action(ctx, target)
		if err != nil {
			return Response{Code: 1, Text: "Group isolation operation failed. Inspect owner diagnostics before retrying."}, fmt.Errorf("isolation %s: %w", op, err)
		}
		return Response{Text: text}, nil
	})
}
