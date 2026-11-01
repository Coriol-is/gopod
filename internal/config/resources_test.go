package config

import "testing"

func TestContainerResources(t *testing.T) {
	t.Setenv("GOPOD_CONTAINER_MEM", "1024m")
	t.Setenv("GOPOD_CONTAINER_CPUS", "1.5")
	t.Setenv("GOPOD_CONTAINER_PIDS", "256")
	var c Config
	if err := loadContainerResources(&c); err != nil {
		t.Fatal(err)
	}
	if c.ContainerMemoryBytes != 1<<30 || c.ContainerNanoCPUs != 1500000000 || c.ContainerPidsLimit != 256 {
		t.Fatalf("limits: %+v", c)
	}
	for _, tc := range []struct{ k, v string }{{"GOPOD_CONTAINER_MEM", "999999999999g"}, {"GOPOD_CONTAINER_MEM", "0"}, {"GOPOD_CONTAINER_CPUS", "NaN"}, {"GOPOD_CONTAINER_CPUS", "-1"}, {"GOPOD_CONTAINER_PIDS", "0"}} {
		t.Run(tc.k+tc.v, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if loadContainerResources(&c) == nil {
				t.Fatal("invalid limit accepted")
			}
		})
	}
}
