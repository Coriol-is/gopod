package control

// Confirm is the answer of a risky command that was not confirmed yet
// (D028): question as the text, Code CodeConfirm, and a Yes (danger)
// button that re-dispatches cmd with Command.Confirmed next to a Cancel
// button that dismisses. The Yes goes through the Router again, which
// checks Perm again. question names the command, never secret args.
func Confirm(cmd Command, question string) Response {
	return Response{
		Text: question,
		Code: CodeConfirm,
		Buttons: [][]Button{{
			{
				Label:     "Yes",
				Command:   cmd.Name,
				Args:      append([]string(nil), cmd.Args...),
				Style:     StyleDanger,
				Confirmed: true,
			},
			{Label: "Cancel"},
		}},
	}
}

// pickerPerRow is how many choices a picker puts in one row.
const pickerPerRow = 3

// pickerButtons offers one button per choice, each dispatching command
// with that choice as its only arg; current is marked (primary, ✓).
func pickerButtons(command string, choices []string, current string) [][]Button {
	var rows [][]Button
	var row []Button
	for _, c := range choices {
		b := Button{Label: c, Command: command, Args: []string{c}}
		if c == current {
			b.Label = "✓ " + c
			b.Style = StylePrimary
		}
		row = append(row, b)
		if len(row) == pickerPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// ParseConfirmFlag removes the CLI confirmation flag (--yes or -y) from
// args and reports whether it was there: the CLI's Command.Confirmed.
func ParseConfirmFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	yes := false
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
			continue
		}
		out = append(out, a)
	}
	return out, yes
}
