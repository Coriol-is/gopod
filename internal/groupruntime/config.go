// Package groupruntime orchestrates the qualified shared-space boundary. Native
// subscriptions stay in trusted inference containers; untrusted tools have no
// credential, host-file or network authority.
package groupruntime

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Coriol-is/gopod/internal/isolationproxy"
)

var ErrUnavailable = errors.New("group isolation unavailable")

type ProviderConfig struct {
	Image string
	// AuthFile is an explicit auth-only subscription reference. No personal CLI
	// home, settings or integration configuration is ever copied or mounted.
	AuthFile     string
	CatalogFile  string
	DefaultModel string
	Routes       []isolationproxy.Route
}
type Config struct {
	// AdmissionMode is "registered" (default: an owner-registered group with a
	// ready clean epoch is admitted) or "allowlist" (strict CP-020 behavior: the
	// JID must also appear in EnabledGroups).
	AdmissionMode string
	// MaxGroups bounds prepared groups in registered mode; zero means 50.
	MaxGroups int
	// EnabledGroups is the allowlist in allowlist mode and ignored in registered
	// mode (kept so older configuration files still load).
	EnabledGroups []string
	Providers     map[string]ProviderConfig
	RelayImage    string
	ToolImage     string
	// Qualification is the operator-controlled policy revision, set only after
	// pinned native inventory and Docker isolation qualification. Missing denies.
	Qualification string
}

func Load(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return cfg, ErrUnavailable
	}
	d := json.NewDecoder(io.LimitReader(f, 65536))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil {
		return cfg, ErrUnavailable
	}
	var rest any
	if !errors.Is(d.Decode(&rest), io.EOF) {
		return cfg, ErrUnavailable
	}
	if cfg.RelayImage == "" || cfg.ToolImage == "" || len(cfg.Qualification) < 8 || len(cfg.EnabledGroups) > 10 || !cfg.validAdmission() {
		return cfg, ErrUnavailable
	}
	for _, jid := range cfg.EnabledGroups {
		if !strings.HasPrefix(jid, "tg:-") {
			return cfg, ErrUnavailable
		}
	}
	for name, p := range cfg.Providers {
		if name != "claude" && name != "codex" || p.Image == "" || p.AuthFile == "" || validateProviderRoutes(name, p.Routes) != nil || name == "codex" && (!filepath.IsAbs(p.CatalogFile) || filepath.Clean(p.CatalogFile) != p.CatalogFile || p.DefaultModel == "") {
			return cfg, ErrUnavailable
		}
	}
	return cfg, nil
}

// A readonly shared subscription reference must never rotate the owner's refresh
// token or reach account services. Expiry fails closed until personal management
// refreshes the source; no auth/token route belongs to this profile.
func validateProviderRoutes(provider string, routes []isolationproxy.Route) error {
	if len(routes) == 0 || len(routes) > 3 {
		return ErrUnavailable
	}
	inference := false
	seen := map[string]bool{}
	for _, r := range routes {
		key := r.Host + " " + r.Method + " " + r.Path + "?" + r.Query
		if seen[key] {
			return ErrUnavailable
		}
		seen[key] = true
		if len(r.JSONFields) != 0 {
			return ErrUnavailable
		}
		switch provider {
		case "claude":
			if r.Host != "api.anthropic.com" || r.Method != http.MethodPost || r.Path != "/v1/messages" || (r.Query != "" && r.Query != "beta=true") || r.Body != isolationproxy.AnthropicMessages {
				return ErrUnavailable
			}
			inference = true
		case "codex":
			if r.Host != "chatgpt.com" {
				return ErrUnavailable
			}
			if r.Method == http.MethodPost && r.Path == "/backend-api/codex/responses" && r.Query == "" && r.Body == isolationproxy.Responses {
				inference = true
				continue
			}
			if r.Method == http.MethodGet && r.Path == "/backend-api/codex/models" && r.Query == "client_version=0.164.0" && r.Body == isolationproxy.NoBody {
				continue
			}
			return ErrUnavailable
		default:
			return ErrUnavailable
		}
	}
	if !inference {
		return ErrUnavailable
	}
	return nil
}
