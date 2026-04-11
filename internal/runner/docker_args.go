package runner

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/strslice"
)

// SpawnConfig is the full input to BuildContainerArgs — everything
// picoclaw needs to decide to start one agent container for one chat.
//
// Every field must be populated by the caller. The resulting
// (container.Config, container.HostConfig) pair is what we hand to
// client.ContainerCreate.
type SpawnConfig struct {
	// Image is the agent container image tag (e.g. "picoclaw-agent:latest").
	Image string

	// ChatFolder is the chat identifier; folds into the container name,
	// the picoclaw.chat label, and nothing else.
	ChatFolder string

	// Version is the picoclaw build version string. Recorded as a
	// container label so boot-time cleanup can evict leftover containers
	// spawned by an older picoclaw.
	Version string

	// Mounts is the bind-mount list from BuildMounts. Converted to
	// mount.Mount at spawn time; tmpfs mounts are added separately as
	// always-on infrastructure per ISOLATION.md §6.1.
	Mounts []Mount

	// UID and GID are the host user the container process runs as.
	// Both must be non-negative. Setting them to 0 (root) is rejected;
	// ISOLATION.md §6.1 requires a non-root uid for bind-mount write
	// sanity and for no-new-privileges to have meaning.
	UID int
	GID int

	// MemoryBytes is the hard RAM cap. 0 means "unset" and is an error.
	// Typical production value: 4 << 30 (4 GiB).
	MemoryBytes int64

	// NanoCPUs is the CPU cap in nanocores (1 full CPU = 1e9).
	// 0 means "unset" and is an error. Typical value: 2e9 (2 CPUs).
	NanoCPUs int64

	// PidsLimit is the max number of processes/threads the container
	// is allowed to spawn. 0 means "unset" and is an error.
	PidsLimit int64

	// EnvAllowlist is the list of environment variable names that
	// should be forwarded from the picoclaw process into the agent
	// container. Values are read from os.Environ() at spawn time; only
	// names, never values, appear in this struct (per D012). Unknown
	// names are silently dropped — it is not an error to allow a var
	// that isn't set.
	EnvAllowlist []string
}

// Validate enforces the non-optional fields of SpawnConfig. Called
// automatically by BuildContainerArgs; exposed for tests.
func (c SpawnConfig) Validate() error {
	if c.Image == "" {
		return errors.New("SpawnConfig: Image is empty")
	}
	if c.ChatFolder == "" {
		return errors.New("SpawnConfig: ChatFolder is empty")
	}
	if c.Version == "" {
		return errors.New("SpawnConfig: Version is empty")
	}
	if c.UID == 0 && c.GID == 0 {
		return errors.New("SpawnConfig: refusing uid=0 gid=0 (container must run as non-root)")
	}
	if c.UID < 0 || c.GID < 0 {
		return fmt.Errorf("SpawnConfig: UID=%d GID=%d must be non-negative", c.UID, c.GID)
	}
	if c.MemoryBytes <= 0 {
		return fmt.Errorf("SpawnConfig: MemoryBytes=%d must be positive", c.MemoryBytes)
	}
	if c.NanoCPUs <= 0 {
		return fmt.Errorf("SpawnConfig: NanoCPUs=%d must be positive", c.NanoCPUs)
	}
	if c.PidsLimit <= 0 {
		return fmt.Errorf("SpawnConfig: PidsLimit=%d must be positive", c.PidsLimit)
	}
	return nil
}

// ContainerName returns the deterministic container name picoclaw uses
// for a chat. Matches the `--name` flag in ISOLATION.md §6.1.
func ContainerName(chatFolder string) string {
	return "picoclaw-" + chatFolder
}

