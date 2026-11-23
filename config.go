package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config describes what phone mode changes. Every behavior has a manage_*
// switch so that disabling one leaves that setting untouched.
type Config struct {
	KeepServices []string `toml:"keep_services"`
	KeepRadiosOn []string `toml:"keep_radios_on"`

	ManageBacklight bool   `toml:"manage_backlight"`
	BacklightDevice string `toml:"backlight_device"`
	Brightness      int    `toml:"brightness"`

	ManageGovernor bool   `toml:"manage_governor"`
	Governor       string `toml:"governor"`

	ManageMaxFreq     bool `toml:"manage_max_freq"`
	CPUMaxFreqPercent int  `toml:"cpu_max_freq_percent"`

	ManageTurbo bool `toml:"manage_turbo"`
	Turbo       bool `toml:"turbo"`

	Powertop    bool `toml:"powertop"`
	DisableDGPU bool `toml:"disable_dgpu"`

	ManageLid bool   `toml:"manage_lid"`
	LidSwitch string `toml:"lid_switch"`

	ManageRadios   bool `toml:"manage_radios"`
	ManageServices bool `toml:"manage_services"`
	IsolateTarget  bool `toml:"isolate_target"`

	BatteryExitPercent int `toml:"battery_exit_percent"`

	// Restore is only used when off runs without a state file: exact values
	// are unknown, so these are the fallback desktop values.
	Restore RestoreConfig `toml:"restore"`
}

type RestoreConfig struct {
	Brightness        int    `toml:"brightness"`
	Governor          string `toml:"governor"` // empty leaves the governor alone
	CPUMaxFreqPercent int    `toml:"cpu_max_freq_percent"`
	Turbo             bool   `toml:"turbo"`
	LidSwitch         string `toml:"lid_switch"`
	Bluetooth         bool   `toml:"bluetooth"`
	ServicesRunning   bool   `toml:"services_running"`
}

func DefaultConfig() Config {
	return Config{
		KeepServices: []string{"opencode-serve.service", "opencode-telegram.service"},
		KeepRadiosOn: []string{"wlan"},

		ManageBacklight: true,
		Brightness:      0,

		ManageGovernor: true,
		Governor:       "powersave",

		ManageMaxFreq:     true,
		CPUMaxFreqPercent: 40,

		ManageTurbo: true,
		Turbo:       false,

		Powertop:    true,
		DisableDGPU: true,

		ManageLid: true,
		LidSwitch: "ignore",

		ManageRadios:   true,
		ManageServices: true,
		IsolateTarget:  true,

		BatteryExitPercent: 15,

		Restore: RestoreConfig{
			Brightness:        100,
			Governor:          "",
			CPUMaxFreqPercent: 100,
			Turbo:             true,
			LidSwitch:         "suspend",
			Bluetooth:         true,
			ServicesRunning:   true,
		},
	}
}

var lidActions = map[string]bool{
	"ignore":                 true,
	"poweroff":               true,
	"reboot":                 true,
	"halt":                   true,
	"kexec":                  true,
	"suspend":                true,
	"hibernate":              true,
	"hybrid-sleep":           true,
	"suspend-then-hibernate": true,
	"lock":                   true,
}

func (c Config) Validate() error {
	if c.ManageBacklight {
		if c.Brightness < -1 || c.Brightness > 100 {
			return fmt.Errorf("brightness must be between -1 and 100, got %d", c.Brightness)
		}
		if c.BacklightDevice != "" && filepath.Base(c.BacklightDevice) != c.BacklightDevice {
			return fmt.Errorf("backlight_device must be a device name, got %q", c.BacklightDevice)
		}
	}
	if c.ManageGovernor && strings.TrimSpace(c.Governor) == "" {
		return errors.New("governor must not be empty when manage_governor is true")
	}
	if c.ManageMaxFreq && (c.CPUMaxFreqPercent < 1 || c.CPUMaxFreqPercent > 100) {
		return fmt.Errorf("cpu_max_freq_percent must be between 1 and 100, got %d", c.CPUMaxFreqPercent)
	}
	if c.ManageLid && !lidActions[c.LidSwitch] {
		return fmt.Errorf("lid_switch %q is not a systemd-logind action", c.LidSwitch)
	}
	if c.BatteryExitPercent < 0 || c.BatteryExitPercent > 100 {
		return fmt.Errorf("battery_exit_percent must be between 0 and 100, got %d", c.BatteryExitPercent)
	}

	if c.Restore.Brightness < 0 || c.Restore.Brightness > 100 {
		return fmt.Errorf("restore.brightness must be between 0 and 100, got %d", c.Restore.Brightness)
	}
	if c.Restore.Governor != "" && strings.TrimSpace(c.Restore.Governor) == "" {
		return errors.New("restore.governor must be empty or a governor name")
	}
	if c.Restore.CPUMaxFreqPercent < 1 || c.Restore.CPUMaxFreqPercent > 100 {
		return fmt.Errorf("restore.cpu_max_freq_percent must be between 1 and 100, got %d", c.Restore.CPUMaxFreqPercent)
	}
	if c.Restore.LidSwitch != "" && !lidActions[c.Restore.LidSwitch] {
		return fmt.Errorf("restore.lid_switch %q is not a systemd-logind action", c.Restore.LidSwitch)
	}
	return nil
}

// LoadConfig starts from the defaults and overlays the file, so omitted keys
// keep their documented default. Unknown keys are rejected to catch typos.
func LoadConfig(path string) (Config, bool, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, true, fmt.Errorf("parse config %s: %w", path, err)
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		keys := make([]string, len(unknown))
		for i, key := range unknown {
			keys[i] = key.String()
		}
		return Config{}, true, fmt.Errorf("unknown config keys in %s: %s", path, strings.Join(keys, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, true, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, true, nil
}

func WriteDefaultConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(exampleConfig), 0o644)
}

//go:embed config.example.toml
var exampleConfig string
