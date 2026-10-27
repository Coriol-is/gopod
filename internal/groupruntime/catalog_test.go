package groupruntime

import (
	"bytes"
	"errors"
	"github.com/Coriol-is/gopod/internal/isolationproxy"
	"github.com/Coriol-is/gopod/internal/nativecatalog"
	"github.com/Coriol-is/gopod/internal/runner"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const managerCatalog = `{"models":[{"base_instructions":"Synthetic local model only.","slug":"qualified-model","display_name":"Synthetic","description":null,"supported_reasoning_levels":[],"shell_type":"unified_exec","visibility":"list","supported_in_api":true,"priority":1,"availability_nux":null,"upgrade":null,"support_verbosity":false,"default_verbosity":null,"apply_patch_tool_type":"freeform","truncation_policy":{"mode":"bytes","limit":10000},"experimental_supported_tools":["clock","sleep","test_sync"],"supports_search_tool":true,"use_responses_lite":true}]}`

func catalogFile(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "catalog.json")
	if err = os.WriteFile(path, []byte(managerCatalog), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestManagerCatalogQualifiedFreshSelectionAndFingerprint(t *testing.T) {
	path := catalogFile(t)
	if _, err := readQualifiedCatalog(path, os.Getuid()); err != nil {
		t.Fatal("source reader rejected synthetic catalog")
	}
	if _, err := nativecatalog.Sanitize([]byte(managerCatalog)); err != nil {
		t.Fatal("sanitizer rejected synthetic catalog")
	}
	m := &Manager{DataDir: filepath.Dir(path), UID: os.Getuid()}
	cfg := ProviderConfig{CatalogFile: path, DefaultModel: "qualified-model"}
	raw, model, hash, err := m.qualifiedCatalog("codex", cfg, "")
	if err != nil || model != "qualified-model" || len(hash) != 64 || bytes.Contains(raw, []byte(`"apply_patch_tool_type":"freeform"`)) {
		t.Fatalf("catalog qualification failed %v", err)
	}
	if _, _, _, err = m.qualifiedCatalog("codex", cfg, "unqualified-model"); err == nil {
		t.Fatal("unknown selected model fell back")
	}
	cfg.DefaultModel = "unqualified-default"
	if _, _, _, err = m.qualifiedCatalog("codex", cfg, "qualified-model"); err == nil {
		t.Fatal("invalid default accepted with override")
	}
	cfg.DefaultModel = "qualified-model"
	// A changed controller snapshot must be loaded anew and change the policy.
	if err = os.WriteFile(path, []byte(strings.Replace(managerCatalog, `"display_name":"Synthetic"`, `"display_name":"Updated"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := m.qualifiedCatalog("codex", cfg, "qualified-model")
	if err != nil || changed == hash {
		t.Fatal("catalog change did not update fingerprint")
	}
	if err = os.WriteFile(path, []byte(`{"models":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = m.qualifiedCatalog("codex", cfg, ""); err == nil {
		t.Fatal("invalidated catalog reused stale native metadata")
	}
	if _, _, _, err = m.qualifiedCatalog("claude", ProviderConfig{}, "owner-model"); err != nil {
		t.Fatal("Claude personal model selection changed")
	}
}
func TestCatalogHostSourceFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "writable", "oversized", "malformed", "parent_symlink", "shared_parent"} {
		t.Run(kind, func(t *testing.T) {
			path := catalogFile(t)
			switch kind {
			case "missing":
				path += ".missing"
			case "symlink":
				if err := os.Symlink(path, path+".link"); err != nil {
					t.Fatal(err)
				}
				path += ".link"
			case "writable":
				if err := os.Chmod(path, 0660); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (4<<20)+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(path, []byte("PRIVATE_INVALID_JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "parent_symlink":
				parent := filepath.Dir(path)
				if err := os.Symlink(parent, parent+".link"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(parent + ".link") })
				path = filepath.Join(parent+".link", "catalog.json")
			case "shared_parent":
				if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
					t.Fatal(err)
				}
			}
			m := &Manager{DataDir: filepath.Dir(path), UID: os.Getuid()}
			_, _, _, err := m.qualifiedCatalog("codex", ProviderConfig{CatalogFile: path, DefaultModel: "qualified-model"}, "")
			if err == nil || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatalf("unsafe catalog accepted or disclosed: %v", err)
			}
		})
	}
}

func TestUnknownGroupModelDeniedBeforeDockerOrSessionExecution(t *testing.T) {
	m, scope, _ := receiptRequestFixture(t)
	path := filepath.Join(m.DataDir, "group-codex-catalog.json")
	if err := os.WriteFile(path, []byte(managerCatalog), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := ProviderConfig{Image: "qualified", AuthFile: "/explicit/auth", CatalogFile: path, DefaultModel: "qualified-model", Routes: []isolationproxy.Route{{Host: "chatgpt.com", Method: "POST", Path: "/backend-api/codex/responses", Body: isolationproxy.Responses}}}
	m.Config.Providers = map[string]ProviderConfig{"codex": cfg}

	registered, err := m.Store.GetRegistered(t.Context(), scope.JID)
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{"unqualified-model", "--model=private", ""} {
		if requested == "" {
			cfg.CatalogFile += ".missing"
			m.Config.Providers["codex"] = cfg
		}
		_, _, release, err := m.Start(t.Context(), registered, runner.NewCodexProvider(""), 7, requested)
		if release != nil {
			release()
		}
		var admission *AdmissionError
		if !errors.As(err, &admission) || admission.Stage != "configuration" {
			t.Fatalf("unknown model reached runtime: %v", err)
		}
	}
}

func TestEnableRejectsMissingCatalogBeforeEpochCompletion(t *testing.T) {
	m, _ := migrationManager(t)
	c, err := m.Store.GetRegistered(t.Context(), "tg:-2")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := m.Store.RotateGroupEpoch(t.Context(), c.JID, c.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.PrepareNamespacePaths(m.DataDir, migration.CurrentScope, m.UID); err != nil {
		t.Fatal(err)
	}
	if err = m.Store.SetNamespaceDisabled(t.Context(), migration.CurrentScope, true); err != nil {
		t.Fatal(err)
	}
	m.Config = Config{Qualification: "synthetic-qualified", EnabledGroups: []string{c.JID}, Providers: map[string]ProviderConfig{"codex": {CatalogFile: filepath.Join(m.DataDir, "missing.json"), DefaultModel: "qualified-model"}}}
	if _, err = m.Enable(t.Context(), c.JID); err == nil {
		t.Fatal("enabled absent catalog")
	}
	current, err := m.Store.GetRegistered(t.Context(), c.JID)
	if err != nil || current.GroupEpochReady {
		t.Fatal("invalid config completed epoch")
	}
	disabled, err := m.Store.GetState(t.Context(), "group_disabled:"+current.NamespaceID)
	if err != nil || disabled != "1" {
		t.Fatal("invalid config opened admission")
	}
}

func TestCatalogSourceCannotBeAgentWorkspaceOrOutsideDataRoot(t *testing.T) {
	path := catalogFile(t)
	base := filepath.Dir(path)
	for _, relative := range []string{"shared-namespaces/ns/e2/workspace/models.json", "chats/group/models.json", "snapshots/group/models.json"} {
		publicPath := filepath.Join(base, relative)
		if err := os.MkdirAll(filepath.Dir(publicPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(publicPath, []byte(managerCatalog), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readQualifiedCatalog(publicPath, os.Getuid()); err != nil {
			t.Fatal("fixture must otherwise meet UID/mode checks")
		}
		if _, _, _, err := (&Manager{DataDir: base, UID: os.Getuid()}).qualifiedCatalog("codex", ProviderConfig{CatalogFile: publicPath, DefaultModel: "qualified-model"}, ""); err == nil {
			t.Fatal("agent-writable source accepted")
		}
	}
	outside := catalogFile(t)
	if _, _, _, err := (&Manager{DataDir: base, UID: os.Getuid()}).qualifiedCatalog("codex", ProviderConfig{CatalogFile: outside, DefaultModel: "qualified-model"}, ""); err == nil {
		t.Fatal("external source accepted")
	}
}
func TestCanaryCatalogStagingIsMetadataOnlyAndImmutable(t *testing.T) {
	path := catalogFile(t)
	source := filepath.Dir(path)
	dest, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Providers: map[string]ProviderConfig{"codex": {CatalogFile: path, DefaultModel: "qualified-model", AuthFile: "/PRIVATE_AUTH_REFERENCE"}, "claude": {AuthFile: "/OTHER_AUTH_REFERENCE"}}}
	staged, err := StageCatalogsForCanary(cfg, source, dest, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["codex"].CatalogFile != path || staged.Providers["codex"].CatalogFile != filepath.Join(dest, "group-codex-catalog.json") || staged.Providers["codex"].AuthFile != cfg.Providers["codex"].AuthFile || !reflect.DeepEqual(staged.Providers["claude"], cfg.Providers["claude"]) {
		t.Fatal("staging mutated source map or authentication grants")
	}
	raw, err := os.ReadFile(staged.Providers["codex"].CatalogFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Synthetic local model only.")) || bytes.Contains(raw, []byte("PRIVATE_")) {
		t.Fatal("persona or authentication copied")
	}
	if _, err = nativecatalog.ParseSanitized(raw); err != nil {
		t.Fatal("staged metadata not capped")
	}
	if _, err = StageCatalogsForCanary(cfg, source, dest, os.Getuid()); err == nil {
		t.Fatal("staging overwrote existing grant")
	}
	if _, err = StageCatalogsForCanary(cfg, filepath.Dir(source), dest, os.Getuid()); err == nil {
		t.Fatal("source outside explicit root accepted")
	}
}
