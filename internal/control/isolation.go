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

const isolationUsage = "Usage: /isolation [status|prepare|enable|disable] [group_chat_id]"

// RegisterIsolationCommands registers /isolation. The owner's private chat
// manages any group by id (D047). The owner's own user id in a group manages
// that group only (D048): status and the disable kill switch run at once;
// enable and prepare change the security boundary, so they are confirmed in the
// owner's private chat and never by a button pressed in the group.
func RegisterIsolationCommands(r *Router, actions IsolationActions) {
	r.Register("isolation", "isolation", "(owner) group isolation: status, repair, per-group disable", PermGroupOwner, isolationHandler(actions))
}

func (a IsolationActions) pick(op string) func(context.Context, string) (string, error) {
	switch op {
	case "status":
		return a.Status
	case "prepare":
		return a.Prepare
	case "enable":
		return a.Enable
	case "disable":
		return a.Disable
	}
	return nil
}

func isolationHandler(actions IsolationActions) Handler {
	return func(ctx context.Context, cmd Command) (Response, error) {
		op, target, ok := parseIsolation(cmd)
		if !ok {
			return Response{Code: 1, Text: isolationUsage}, nil
		}
		action := actions.pick(op)
		if action == nil {
			return Response{Code: 1, Text: "Group isolation operation is unavailable."}, nil
		}
		if resp, asked := isolationConfirmation(cmd, op, target); asked {
			return resp, nil
		}
		text, err := action(ctx, target)
		if err != nil {
			return Response{Code: 1, Text: "Group isolation operation failed. Inspect owner diagnostics before retrying."}, fmt.Errorf("isolation %s: %w", op, err)
		}
		return Response{Text: text}, nil
	}
}

// parseIsolation returns the operation and the canonical target jid ("" is the
// all-groups status of the private chat). From a group the target is always
// the calling group; naming another group is refused.
func parseIsolation(cmd Command) (op, target string, ok bool) {
	op = "status"
	if len(cmd.Args) > 0 {
		op = cmd.Args[0]
	}
	if len(cmd.Args) > 2 || op != "status" && op != "prepare" && op != "enable" && op != "disable" {
		return "", "", false
	}
	if len(cmd.Args) == 2 {
		text := strings.TrimPrefix(cmd.Args[1], "tg:")
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || id >= 0 || text != strconv.FormatInt(id, 10) {
			return "", "", false
		}
		target = "tg:" + text
	}
	if cmd.Caller.ChatID < 0 {
		own := "tg:" + strconv.FormatInt(cmd.Caller.ChatID, 10)
		if target != "" && target != own {
			return "", "", false
		}
		return op, own, true
	}
	if op != "status" && target == "" {
		return "", "", false
	}
	return op, target, true
}

// isolationConfirmation reports whether the operation must be confirmed first
// and returns the question. It never lets a group-side Confirmed flag stand in
// for the private confirmation of enable and prepare.
func isolationConfirmation(cmd Command, op, target string) (Response, bool) {
	if op == "status" {
		return Response{}, false
	}
	question := fmt.Sprintf("Confirm group isolation %s for %s? Existing data is preserved; this operation does not grant personal services.", op, target)
	if cmd.Caller.ChatID < 0 {
		if op == "disable" {
			return Response{}, false // the kill switch is immediate
		}
		resp := Confirm(Command{Name: cmd.Name, Args: []string{op, target}}, question)
		resp.Private = true
		resp.Ack = "Confirmation sent to the owner's private chat."
		return resp, true
	}
	if !cmd.Confirmed {
		return Confirm(cmd, question), true
	}
	return Response{}, false
}
