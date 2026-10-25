package control

import "strings"

// FormatText renders a Response for a frontend without buttons (the CLI
// printer; the CLI frontend itself is C3, still deferred): the text,
// then one line per button with its label and the command it runs. A
// confirmation prints how to confirm from the command line instead.
func FormatText(resp Response) string {
	if len(resp.Buttons) == 0 && resp.Code != CodeConfirm {
		return resp.Text
	}
	var sb strings.Builder
	sb.WriteString(resp.Text)
	if resp.Code == CodeConfirm {
		sb.WriteString("\n\nRun the command again with --yes to confirm.")
		return sb.String()
	}
	sb.WriteString("\n")
	for _, row := range resp.Buttons {
		for _, b := range row {
			sb.WriteString("\n  [" + b.Label + "]")
			if b.Command != "" {
				sb.WriteString("  " + strings.TrimSpace(b.Command+" "+strings.Join(b.Args, " ")))
			}
		}
	}
	return sb.String()
}
