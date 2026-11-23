package main

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

type fakeHost struct {
	files     map[string]string
	globs     map[string][]string
	outputs   map[string]string
	mtimes    map[string]time.Time
	runErrs   map[string]error
	writeErrs map[string]error
	removeErr map[string]error
	look      map[string]bool
	writes    []string
	removed   []string
	runs      []string
	primed    bool
	primeErr  error
	userName  string
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		files: map[string]string{
			"/sys/class/power_supply/BAT0/capacity":                    "80\n",
			"/sys/class/power_supply/BAT0/status":                      "Discharging\n",
			"/sys/class/backlight/intel_backlight/max_brightness":      "1000\n",
			"/sys/class/backlight/intel_backlight/brightness":          "500\n",
			"/sys/devices/system/cpu/cpufreq/policy0/scaling_governor": "powersave\n",
			"/sys/devices/system/cpu/cpufreq/policy0/scaling_min_freq": "400000\n",
			"/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq": "2000000\n",
			"/sys/devices/system/cpu/cpufreq/policy0/cpuinfo_max_freq": "2000000\n",
			"/sys/devices/system/cpu/intel_pstate/no_turbo":            "0\n",
		},
		globs: map[string][]string{
			"/sys/class/power_supply/BAT*":            {"/sys/class/power_supply/BAT0"},
			"/sys/class/backlight/*":                  {"/sys/class/backlight/intel_backlight"},
			"/sys/devices/system/cpu/cpufreq/policy*": {"/sys/devices/system/cpu/cpufreq/policy0"},
			"/sys/bus/pci/devices/*":                  {},
		},
		outputs: map[string]string{
			"systemd-analyze cat-config systemd/logind.conf": "#HandleLidSwitch=suspend\n[Login]\n",
			"rfkill -J": `{"rfkilldevices":[
				{"id":0,"type":"bluetooth","device":"hci0","soft":"unblocked","hard":"unblocked"},
				{"id":1,"type":"wlan","device":"phy0","soft":"unblocked","hard":"unblocked"}
			]}`,
			"systemctl show -p ActiveState --value -- graphical.target":                   "active",
			"systemctl show -p ActiveState --value -- bluetooth.service":                  "active",
			"systemctl --user show -p ActiveState --value -- opencode-serve.service":      "active",
			"systemctl --user show -p UnitFileState --value -- opencode-serve.service":    "enabled",
			"systemctl --user show -p ActiveState --value -- opencode-telegram.service":   "active",
			"systemctl --user show -p UnitFileState --value -- opencode-telegram.service": "enabled",
			"loginctl show-user testuser -p Linger --value":                               "no",
		},
		mtimes:    map[string]time.Time{},
		runErrs:   map[string]error{},
		writeErrs: map[string]error{},
		removeErr: map[string]error{},
		look:      map[string]bool{"powertop": true},
	}
}

func (f *fakeHost) Read(path string) (string, error) {
	value, ok := f.files[path]
	if !ok {
		return "", fmt.Errorf("open %s: %w", path, fs.ErrNotExist)
	}
	return value, nil
}

func (f *fakeHost) WriteRoot(path, value string) error {
	if err := f.writeErrs[path]; err != nil {
		return err
	}
	f.writes = append(f.writes, path+"="+value)
	f.files[path] = value
	return nil
}

func (f *fakeHost) RemoveRoot(path string) error {
	if err := f.removeErr[path]; err != nil {
		return err
	}
	f.removed = append(f.removed, path)
	delete(f.files, path)
	return nil
}

func (f *fakeHost) MkdirRoot(path string) error { return nil }

func (f *fakeHost) Run(name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.runs = append(f.runs, key)
	if err := f.runErrs[key]; err != nil {
		return "", err
	}
	return f.outputs[key], nil
}

func (f *fakeHost) RunRoot(name string, args ...string) (string, error) {
	return f.Run(name, args...)
}

func (f *fakeHost) Glob(pattern string) ([]string, error) {
	return f.globs[pattern], nil
}

func (f *fakeHost) ModTime(path string) (time.Time, error) {
	at, ok := f.mtimes[path]
	if !ok {
		return time.Time{}, fmt.Errorf("stat %s: %w", path, fs.ErrNotExist)
	}
	return at, nil
}

func (f *fakeHost) Look(name string) bool { return f.look[name] }

func (f *fakeHost) User() string {
	if f.userName == "" {
		return "testuser"
	}
	return f.userName
}

func (f *fakeHost) PrimeRoot() error {
	f.primed = true
	return f.primeErr
}

func (f *fakeHost) ran(command string) bool {
	for _, run := range f.runs {
		if run == command {
			return true
		}
	}
	return false
}

func (f *fakeHost) wrote(path, value string) bool {
	for _, write := range f.writes {
		if write == path+"="+value {
			return true
		}
	}
	return false
}

func (f *fakeHost) writeFor(path string) (string, bool) {
	prefix := path + "="
	for _, write := range f.writes {
		if strings.HasPrefix(write, prefix) {
			return strings.TrimPrefix(write, prefix), true
		}
	}
	return "", false
}

var errBoom = errors.New("boom")

func boolPtr(v bool) *bool { return &v }
