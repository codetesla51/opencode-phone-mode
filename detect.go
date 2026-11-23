package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var lidKeys = []string{
	"HandleLidSwitch",
	"HandleLidSwitchExternalPower",
	"HandleLidSwitchDocked",
}

var lidDefaults = map[string]string{
	"HandleLidSwitch":              "suspend",
	"HandleLidSwitchExternalPower": "suspend",
	"HandleLidSwitchDocked":        "ignore",
}

type radio struct {
	ID          int
	Name        string
	Type        string
	SoftBlocked bool
}

func listRadios(h host) ([]radio, error) {
	out, err := h.Run("rfkill", "-J")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Devices []struct {
			ID     int        `json:"id"`
			Type   string     `json:"type"`
			Device string     `json:"device"`
			Soft   rfkillFlag `json:"soft"`
		} `json:"rfkilldevices"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("parse rfkill json: %w", err)
	}
	radios := make([]radio, 0, len(parsed.Devices))
	for _, d := range parsed.Devices {
		radios = append(radios, radio{
			ID:          d.ID,
			Name:        d.Device,
			Type:        strings.ToLower(d.Type),
			SoftBlocked: bool(d.Soft),
		})
	}
	return radios, nil
}

// rfkillSelector is a command-line identifier this rfkill version accepts.
// Device names like hci0 are rejected; numeric IDs and types work.
func rfkillSelector(r radio) string {
	if r.ID >= 0 {
		return strconv.Itoa(r.ID)
	}
	return r.Type
}

// matchRadio finds the current device matching a saved radio. Names can change
// across reboots, so the type is the fallback.
func matchRadio(current []radio, saved RadioState) *radio {
	for i := range current {
		if current[i].Name == saved.Name && current[i].Type == saved.Type {
			return &current[i]
		}
	}
	for i := range current {
		if current[i].Type == saved.Type {
			return &current[i]
		}
	}
	return nil
}

// rfkillFlag accepts both the string form ("blocked"/"unblocked") used by
// util-linux and the boolean form older releases emit.
type rfkillFlag bool

func (f *rfkillFlag) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case `"blocked"`:
		*f = true
		return nil
	case `"unblocked"`:
		*f = false
		return nil
	}
	var v bool
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("unexpected rfkill soft value %s", data)
	}
	*f = rfkillFlag(v)
	return nil
}

func radioKept(cfg Config, typ string) bool {
	for _, kept := range cfg.KeepRadiosOn {
		kept = strings.ToLower(strings.TrimSpace(kept))
		if kept == "wifi" {
			kept = "wlan"
		}
		if kept == typ {
			return true
		}
	}
	return false
}

func batteryState(h host) (pct int, status string, ok bool) {
	paths, err := h.Glob("/sys/class/power_supply/BAT*")
	if err != nil || len(paths) == 0 {
		return 0, "", false
	}
	sort.Strings(paths)
	raw, err := h.Read(paths[0] + "/capacity")
	if err != nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, "", false
	}
	status, _ = h.Read(paths[0] + "/status")
	return n, strings.TrimSpace(status), true
}

// batteryBelowExit reports whether going headless would risk losing remote
// access. Charging and full batteries are never treated as too low.
func batteryBelowExit(threshold, pct int, status string) bool {
	if threshold <= 0 || pct <= 0 || pct >= threshold {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "charging", "full":
		return false
	}
	return true
}

func effectiveLidActions(h host) map[string]string {
	actions := make(map[string]string, len(lidKeys))
	for _, key := range lidKeys {
		actions[key] = lidDefaults[key]
	}
	out, err := h.Run("systemd-analyze", "cat-config", "systemd/logind.conf")
	if err != nil {
		return actions
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if _, ok := actions[key]; ok {
			actions[key] = strings.TrimSpace(value)
		}
	}
	return actions
}

func lidSummary(actions map[string]string) string {
	return fmt.Sprintf("lid=%s external=%s docked=%s",
		valueOr(actions["HandleLidSwitch"]),
		valueOr(actions["HandleLidSwitchExternalPower"]),
		valueOr(actions["HandleLidSwitchDocked"]))
}

func valueOr(value string) string {
	if value == "" {
		return "?"
	}
	return value
}

func lidDropIn(value string) string {
	return fmt.Sprintf("[Login]\nHandleLidSwitch=%s\nHandleLidSwitchExternalPower=%s\nHandleLidSwitchDocked=%s\n",
		value, value, value)
}

func cpufreqPolicies(h host) ([]string, error) {
	dirs, err := h.Glob("/sys/devices/system/cpu/cpufreq/policy*")
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, errors.New("no cpufreq policies found")
	}
	sort.Strings(dirs)
	return dirs, nil
}

func readUint(h host, path string) (uint64, error) {
	raw, err := h.Read(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return value, nil
}

func readUintOr(h host, path string, fallback uint64) uint64 {
	value, err := readUint(h, path)
	if err != nil {
		return fallback
	}
	return value
}

func capFrequency(max, min uint64, pct int) uint64 {
	want := max * uint64(pct) / 100
	if want < min {
		want = min
	}
	if want > max {
		want = max
	}
	return want
}

// turboPath returns the sysfs file for turbo control and whether it uses the
// intel_pstate inverted value (0 = turbo on).
func turboPath(h host) (string, bool, error) {
	const intel = "/sys/devices/system/cpu/intel_pstate/no_turbo"
	if _, err := h.Read(intel); err == nil {
		return intel, true, nil
	}
	const boost = "/sys/devices/system/cpu/cpufreq/boost"
	if _, err := h.Read(boost); err == nil {
		return boost, false, nil
	}
	return "", false, errors.New("no turbo control file found")
}

func turboValue(enabled, inverted bool) string {
	on, off := "1", "0"
	if inverted {
		on, off = "0", "1"
	}
	if enabled {
		return on
	}
	return off
}

func turboEnabled(raw string, inverted bool) bool {
	if inverted {
		return strings.TrimSpace(raw) == "0"
	}
	return strings.TrimSpace(raw) == "1"
}

// discreteGPUs lists non-boot display controllers. boot_vga marks the GPU
// that owns the console, so it is never powered down.
func discreteGPUs(h host) ([]string, error) {
	dirs, err := h.Glob("/sys/bus/pci/devices/*")
	if err != nil {
		return nil, err
	}
	var gpus []string
	for _, dir := range dirs {
		class, err := h.Read(dir + "/class")
		if err != nil || !strings.HasPrefix(strings.TrimSpace(class), "0x03") {
			continue
		}
		if boot, err := h.Read(dir + "/boot_vga"); err == nil && strings.TrimSpace(boot) == "1" {
			continue
		}
		gpus = append(gpus, dir)
	}
	sort.Strings(gpus)
	return gpus, nil
}

func userUnitState(h host, name string) (active bool, enabled string, err error) {
	out, err := h.Run("systemctl", "--user", "show", "-p", "ActiveState", "--value", "--", name)
	if err != nil {
		return false, "", err
	}
	active = strings.TrimSpace(out) == "active"

	out, err = h.Run("systemctl", "--user", "show", "-p", "UnitFileState", "--value", "--", name)
	if err != nil {
		return false, "", err
	}
	return active, strings.TrimSpace(out), nil
}

func systemUnitActive(h host, name string) (bool, error) {
	out, err := h.Run("systemctl", "show", "-p", "ActiveState", "--value", "--", name)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "active", nil
}

func lingerEnabled(h host, userName string) (bool, error) {
	out, err := h.Run("loginctl", "show-user", userName, "-p", "Linger", "--value")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "yes", nil
}
