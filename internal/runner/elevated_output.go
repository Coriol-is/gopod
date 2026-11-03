package runner

import (
	"encoding/json"
	"strings"
)

func parseElevatedCodex(output string) (string, string) {
	var texts []string
	var id string
	for _, line := range strings.Split(output, "\n") {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "thread.started" && validSessionID(ev.ThreadID) {
			id = ev.ThreadID
		}
		if ev.Type == "item.completed" && ev.Item.Type == "agent_message" {
			texts = append(texts, ev.Item.Text)
		}
	}
	return strings.Join(texts, "\n\n"), id
}
