package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	lidDropInPath = "/etc/systemd/logind.conf.d/99-opencode-phone-mode.conf"
	backlightGlob = "/sys/class/backlight/*"
)

type step struct {
	setting string
	current string
	next    string
	apply   func() error
}

type plan struct {
	state State
	steps []step
}

// buildOnPlan reads every setting first, then returns the changes to apply.
// A read failure skips that setting instead of guessing a restore value.
func buildOnPlan(cfg Config, h host) (plan, []error) {
	p := plan{state: State{Version: stateVersion, SavedAt: time.Now()}}
	var errs []error
	note := func(err error) { errs = append(errs, err) }

	if cfg.ManageLid {
		cur, existed, err := readFileState(h, lidDropInPath)
		if err != nil {
			note(fmt.Errorf("snapshot lid switch: %w", err))
		} else {
			actions := effectiveLidActions(h)
			p.state.Lid = &LidState{
				Path:      lidDropInPath,
				Existed:   existed,
				Content:   cur,
				Effective: actions,
			}
			dropIn := lidDropIn(cfg.LidSwitch)
			p.steps = append(p.steps, step{
				setting: "lid switch",
				current: lidSummary(actions),
				next:    cfg.LidSwitch,
				apply: func() error {
					if err := h.MkdirRoot(filepath.Dir(lidDropInPath)); err != nil {
						return err
					}
					if err := h.WriteRoot(lidDropInPath, dropIn); err != nil {
						return err
					}
					_, err := h.RunRoot("systemctl", "restart", "systemd-logind")
					return err
				},
			})
		}
	}

	if cfg.DisableDGPU {
		gpus, err := discreteGPUs(h)
		if err != nil {
			note(fmt.Errorf("scan discrete GPU: %w", err))
		}
		for _, dir := range gpus {
			control := dir + "/power/control"
			raw, err := h.Read(control)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				note(fmt.Errorf("snapshot %s: %w", control, err))
				continue
			}
			value := strings.TrimSpace(raw)
			p.state.DGPU = append(p.state.DGPU, SysValue{Path: control, Value: value})
			gpu := filepath.Base(dir)
			p.steps = append(p.steps, step{
				setting: "dgpu " + gpu,
				current: value,
				next:    "auto",
				apply:   func() error { return h.WriteRoot(control, "auto") },
			})
		}

		if raw, err := h.Read("/proc/acpi/bbswitch"); err == nil {
			value := strings.TrimSpace(raw)
			p.state.BBSwitch = &SysValue{Path: "/proc/acpi/bbswitch", Value: value}
			p.steps = append(p.steps, step{
				setting: "bbswitch",
				current: value,
				next:    "OFF",
				apply:   func() error { return h.WriteRoot("/proc/acpi/bbswitch", "OFF") },
			})
		}

		if out, err := h.Run("systemctl", "show", "-p", "ActiveState", "--value", "--", "nvidia-persistenced.service"); err == nil && strings.TrimSpace(out) == "active" {
			svc := ServiceState{Name: "nvidia-persistenced.service", Scope: "system", Active: true}
			p.state.Nvidia = &svc
			p.steps = append(p.steps, step{
				setting: "nvidia-persistenced",
				current: "active",
				next:    "stopped",
				apply: func() error {
					_, err := h.RunRoot("systemctl", "stop", "nvidia-persistenced.service")
					return err
				},
			})
		}
	}

	if cfg.ManageGovernor {
		policies, err := cpufreqPolicies(h)
		if err != nil {
			note(fmt.Errorf("snapshot governor: %w", err))
		}
		for _, dir := range policies {
			path := dir + "/scaling_governor"
			raw, err := h.Read(path)
			if err != nil {
				note(fmt.Errorf("snapshot %s: %w", path, err))
				continue
			}
			current := strings.TrimSpace(raw)
			p.state.Governor = append(p.state.Governor, SysValue{Path: path, Value: current})
			p.steps = append(p.steps, step{
				setting: "governor " + filepath.Base(dir),
				current: current,
				next:    cfg.Governor,
				apply:   func() error { return h.WriteRoot(path, cfg.Governor) },
			})
		}
	}

	if cfg.ManageTurbo {
		path, inverted, err := turboPath(h)
		if err != nil {
			note(fmt.Errorf("snapshot turbo: %w", err))
		} else {
			raw, err := h.Read(path)
			if err != nil {
				note(fmt.Errorf("snapshot %s: %w", path, err))
			} else {
				current := strings.TrimSpace(raw)
				p.state.Turbo = &SysValue{Path: path, Value: current}
				p.steps = append(p.steps, step{
					setting: "turbo",
					current: onOff(turboEnabled(current, inverted)),
					next:    onOff(cfg.Turbo),
					apply:   func() error { return h.WriteRoot(path, turboValue(cfg.Turbo, inverted)) },
				})
			}
		}
	}

	if cfg.ManageMaxFreq {
		policies, err := cpufreqPolicies(h)
		if err != nil {
			note(fmt.Errorf("snapshot max freq: %w", err))
		}
		for _, dir := range policies {
			path := dir + "/scaling_max_freq"
			max, err := readUint(h, dir+"/cpuinfo_max_freq")
			if err != nil {
				note(fmt.Errorf("snapshot %s: %w", path, err))
				continue
			}
			current, err := readUint(h, path)
			if err != nil {
				note(fmt.Errorf("snapshot %s: %w", path, err))
				continue
			}
			min := readUintOr(h, dir+"/scaling_min_freq", 0)
			want := capFrequency(max, min, cfg.CPUMaxFreqPercent)
			p.state.MaxFreq = append(p.state.MaxFreq, SysValue{Path: path, Value: strconv.FormatUint(current, 10)})
			p.steps = append(p.steps, step{
				setting: "max freq " + filepath.Base(dir),
				current: frequencyLabel(current, max),
				next:    frequencyLabel(want, max),
				apply:   func() error { return h.WriteRoot(path, strconv.FormatUint(want, 10)) },
			})
		}
	}

	if cfg.Powertop {
		next := "auto-tune"
		if !h.Look("powertop") {
			next += " (not installed)"
		}
		p.steps = append(p.steps, step{
			setting: "powertop",
			current: "manual",
			next:    next,
			apply: func() error {
				_, err := h.RunRoot("powertop", "--auto-tune")
				return err
			},
		})
	}

	if cfg.ManageRadios {
		radios, err := listRadios(h)
		if err != nil {
			note(fmt.Errorf("snapshot radios: %w", err))
		}
		blockBluetooth := false
		var radioSteps []step
		for _, r := range radios {
			p.state.Radios = append(p.state.Radios, RadioState(r))
			if !radioKept(cfg, r.Type) {
				if r.Type == "bluetooth" {
					blockBluetooth = true
				}
				name := r.Name
				radioSteps = append(radioSteps, step{
					setting: "radio " + name,
					current: blockedLabel(r.SoftBlocked),
					next:    "blocked",
					apply:   func() error { _, err := h.RunRoot("rfkill", "block", name); return err },
				})
			}
		}
		if blockBluetooth {
			if out, err := h.Run("systemctl", "show", "-p", "ActiveState", "--value", "--", "bluetooth.service"); err == nil && strings.TrimSpace(out) == "active" {
				svc := ServiceState{Name: "bluetooth.service", Scope: "system", Active: true}
				p.state.Bluetooth = &svc
				p.steps = append(p.steps, step{
					setting: "bluetooth.service",
					current: "active",
					next:    "stopped",
					apply: func() error {
						_, err := h.RunRoot("systemctl", "stop", "bluetooth.service")
						return err
					},
				})
			}
		}
		p.steps = append(p.steps, radioSteps...)
	}

	if cfg.ManageServices && len(cfg.KeepServices) > 0 {
		userName := h.User()
		if enabled, err := lingerEnabled(h, userName); err != nil {
			note(fmt.Errorf("snapshot linger: %w", err))
		} else {
			p.state.Linger = &enabled
			if !enabled {
				p.steps = append(p.steps, step{
					setting: "linger",
					current: "disabled",
					next:    "enabled",
					apply: func() error {
						_, err := h.Run("loginctl", "enable-linger", userName)
						return err
					},
				})
			}
		}
		for _, unit := range cfg.KeepServices {
			name := unit
			active, enabled, err := userUnitState(h, name)
			if err != nil {
				note(fmt.Errorf("snapshot %s: %w", name, err))
				continue
			}
			p.state.Services = append(p.state.Services, ServiceState{
				Name:    name,
				Scope:   "user",
				Active:  active,
				Enabled: enabled,
			})
			if enabled == "disabled" {
				p.steps = append(p.steps, step{
					setting: "enable " + name,
					current: enabled,
					next:    "enabled",
					apply: func() error {
						_, err := h.Run("systemctl", "--user", "enable", "--", name)
						return err
					},
				})
			}
			if !active {
				p.steps = append(p.steps, step{
					setting: "start " + name,
					current: "inactive",
					next:    "active",
					apply: func() error {
						_, err := h.Run("systemctl", "--user", "start", "--", name)
						return err
					},
				})
			}
		}
	}

	if cfg.ManageBacklight && cfg.Brightness >= 0 {
		base, err := backlightBase(cfg, h)
		if err != nil {
			note(err)
		} else {
			max, err := readUint(h, base+"/max_brightness")
			if err != nil {
				note(fmt.Errorf("snapshot backlight: %w", err))
			} else {
				current, err := readUint(h, base+"/brightness")
				if err != nil {
					note(fmt.Errorf("snapshot backlight: %w", err))
				} else {
					path := base + "/brightness"
					want := max * uint64(cfg.Brightness) / 100
					p.state.Brightness = &SysValue{Path: path, Value: strconv.FormatUint(current, 10)}
					p.steps = append(p.steps, step{
						setting: "brightness",
						current: brightnessLabel(current, max),
						next:    brightnessLabel(want, max),
						apply:   func() error { return h.WriteRoot(path, strconv.FormatUint(want, 10)) },
					})
				}
			}
		}
	}

	if cfg.IsolateTarget {
		current := "multi-user.target"
		if active, err := systemUnitActive(h, "graphical.target"); err == nil && active {
			current = "graphical.target"
		}
		p.steps = append(p.steps, step{
			setting: "target",
			current: current,
			next:    "multi-user.target",
			apply: func() error {
				_, err := h.RunRoot("systemctl", "isolate", "multi-user.target")
				return err
			},
		})
	}

	return p, errs
}