// BuildContainerArgs is the single place where picoclaw turns a
// validated SpawnConfig into the exact (Config, HostConfig, name)
// triple that goes into client.ContainerCreate. Every flag from
// docs/ISOLATION.md §6 lives here.
//
// Pure data transformation — no Docker client calls, no fs access.
// Unit-testable by comparing the returned structs against a fixture.
func BuildContainerArgs(cfg SpawnConfig) (*container.Config, *container.HostConfig, string, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, "", err
	}

	// -- container.Config -------------------------------------------------

	// HOME must be explicitly set when --user overrides the image's
	// default user: Docker does not adjust HOME automatically, so a
	// `--user 501:20` invocation inherits HOME from the image (which
	// for node:22-slim with no USER directive is "/", the root
	// directory). Claude Code then tries to write its config to
	// `/.claude.json` on the read-only rootfs and silently fails.
	// Forcing HOME=/home/node points it at the writable tmpfs we set
	// up in the HostConfig below.
	env := append(buildEnvSlice(cfg.EnvAllowlist), "HOME=/home/node")

	conf := &container.Config{
		Image:      cfg.Image,
		User:       fmt.Sprintf("%d:%d", cfg.UID, cfg.GID),
		WorkingDir: "/workspace/chat",
		// Long-lived container: picoclaw owns the agent loop via
		// `docker exec`, and the container's foreground process just
		// waits forever until we stop it. The actual sleep loop lives
		// in the image's entrypoint (M5e); Cmd is left empty here so
		// the image default wins.
		Labels: map[string]string{
			"picoclaw.chat":    cfg.ChatFolder,
			"picoclaw.version": cfg.Version,
		},
		Env:             env,
		AttachStdin:     false,
		AttachStdout:    false,
		AttachStderr:    false,
		OpenStdin:       false,
		NetworkDisabled: false, // outbound network is required (Claude API)
	}

	// -- container.HostConfig ---------------------------------------------

	pidsLimit := cfg.PidsLimit
	host := &container.HostConfig{
		// RO root filesystem; every writable path is either a mount
		// from BuildMounts or one of the tmpfs mounts below.
		ReadonlyRootfs: true,

		// Drop every Linux capability. If a future skill legitimately
		// needs one back, that's an ADR not a code patch (see
		// ISOLATION.md §6.4).
		CapDrop: strslice.StrSlice{"ALL"},

		// no-new-privileges blocks setuid/setgid escalation even if
		// the image contains such a binary.
		//
		// Note on seccomp: the Docker daemon applies its default
		// seccomp profile automatically when no `seccomp=` SecurityOpt
		// is set — that is the documented behaviour and what we want.
		// We do NOT pass `seccomp=default` explicitly because the
		// daemon parses the SecurityOpt VALUE as either inline JSON
		// or a JSON file path; the literal string "default" is
		// neither and the spawn fails with
		// `Decoding seccomp profile failed: invalid character 'd'`.
		// The CLI flag `--security-opt seccomp=default` works
		// because the docker CLI translates "default" to "no value"
		// before sending to the daemon; the SDK does not.
		SecurityOpt: []string{
			"no-new-privileges:true",
		},

		// picoclaw decides restart policy, not Docker. Crashes should
		// be visible; auto-restart would hide them.
		RestartPolicy: container.RestartPolicy{Name: "no"},

		// Resource caps.
		Resources: container.Resources{
			Memory:    cfg.MemoryBytes,
			NanoCPUs:  cfg.NanoCPUs,
			PidsLimit: &pidsLimit,
		},

		// Tmpfs mounts. ISOLATION.md §6.1 names three; we add one
		// more (/home/node) once end-to-end testing of M6d showed
		// that Claude Code needs a writable HOME to store its
		// per-instance state (`$HOME/.claude.json` plus npm/node
		// scratch). The /home/node tmpfs sits below the
		// /home/node/.claude bind mount in the mount tree — Docker
		// applies mounts in target-depth order so the bind mount
		// nests on top of the tmpfs, giving us:
		//
		//   /home/node                 → tmpfs (writable, ephemeral)
		//   /home/node/.claude         → bind  (writable, persistent)
		//   /home/node/.claude/skills  → bind  (read-only, persistent)
		//
		// mode=1777 is the sticky-write-all mode; without it, the
		// default tmpfs mode is 1755 and a non-root uid override
		// (`--user 501:20` etc) cannot write into it.
		//
		// /home/node/.cache is intentionally NOT a separate tmpfs
		// any more — it falls through to the parent /home/node
		// tmpfs naturally.
		Tmpfs: map[string]string{
			"/tmp":       "rw,size=512m,mode=1777",
			"/home/node": "rw,size=256m,mode=1777",
			"/run":       "rw,size=64m,mode=1777",
		},

		// Bind mounts from BuildMounts.
		Mounts: toDockerMounts(cfg.Mounts),
	}

	return conf, host, ContainerName(cfg.ChatFolder), nil
}

// toDockerMounts converts picoclaw's internal Mount slice to the Docker
// SDK's mount.Mount slice.
func toDockerMounts(in []Mount) []mount.Mount {
	out := make([]mount.Mount, len(in))
	for i, m := range in {
		out[i] = mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		}
	}
	return out
}

// buildEnvSlice reads each allow-listed environment variable from
// os.Environ() and returns the KEY=VALUE lines Docker's Env slice
// expects. Variables that are not set on the host are silently
// dropped — it is not an error for a chat to allow a variable that
// doesn't exist, just a no-op.
//
// Per [D012](../../docs/DECISIONS.md) this is the only place picoclaw
// reads secret-bearing environment variables; the values never hit the
// logs, never get serialized to disk, and never live in memory outside
// this Env slice.
func buildEnvSlice(allow []string) []string {
	out := make([]string, 0, len(allow))
	for _, name := range allow {
		if name == "" {
			continue
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		out = append(out, name+"="+v)
	}
	return out
}

// ParseMemoryBytes parses a picoclaw memory cap string like "4g" or
// "512m" into bytes. Units are case-insensitive: k=KiB, m=MiB, g=GiB.
// Bare integers are treated as bytes. Returned for reuse from config
// loading in M5f.
func ParseMemoryBytes(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty memory value")
	}
	mul := int64(1)
	switch s[len(s)-1] {
	case 'k':
		mul = 1 << 10
		s = s[:len(s)-1]
	case 'm':
		mul = 1 << 20
		s = s[:len(s)-1]
	case 'g':
		mul = 1 << 30
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse memory: %w", err)
	}
	if n < 0 {
		return 0, fmt.Errorf("memory %d: must be non-negative", n)
	}
	return n * mul, nil
}
