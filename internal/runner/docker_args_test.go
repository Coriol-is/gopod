package runner

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func goodSpawnConfig() SpawnConfig {
	return SpawnConfig{
		Image:       "gopod-agent:latest",
		ChatFolder:  "alice",
		Version:     "abc1234",
		UID:         501,
		GID:         20,
		MemoryBytes: 4 << 30,
		NanoCPUs:    2_000_000_000,
		PidsLimit:   1024,
	}
}

func TestValidateSpawnConfig(t *testing.T) {
	cfg := goodSpawnConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("good cfg: %v", err)
	}

	bad := cfg
	bad.Image = ""
	if err := bad.Validate(); err == nil {
		t.Error("empty image: want error")
	}

	bad = cfg
	bad.UID = 0
	bad.GID = 0
	if err := bad.Validate(); err == nil {
		t.Error("uid=gid=0: want error (root refused)")
	}

	bad = cfg
	bad.MemoryBytes = 0
	if err := bad.Validate(); err == nil {
		t.Error("zero memory: want error")
	}

	bad = cfg
	bad.PidsLimit = 0
	if err := bad.Validate(); err == nil {
		t.Error("zero pids: want error")
	}
}

func TestBuildContainerArgsFlags(t *testing.T) {
	cfg := goodSpawnConfig()
	cfg.Mounts = []Mount{
		{Source: "/tmp/chat", Target: "/workspace/chat", ReadOnly: false},
		{Source: "/tmp/skills", Target: "/home/node/.claude/skills", ReadOnly: true},
	}

	conf, host, name, err := BuildContainerArgs(cfg)
	if err != nil {
		t.Fatalf("BuildContainerArgs: %v", err)
	}

	if name != "gopod-alice" {
		t.Errorf("name = %q, want gopod-alice", name)
	}
	if conf.Image != "gopod-agent:latest" {
		t.Errorf("image = %q", conf.Image)
	}
	if conf.User != "501:20" {
		t.Errorf("user = %q, want 501:20", conf.User)
	}
	if conf.WorkingDir != "/workspace/chat" {
		t.Errorf("workdir = %q", conf.WorkingDir)
	}
	if conf.Labels["gopod.chat"] != "alice" {
		t.Errorf("chat label = %q", conf.Labels["gopod.chat"])
	}
	if conf.Labels["gopod.version"] != "abc1234" {
		t.Errorf("version label = %q", conf.Labels["gopod.version"])
	}

	if !host.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = false, want true")
	}
	if len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", host.CapDrop)
	}
	if !hasString(host.SecurityOpt, "no-new-privileges:true") {
		t.Errorf("SecurityOpt missing no-new-privileges: %v", host.SecurityOpt)
	}
	// seccomp is NOT set explicitly — the daemon's default profile
	// applies automatically when SecurityOpt has no `seccomp=` entry.
	// Setting it explicitly to "default" via the SDK is broken
	// because the daemon JSON-parses the value (the CLI accepts
	// "default" as a keyword; the SDK does not).
	for _, s := range host.SecurityOpt {
		if strings.HasPrefix(s, "seccomp=") {
			t.Errorf("SecurityOpt unexpectedly carries seccomp entry %q (default profile applies on its own)", s)
		}
	}
	if host.RestartPolicy.Name != "no" {
		t.Errorf("RestartPolicy = %q, want no", host.RestartPolicy.Name)
	}
	if host.Resources.Memory != 4<<30 {
		t.Errorf("Memory = %d, want %d", host.Resources.Memory, int64(4<<30))
	}
	if host.Resources.NanoCPUs != 2_000_000_000 {
		t.Errorf("NanoCPUs = %d, want 2e9", host.Resources.NanoCPUs)
	}
	if host.Resources.PidsLimit == nil || *host.Resources.PidsLimit != 1024 {
		t.Errorf("PidsLimit = %v, want 1024", host.Resources.PidsLimit)
	}

	// Tmpfs targets: /tmp, /home/node, /run. /home/node MUST be present
	// or `--user <uid>` containers can't write `$HOME/.claude.json` and
	// claude silently exits with empty stdout (regression hunted by
	// this test after the M6d end-to-end test surfaced it).
	for _, target := range []string{"/tmp", "/home/node", "/run"} {
		spec, ok := host.Tmpfs[target]
		if !ok {
			t.Errorf("missing tmpfs %q", target)
			continue
		}
		if !strings.Contains(spec, "mode=1777") {
			t.Errorf("tmpfs %q missing mode=1777 (got %q): non-root uid override needs world-writable tmpfs", target, spec)
		}
	}

	// HOME must be set in Env so the --user override doesn't inherit
	// HOME=/ from the image. This is the matching half of the tmpfs
	// fix above.
	if !hasString(conf.Env, "HOME=/home/node") {
		t.Errorf("Env missing HOME=/home/node: %v", conf.Env)
	}

	// Mount conversion.
	if len(host.Mounts) != 2 {
		t.Fatalf("len(host.Mounts) = %d, want 2", len(host.Mounts))
	}
	if host.Mounts[0].Type != mount.TypeBind {
		t.Errorf("mount type = %q, want bind", host.Mounts[0].Type)
	}
	if host.Mounts[0].Source != "/tmp/chat" || host.Mounts[0].Target != "/workspace/chat" {
		t.Errorf("first mount: %+v", host.Mounts[0])
	}
	if !host.Mounts[1].ReadOnly {
		t.Error("second mount (skills) should be ReadOnly")
	}
}

func TestBuildContainerArgsEnvAllowlist(t *testing.T) {
	t.Setenv("GOPOD_TEST_PRESENT", "value-here")
	// leave GOPOD_TEST_ABSENT unset

	cfg := goodSpawnConfig()
	cfg.EnvAllowlist = []string{
		"GOPOD_TEST_PRESENT",
		"GOPOD_TEST_ABSENT",
		"", // empty entry must be skipped
	}
	conf, _, _, err := BuildContainerArgs(cfg)
	if err != nil {
		t.Fatalf("BuildContainerArgs: %v", err)
	}
	if !hasString(conf.Env, "GOPOD_TEST_PRESENT=value-here") {
		t.Errorf("Env missing PRESENT: %v", conf.Env)
	}
	for _, e := range conf.Env {
		if e == "GOPOD_TEST_ABSENT=" {
			t.Error("Env should NOT include absent var with empty value")
		}
	}
}

func TestParseMemoryBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		bad  bool
	}{
		{"1024", 1024, false},
		{"1k", 1 << 10, false},
		{"4G", 4 << 30, false},
		{"512m", 512 << 20, false},
		{"2g", 2 << 30, false},
		{" 2g ", 2 << 30, false},
		{"", 0, true},
		{"-1", 0, true},
		{"four", 0, true},
	}
	for _, c := range cases {
		got, err := ParseMemoryBytes(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseMemoryBytes(%q) want error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMemoryBytes(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMemoryBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestContainerName(t *testing.T) {
	if got := ContainerName("alice"); got != "gopod-alice" {
		t.Errorf("got %q", got)
	}
}

func hasString(xs []string, want string) bool {
	for _, s := range xs {
		if s == want {
			return true
		}
	}
	return false
}
