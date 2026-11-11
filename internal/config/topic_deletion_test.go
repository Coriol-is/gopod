package config

import "testing"

func TestTopicDeletionGateDefaultsOff(t *testing.T) {
	for _, v := range []string{"", "0", "1"} {
		t.Setenv("GOPOD_TOPIC_DELETION_ENABLED", v)
		cfg, err := Load(false)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TopicDeletionEnabled != (v == "1") {
			t.Fatal(v, cfg.TopicDeletionEnabled)
		}
	}
}
