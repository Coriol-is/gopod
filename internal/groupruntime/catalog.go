package groupruntime

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Coriol-is/gopod/internal/nativecatalog"
)

// Read a fresh explicit controller-owned catalog for each lease. Never consult
// mutable native caches, personal CLI directories or model-name SDK fallbacks.
func (m *Manager) qualifiedCatalog(provider string, cfg ProviderConfig, requested string) ([]byte, string, string, error) {
	if provider != "codex" {
		return nil, "", "", nil
	}
	if !filepath.IsAbs(m.DataDir) || filepath.Clean(m.DataDir) != m.DataDir || filepath.Dir(cfg.CatalogFile) != m.DataDir || filepath.Base(cfg.CatalogFile) == "." {
		return nil, "", "", ErrUnavailable
	}
	raw, err := readQualifiedCatalog(cfg.CatalogFile, m.UID)
	if err != nil {
		return nil, "", "", ErrUnavailable
	}
	catalog, err := nativecatalog.Sanitize(raw)
	if err != nil {
		return nil, "", "", ErrUnavailable
	}
	model, err := catalog.Select(requested, cfg.DefaultModel)
	if err != nil {
		return nil, "", "", ErrUnavailable
	}
	// The configured default must remain qualified even while a turn overrides it.
	if !catalog.HasModel(cfg.DefaultModel) {
		return nil, "", "", ErrUnavailable
	}
	return catalog.JSON, model, fmt.Sprintf("%x", sha256.Sum256(catalog.JSON)), nil
}
func readQualifiedCatalog(path string, uid int) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnavailable
	}
	if validateCatalogParents(path, uid) != nil {
		return nil, ErrUnavailable
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4<<20 || info.Mode().Perm()&0022 != 0 {
		return nil, ErrUnavailable
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != uid && st.Uid != 0 {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, ErrUnavailable
	}
	return raw, nil
}

// StageCatalogsForCanary copies only sanitized, explicitly granted public model
// metadata into a disposable root. Authentication references are never copied or
// changed. Production admission still requires a direct child of its DataDir.
func StageCatalogsForCanary(cfg Config, sourceRoot, destRoot string, uid int) (Config, error) {
	out := cfg
	out.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
	for name, provider := range cfg.Providers {
		out.Providers[name] = provider
	}
	for name, provider := range cfg.Providers {
		if name != "codex" {
			continue
		}
		metadata, _, _, err := (&Manager{DataDir: sourceRoot, UID: uid}).qualifiedCatalog(name, provider, "")
		if err != nil {
			return Config{}, ErrUnavailable
		}
		path := filepath.Join(destRoot, "group-codex-catalog.json")
		if !filepath.IsAbs(destRoot) || filepath.Clean(destRoot) != destRoot {
			return Config{}, ErrUnavailable
		}
		if err = validateCatalogParents(path, uid); err != nil {
			return Config{}, ErrUnavailable
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return Config{}, ErrUnavailable
		}
		_, err = f.Write(metadata)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return Config{}, ErrUnavailable
		}
		provider.CatalogFile = path
		out.Providers[name] = provider
	}
	return out, nil
}

func validateCatalogParents(path string, uid int) error {
	// Each ancestor must be host-owned and immune to replacement by the actor.
	// Root-owned sticky system temp directories retain that property for private
	// owned children; all non-sticky shared writable ancestors are rejected.
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid && st.Uid != 0 {
			return ErrUnavailable
		}
		if info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return ErrUnavailable
		}
		if parent == string(filepath.Separator) {
			break
		}
	}
	return nil
}
