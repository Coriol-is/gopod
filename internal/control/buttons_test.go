package control

import (
	"strings"
	"testing"
)

func TestResponseButtons_CLIPrintsLabels(t *testing.T) {
	resp := Response{
		Text: "Current model: opus",
		Buttons: [][]Button{
			{{Label: "opus", Command: "model", Args: []string{"opus"}, Style: StylePrimary}},
			{{Label: "sonnet", Command: "model", Args: []string{"sonnet"}}, {Label: "Cancel"}},
		},
	}
	out := FormatText(resp)
	for _, want := range []string{"Current model: opus", "[opus]", "model opus", "[sonnet]", "model sonnet", "[Cancel]"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatText missing %q:\n%s", want, out)
		}
	}
	if got := FormatText(Response{Text: "pong"}); got != "pong" {
		t.Errorf("no buttons: %q, want plain text", got)
	}
}
