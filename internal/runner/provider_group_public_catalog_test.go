package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/nativecatalog"
)

// Exercise every admitted public model's actual metadata through the pinned CLI,
// not only the simplified synthetic model. All provider/MCP endpoints are local
// mocks; each case uses a fresh synthetic HOME with no real account credentials.
func TestGroupNativeInventoryPublicCatalog(t *testing.T) {
	requireNativeGroupQualification(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "container", "group", "policy", "codex-models-0.164.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := nativecatalog.ParseSanitized(raw)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Models []struct {
			Slug          string `json:"slug"`
			ResponsesLite bool   `json:"use_responses_lite"`
			Search        bool   `json:"supports_search_tool"`
		}
	}
	if err = json.Unmarshal(catalog.JSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Models) != 11 {
		t.Fatal("public model set changed; requalify exact pinned catalog")
	}
	for _, model := range metadata.Models {
		for _, mode := range []string{"mcp", "native_attack", "patch_read_attack", "patch_write_attack", "file_read_attack"} {
			t.Run(model.Slug+"/"+mode, func(t *testing.T) {
				runNativeGroupFixture(t, nativeGroupCase{provider: "codex", mode: mode, model: model.Slug, rlite: model.ResponsesLite, search: model.Search, catalog: catalog.JSON})
			})
		}
	}
}
