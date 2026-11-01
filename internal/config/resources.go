package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func loadContainerResources(c *Config) error {
	raw := strings.ToLower(strings.TrimSpace(getenvDefault("GOPOD_CONTAINER_MEM", "4g")))
	multiplier := int64(1)
	if len(raw) > 0 {
		switch raw[len(raw)-1] {
		case 'k':
			multiplier = 1 << 10
			raw = raw[:len(raw)-1]
		case 'm':
			multiplier = 1 << 20
			raw = raw[:len(raw)-1]
		case 'g':
			multiplier = 1 << 30
			raw = raw[:len(raw)-1]
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/multiplier {
		return fmt.Errorf("GOPOD_CONTAINER_MEM must be positive bytes or k/m/g without overflow")
	}
	c.ContainerMemoryBytes = n * multiplier
	cpus, err := strconv.ParseFloat(getenvDefault("GOPOD_CONTAINER_CPUS", "2"), 64)
	if err != nil || math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus < 0.001 || cpus > 1000 {
		return fmt.Errorf("GOPOD_CONTAINER_CPUS must be between 0.001 and 1000")
	}
	c.ContainerNanoCPUs = int64(cpus * 1e9)
	pids, err := envInt("GOPOD_CONTAINER_PIDS", 1024)
	if err != nil || pids <= 0 {
		return fmt.Errorf("GOPOD_CONTAINER_PIDS must be a positive integer")
	}
	c.ContainerPidsLimit = int64(pids)
	return nil
}
