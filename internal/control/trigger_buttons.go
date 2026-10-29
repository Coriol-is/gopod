package control

import (
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

// triggerModes lists the picker rows in display order.
var triggerModes = []store.TriggerMode{
	store.TriggerOwner, store.TriggerMention, store.TriggerOwnerMention, store.TriggerAll,
}

// triggerButtons is the /trigger picker: one row per mode, the current one
// marked. A press dispatches /trigger again, so PermGroupOwner is checked on
// the presser. From a group the args are just the mode; from the private chat
// they lead with the numeric group id (no "tg:" prefix, no ':' in any arg).
func triggerButtons(jid string, current store.TriggerMode, fromGroup bool) [][]Button {
	rows := make([][]Button, 0, len(triggerModes))
	for _, m := range triggerModes {
		args := []string{string(m)}
		if !fromGroup {
			args = []string{strings.TrimPrefix(jid, "tg:"), string(m)}
		}
		b := Button{Label: string(m), Command: "trigger", Args: args}
		if m == current {
			b.Label = "✓ " + b.Label
			b.Style = StylePrimary
		}
		rows = append(rows, []Button{b})
	}
	return rows
}
