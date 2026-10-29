package store

import (
	"errors"
	"sync"
	"time"
)

// Defaults from the CP-003 spec (policy, not existing behavior). They are
// admission ceilings, not a performance claim for any device.
const (
	DefaultMaxArtifactFileBytes      int64 = 20 << 20
	DefaultMaxArtifactRequestBytes   int64 = 40 << 20
	DefaultMaxArtifactNamespaceBytes int64 = 512 << 20
	DefaultMaxArtifactGlobalBytes    int64 = 2 << 30
	DefaultMaxArtifactFiles                = 4
	DefaultPreparationTTL                  = 15 * time.Minute
	MaxPreparationTTL                      = 24 * time.Hour
	// DefaultArtifactRetention is how long after every reference is terminal
	// (sent, failed, cancelled, expired) a blob is kept. Uncertain and pending
	// references stay pinned regardless.
	DefaultArtifactRetention = 6 * time.Hour

	// Admission rate caps: a prompt-injected agent must not fill the quota or
	// spam the chat. Counted on reserved (upper-bound) files and measured bytes.
	DefaultMaxRunFiles  = 8
	DefaultMaxRunBytes  = 80 << 20
	DefaultMaxHourFiles = 40
	DefaultMaxHourBytes = 200 << 20
	artifactRateWindow  = time.Hour
)

// ArtifactLimits bounds stored plus reserved artifact bytes. A zero field means
// the default. Quarantined, deleting and reserved bytes all count.
type ArtifactLimits struct {
	MaxFileBytes      int64
	MaxRequestBytes   int64
	MaxNamespaceBytes int64
	MaxGlobalBytes    int64
	MaxFiles          int
	// Per run (reservation owner) and per namespace per hour.
	MaxRunFiles  int
	MaxRunBytes  int64
	MaxHourFiles int
	MaxHourBytes int64
}

type artifactConfig struct {
	mu     sync.RWMutex
	limits ArtifactLimits
}

// SetArtifactLimits replaces the artifact quotas. Negative values are rejected.
func (s *Store) SetArtifactLimits(l ArtifactLimits) error {
	if l.MaxFileBytes < 0 || l.MaxRequestBytes < 0 || l.MaxNamespaceBytes < 0 || l.MaxGlobalBytes < 0 || l.MaxFiles < 0 ||
		l.MaxRunFiles < 0 || l.MaxRunBytes < 0 || l.MaxHourFiles < 0 || l.MaxHourBytes < 0 {
		return errors.New("store: negative artifact limit")
	}
	s.artifacts.mu.Lock()
	defer s.artifacts.mu.Unlock()
	s.artifacts.limits = l
	return nil
}

func (s *Store) artifactLimits() ArtifactLimits {
	s.artifacts.mu.RLock()
	l := s.artifacts.limits
	s.artifacts.mu.RUnlock()
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = DefaultMaxArtifactFileBytes
	}
	if l.MaxRequestBytes == 0 {
		l.MaxRequestBytes = DefaultMaxArtifactRequestBytes
	}
	if l.MaxNamespaceBytes == 0 {
		l.MaxNamespaceBytes = DefaultMaxArtifactNamespaceBytes
	}
	if l.MaxGlobalBytes == 0 {
		l.MaxGlobalBytes = DefaultMaxArtifactGlobalBytes
	}
	if l.MaxFiles == 0 {
		l.MaxFiles = DefaultMaxArtifactFiles
	}
	if l.MaxRunFiles == 0 {
		l.MaxRunFiles = DefaultMaxRunFiles
	}
	if l.MaxRunBytes == 0 {
		l.MaxRunBytes = DefaultMaxRunBytes
	}
	if l.MaxHourFiles == 0 {
		l.MaxHourFiles = DefaultMaxHourFiles
	}
	if l.MaxHourBytes == 0 {
		l.MaxHourBytes = DefaultMaxHourBytes
	}
	return l
}

// ArtifactLimits returns the effective limits (defaults filled in), so the
// host validates requests against exactly what the store enforces.
func (s *Store) ArtifactLimits() ArtifactLimits { return s.artifactLimits() }
