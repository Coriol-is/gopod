package config

import (
	"fmt"
	"math"
	"strconv"
)

func loadElevated(c *Config) error {
	c.ElevatedEnabled = envFlag("GOPOD_ELEVATED")
	c.ElevatedImage = getenvDefault("GOPOD_ELEVATED_IMAGE", "gopod-agent-elevated:latest")
	c.ElevatedCodexImage = getenvDefault("GOPOD_ELEVATED_IMAGE_CODEX", "gopod-agent-elevated-codex:latest")
	mem, err := strconv.ParseInt(getenvDefault("GOPOD_ELEVATED_MEM", "1073741824"), 10, 64)
	if err != nil || mem <= 0 {
		return fmt.Errorf("GOPOD_ELEVATED_MEM must be positive bytes")
	}
	c.ElevatedMemory = mem
	cpus, err := strconv.ParseFloat(getenvDefault("GOPOD_ELEVATED_CPUS", "1"), 64)
	if err != nil || math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus < 0.001 || cpus > 1000 {
		return fmt.Errorf("invalid GOPOD_ELEVATED_CPUS")
	}
	c.ElevatedCPUs = int64(cpus * 1e9)
	pids, err := strconv.ParseInt(getenvDefault("GOPOD_ELEVATED_PIDS", "256"), 10, 64)
	if err != nil || pids <= 0 {
		return fmt.Errorf("GOPOD_ELEVATED_PIDS must be positive")
	}
	c.ElevatedPids = pids
	if c.ElevatedEnabled && (c.OwnerChatID == 0 || !c.ContainerEnabled) {
		return fmt.Errorf("GOPOD_ELEVATED requires owner private chat and Docker enabled")
	}
	return nil
}