// buildOffPlan turns a saved snapshot back into changes. Values are written
// verbatim, including devices that disappeared; those failures are reported.
func buildOffPlan(cfg Config, st State, h host) ([]step, []error) {
	var steps []step
	var errs []error
	note := func(err error) { errs = append(errs, err) }

	if st.Brightness != nil && st.Brightness.Path != "" {
		saved := st.Brightness.Value
		path := st.Brightness.Path
		max := readUintOr(h, filepath.Dir(path)+"/max_brightness", 0)
		current, err := readUint(h, path)
		if err != nil {
			note(fmt.Errorf("read brightness: %w", err))
		}
		steps = append(steps, step{
			setting: "brightness",
			current: brightnessLabel(current, max),
			next:    brightnessLabelFromString(saved, max),
			apply:   func() error { return h.WriteRoot(path, saved) },
		})
	}

	if st.Lid != nil {
		saved := *st.Lid
		next := saved.Effective["HandleLidSwitch"]
		if next == "" {
			next = "previous"
		}
		steps = append(steps, step{
			setting: "lid switch",
			current: lidSummary(effectiveLidActions(h)),
			next:    next,
			apply: func() error {
				if saved.Existed {
					if err := h.MkdirRoot(filepath.Dir(saved.Path)); err != nil {
						return err
					}
					if err := h.WriteRoot(saved.Path, saved.Content); err != nil {
						return err
					}
				} else if err := h.RemoveRoot(saved.Path); err != nil {
					return err
				}
				_, err := h.RunRoot("systemctl", "restart", "systemd-logind")
				return err
			},
		})
	}

	for _, v := range st.Governor {
		value := v
		current, _ := h.Read(value.Path)
		steps = append(steps, step{
			setting: "governor " + filepath.Base(filepath.Dir(value.Path)),
			current: strings.TrimSpace(current),
			next:    value.Value,
			apply:   func() error { return h.WriteRoot(value.Path, value.Value) },
		})
	}

	for _, v := range st.MaxFreq {
		value := v
		max := readUintOr(h, filepath.Dir(value.Path)+"/cpuinfo_max_freq", 0)
		current, _ := readUint(h, value.Path)
		want, _ := strconv.ParseUint(value.Value, 10, 64)
		steps = append(steps, step{
			setting: "max freq " + filepath.Base(filepath.Dir(value.Path)),
			current: frequencyLabel(current, max),
			next:    frequencyLabel(want, max),
			apply:   func() error { return h.WriteRoot(value.Path, value.Value) },
		})
	}

	if st.Turbo != nil {
		value := *st.Turbo
		inverted := strings.Contains(filepath.Base(value.Path), "no_turbo")
		raw, _ := h.Read(value.Path)
		steps = append(steps, step{
			setting: "turbo",
			current: onOff(turboEnabled(raw, inverted)),
			next:    onOff(turboEnabled(value.Value, inverted)),
			apply:   func() error { return h.WriteRoot(value.Path, value.Value) },
		})
	}

	for _, r := range st.Radios {
		radio := r
		command := "unblock"
		if radio.SoftBlocked {
			command = "block"
		}
		steps = append(steps, step{
			setting: "radio " + radio.Name,
			current: blockedLabel(!radio.SoftBlocked),
			next:    blockedLabel(radio.SoftBlocked),
			apply: func() error {
				_, err := h.RunRoot("rfkill", command, radio.Name)
				return err
			},
		})
	}

	if st.Bluetooth != nil && st.Bluetooth.Active {
		steps = append(steps, step{
			setting: "bluetooth.service",
			current: "stopped",
			next:    "active",
			apply: func() error {
				_, err := h.RunRoot("systemctl", "start", "bluetooth.service")
				return err
			},
		})
	}

	for _, svc := range st.Services {
		service := svc
		currentActive, currentEnabled, stateErr := userUnitState(h, service.Name)
		if stateErr != nil {
			note(fmt.Errorf("read %s: %w", service.Name, stateErr))
		}
		if service.Enabled == "enabled" {
			steps = append(steps, step{
				setting: "enable " + service.Name,
				current: currentEnabled,
				next:    "enabled",
				apply: func() error {
					_, err := h.Run("systemctl", "--user", "enable", "--", service.Name)
					return err
				},
			})
		}
		if service.Enabled == "disabled" {
			steps = append(steps, step{
				setting: "enable " + service.Name,
				current: currentEnabled,
				next:    "disabled",
				apply: func() error {
					_, err := h.Run("systemctl", "--user", "disable", "--", service.Name)
					return err
				},
			})
		}
		if service.Active {
			steps = append(steps, step{
				setting: "service " + service.Name,
				current: onOff(currentActive),
				next:    "active",
				apply: func() error {
					_, err := h.Run("systemctl", "--user", "start", "--", service.Name)
					return err
				},
			})
		} else {
			steps = append(steps, step{
				setting: "service " + service.Name,
				current: onOff(currentActive),
				next:    "inactive",
				apply: func() error {
					_, err := h.Run("systemctl", "--user", "stop", "--", service.Name)
					return err
				},
			})
		}
	}

	if st.Linger != nil {
		want := *st.Linger
		userName := h.User()
		current, err := lingerEnabled(h, userName)
		if err != nil {
			note(fmt.Errorf("read linger: %w", err))
		}
		command := "disable-linger"
		if want {
			command = "enable-linger"
		}
		steps = append(steps, step{
			setting: "linger",
			current: onOff(current),
			next:    onOff(want),
			apply: func() error {
				_, err := h.Run("loginctl", command, userName)
				return err
			},
		})
	}

	for _, v := range st.DGPU {
		value := v
		current, _ := h.Read(value.Path)
		steps = append(steps, step{
			setting: "dgpu " + filepath.Base(filepath.Dir(filepath.Dir(value.Path))),
			current: strings.TrimSpace(current),
			next:    value.Value,
			apply:   func() error { return h.WriteRoot(value.Path, value.Value) },
		})
	}
	if st.BBSwitch != nil {
		value := *st.BBSwitch
		steps = append(steps, step{
			setting: "bbswitch",
			current: "OFF",
			next:    value.Value,
			apply:   func() error { return h.WriteRoot(value.Path, value.Value) },
		})
	}
	if st.Nvidia != nil && st.Nvidia.Active {
		steps = append(steps, step{
			setting: "nvidia-persistenced",
			current: "stopped",
			next:    "active",
			apply: func() error {
				_, err := h.RunRoot("systemctl", "start", "nvidia-persistenced.service")
				return err
			},
		})
	}

	if cfg.IsolateTarget {
		steps = append(steps, step{
			setting: "target",
			current: "multi-user.target",
			next:    "graphical.target",
			apply: func() error {
				_, err := h.RunRoot("systemctl", "isolate", "graphical.target")
				return err
			},
		})
	}

	return steps, errs
}

