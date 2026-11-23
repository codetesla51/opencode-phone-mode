package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := `
brightness = 25
keep_services = ["agent.service"]
[restore]
brightness = 80
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, exists, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !exists {
		t.Fatal("exists = false, want true")
	}
	if cfg.Brightness != 25 {
		t.Errorf("Brightness = %d, want 25", cfg.Brightness)
	}
	if cfg.Restore.Brightness != 80 {
		t.Errorf("Restore.Brightness = %d, want 80", cfg.Restore.Brightness)
	}
	if cfg.Governor != "powersave" {
		t.Errorf("Governor = %q, want default powersave", cfg.Governor)
	}
	if len(cfg.KeepServices) != 1 || cfg.KeepServices[0] != "agent.service" {
		t.Errorf("KeepServices = %v, want [agent.service]", cfg.KeepServices)
	}
}

func TestConfigMissingReturnsDefaults(t *testing.T) {
	cfg, exists, err := LoadConfig(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if exists {
		t.Fatal("exists = true, want false")
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Errorf("config differs from defaults:\n got %+v\nwant %+v", cfg, DefaultConfig())
	}
}

func TestConfigExampleMatchesDefaults(t *testing.T) {
	cfg, exists, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatalf("LoadConfig example: %v", err)
	}
	if !exists {
		t.Fatal("example config not found")
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Errorf("config.example.toml drifted from DefaultConfig:\n got %+v\nwant %+v", cfg, DefaultConfig())
	}
}

func TestConfigRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"brightness too high", "brightness = 101", "brightness"},
		{"brightness below sentinel", "brightness = -2", "brightness"},
		{"max freq zero", "cpu_max_freq_percent = 0", "cpu_max_freq_percent"},
		{"max freq high", "cpu_max_freq_percent = 101", "cpu_max_freq_percent"},
		{"bad lid action", `lid_switch = "explode"`, "lid_switch"},
		{"bad restore lid", "[restore]\nlid_switch = \"explode\"", "restore.lid_switch"},
		{"negative battery threshold", "battery_exit_percent = -1", "battery_exit_percent"},
		{"unknown key", "brightnes = 10", "brightnes"},
		{"path in device", `backlight_device = "../evil"`, "backlight_device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tt.data), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := LoadConfig(path)
			if err == nil {
				t.Fatal("LoadConfig succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestConfigAllowsDisablingManagementWithoutValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := `
manage_governor = false
governor = ""
manage_lid = false
lid_switch = ""
manage_backlight = false
brightness = -1
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(path); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
}
