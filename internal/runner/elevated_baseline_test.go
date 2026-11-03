package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestElevatedOrdinaryBaseline(t *testing.T) {
	cfg := goodSpawnConfig()
	cfg.Mounts = []Mount{{Source: "/data/chat", Target: "/workspace/chat"}}
	conf, host, name, err := BuildContainerArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent([]any{conf, host, name}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/elevated-ordinary-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, want) {
		t.Fatal("ordinary container arguments changed from pre-E1 baseline")
	}
	if got := digestOfSpawn(conf, host, cfg.Mounts, nil); got != "4cb6ef69dcbbb79fddfdce570be89326b207ffb4bb294170fc457d911973a057" {
		t.Fatalf("ordinary digest changed: %s", got)
	}
}