// buildFallbackPlan is used when state.json is missing: exact values are
// unknowable, so the [restore] defaults are applied.
func buildFallbackPlan(cfg Config, h host) ([]step, []error) {
	var steps []step
	var errs []error
	note := func(err error) { errs = append(errs, err) }
	restore := cfg.Restore

	if base, err := backlightBase(cfg, h); err != nil {
		note(err)
	} else {
		max, err := readUint(h, base+"/max_brightness")
		if err != nil {
			note(fmt.Errorf("read backlight: %w", err))
		} else {
			current, _ := readUint(h, base+"/brightness")
			want := max * uint64(restore.Brightness) / 100
			path := base + "/brightness"
			steps = append(steps, step{
				setting: "brightness",
				current: brightnessLabel(current, max),
				next:    brightnessLabel(want, max),
				apply:   func() error { return h.WriteRoot(path, strconv.FormatUint(want, 10)) },
			})
		}
	}

	if restore.LidSwitch != "" {
		dropIn := lidDropIn(restore.LidSwitch)
		steps = append(steps, step{
			setting: "lid switch",
			current: lidSummary(effectiveLidActions(h)),
			next:    restore.LidSwitch,
			apply: func() error {
				if err := h.MkdirRoot(filepath.Dir(lidDropInPath)); err != nil {
					return err
				}
				if err := h.WriteRoot(lidDropInPath, dropIn); err != nil {
					return err
				}
				_, err := h.RunRoot("systemctl", "restart", "systemd-logind")
				return err
			},
		})
	} else if _, err := h.Read(lidDropInPath); err == nil {
		steps = append(steps, step{
			setting: "lid switch",
			current: lidSummary(effectiveLidActions(h)),
			next:    "system default",
			apply: func() error {
				if err := h.RemoveRoot(lidDropInPath); err != nil {
					return err
				}
				_, err := h.RunRoot("systemctl", "restart", "systemd-logind")
				return err
			},
		})
	}

	if restore.Governor != "" {
		policies, err := cpufreqPolicies(h)
		if err != nil {
			note(fmt.Errorf("restore governor: %w", err))
		}
		for _, dir := range policies {
			path := dir + "/scaling_governor"
			current, _ := h.Read(path)
			steps = append(steps, step{
				setting: "governor " + filepath.Base(dir),
				current: strings.TrimSpace(current),
				next:    restore.Governor,
				apply:   func() error { return h.WriteRoot(path, restore.Governor) },
			})
		}
	}

	policies, err := cpufreqPolicies(h)
	if err != nil {
		note(fmt.Errorf("restore max freq: %w", err))
	}
	for _, dir := range policies {
		path := dir + "/scaling_max_freq"
		max, err := readUint(h, dir+"/cpuinfo_max_freq")
		if err != nil {
			note(fmt.Errorf("restore max freq: %w", err))
			continue
		}
		current, _ := readUint(h, path)
		min := readUintOr(h, dir+"/scaling_min_freq", 0)
		want := capFrequency(max, min, restore.CPUMaxFreqPercent)
		steps = append(steps, step{
			setting: "max freq " + filepath.Base(dir),
			current: frequencyLabel(current, max),
			next:    frequencyLabel(want, max),
			apply:   func() error { return h.WriteRoot(path, strconv.FormatUint(want, 10)) },
		})
	}

	if path, inverted, err := turboPath(h); err != nil {
		note(fmt.Errorf("restore turbo: %w", err))
	} else {
		raw, _ := h.Read(path)
		steps = append(steps, step{
			setting: "turbo",
			current: onOff(turboEnabled(raw, inverted)),
			next:    onOff(restore.Turbo),
			apply:   func() error { return h.WriteRoot(path, turboValue(restore.Turbo, inverted)) },
		})
	}

	radios, err := listRadios(h)
	if err != nil {
		note(fmt.Errorf("restore radios: %w", err))
	}
	bluetoothKept := restore.Bluetooth
	for _, r := range radios {
		radio := r
		block := radio.Type == "bluetooth" && !bluetoothKept
		command := "unblock"
		if block {
			command = "block"
		}
		steps = append(steps, step{
			setting: "radio " + radio.Name,
			current: blockedLabel(radio.SoftBlocked),
			next:    blockedLabel(block),
			apply: func() error {
				_, err := h.RunRoot("rfkill", command, radio.Name)
				return err
			},
		})
	}
	if bluetoothKept {
		if out, err := h.Run("systemctl", "show", "-p", "ActiveState", "--value", "--", "bluetooth.service"); err == nil && strings.TrimSpace(out) != "active" {
			steps = append(steps, step{
				setting: "bluetooth.service",
				current: "inactive",
				next:    "active",
				apply: func() error {
					_, err := h.RunRoot("systemctl", "start", "bluetooth.service")
					return err
				},
			})
		}
	} else if out, err := h.Run("systemctl", "show", "-p", "ActiveState", "--value", "--", "bluetooth.service"); err == nil && strings.TrimSpace(out) == "active" {
		steps = append(steps, step{
			setting: "bluetooth.service",
			current: "active",
			next:    "stopped",
			apply: func() error {
				_, err := h.RunRoot("systemctl", "stop", "bluetooth.service")
				return err
			},
		})
	}

	if len(cfg.KeepServices) > 0 && restore.ServicesRunning {
		if enabled, err := lingerEnabled(h, h.User()); err == nil && !enabled {
			steps = append(steps, step{
				setting: "linger",
				current: "disabled",
				next:    "enabled",
				apply: func() error {
					_, err := h.Run("loginctl", "enable-linger", h.User())
					return err
				},
			})
		}
		for _, unit := range cfg.KeepServices {
			name := unit
			active, enabled, err := userUnitState(h, name)
			currentActive := "inactive"
			if err == nil {
				currentActive = onOff(active)
			}
			if err != nil || enabled == "disabled" {
				steps = append(steps, step{
					setting: "enable " + name,
					current: enabled,
					next:    "enabled",
					apply: func() error {
						_, err := h.Run("systemctl", "--user", "enable", "--", name)
						return err
					},
				})
			}
			if err != nil || !active {
				steps = append(steps, step{
					setting: "service " + name,
					current: currentActive,
					next:    "active",
					apply: func() error {
						_, err := h.Run("systemctl", "--user", "start", "--", name)
						return err
					},
				})
			}
		}
	}

	if gpus, err := discreteGPUs(h); err == nil {
		for _, dir := range gpus {
			control := dir + "/power/control"
			steps = append(steps, step{
				setting: "dgpu " + filepath.Base(dir),
				current: "auto",
				next:    "auto",
				apply:   func() error { return h.WriteRoot(control, "auto") },
			})
		}
	}
	if _, err := h.Read("/proc/acpi/bbswitch"); err == nil {
		steps = append(steps, step{
			setting: "bbswitch",
			current: "OFF",
			next:    "ON",
			apply:   func() error { return h.WriteRoot("/proc/acpi/bbswitch", "ON") },
		})
	}

	if cfg.IsolateTarget {
		current := "multi-user.target"
		if active, err := systemUnitActive(h, "graphical.target"); err == nil && active {
			current = "graphical.target"
		}
		steps = append(steps, step{
			setting: "target",
			current: current,
			next:    "graphical.target",
			apply: func() error {
				_, err := h.RunRoot("systemctl", "isolate", "graphical.target")
				return err
			},
		})
	}

	return steps, errs
}

