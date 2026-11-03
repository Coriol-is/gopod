package config

import "testing"

func TestElevatedConfigurationFailsClosed(t *testing.T) {
	t.Setenv("GOPOD_ELEVATED", "1")
	if err := loadElevated(&Config{ContainerEnabled: true}); err == nil {
		t.Fatal("missing owner accepted")
	}
	c := Config{OwnerChatID: 123, ContainerEnabled: true}
	if err := loadElevated(&c); err != nil {
		t.Fatal(err)
	}
	if c.ElevatedMemory != 1<<30 || c.ElevatedCPUs != 1e9 || c.ElevatedPids != 256 {
		t.Fatal(c.ElevatedMemory, c.ElevatedCPUs, c.ElevatedPids)
	}
	t.Setenv("GOPOD_ELEVATED_CPUS", "NaN")
	if loadElevated(&c) == nil {
		t.Fatal("NaN accepted")
	}
}

func TestElevatedFlagDefaultsOff(t *testing.T) {
	for _, value := range []string{"", "0", "false"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GOPOD_ELEVATED", value)
			c := Config{}
			if err := loadElevated(&c); err != nil {
				t.Fatal(err)
			}
			if c.ElevatedEnabled {
				t.Fatal("elevated enabled without opt-in")
			}
		})
	}
}