func runSteps(steps []step, dryRun bool, out io.Writer) []error {
	var errs []error
	for _, s := range steps {
		fmt.Fprintf(out, "  %-26s %s -> %s\n", s.setting, s.current, s.next)
		if dryRun {
			continue
		}
		if err := s.apply(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.setting, err))
		}
	}
	return errs
}

func readFileState(h host, path string) (content string, exists bool, err error) {
	raw, err := h.Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return raw, true, nil
}

func backlightBase(cfg Config, h host) (string, error) {
	device := cfg.BacklightDevice
	if device == "" {
		dirs, err := h.Glob(backlightGlob)
		if err != nil {
			return "", fmt.Errorf("find backlight: %w", err)
		}
		if len(dirs) == 0 {
			return "", errors.New("no backlight device found")
		}
		sort.Strings(dirs)
		device = filepath.Base(dirs[0])
	}
	return "/sys/class/backlight/" + device, nil
}

func brightnessLabel(value, max uint64) string {
	if max == 0 {
		return strconv.FormatUint(value, 10)
	}
	return fmt.Sprintf("%d/%d (%d%%)", value, max, value*100/max)
}

func brightnessLabelFromString(value string, max uint64) string {
	n, _ := strconv.ParseUint(value, 10, 64)
	return brightnessLabel(n, max)
}

func frequencyLabel(value, max uint64) string {
	if max == 0 {
		return fmt.Sprintf("%d kHz", value)
	}
	return fmt.Sprintf("%d/%d kHz (%d%%)", value, max, value*100/max)
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func blockedLabel(blocked bool) string {
	if blocked {
		return "blocked"
	}
	return "unblocked"
}
