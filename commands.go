package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type app struct {
	host       host
	store      stateStore
	configPath string
	out        io.Writer
	errOut     io.Writer
	now        func() time.Time
}

func (a *app) run(args []string) int {
	dryRun := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--dry-run" {
			dryRun = true
			continue
		}
		rest = append(rest, arg)
	}
	if len(rest) == 0 {
		a.usage(a.errOut)
		return 2
	}
	command := rest[0]
	if len(rest) > 1 {
		arg := rest[1]
		if arg == "-h" || arg == "--help" {
			a.usage(a.out)
			return 0
		}
		fmt.Fprintf(a.errOut, "unknown argument %q\n", arg)
		a.usage(a.errOut)
		return 2
	}

	switch command {
	case "on":
		return a.on(dryRun)
	case "off":
		return a.off(dryRun)
	case "status":
		return a.status()
	case "idle":
		return a.idle(dryRun)
	case "help", "-h", "--help":
		a.usage(a.out)
		return 0
	default:
		fmt.Fprintf(a.errOut, "unknown command %q\n", command)
		a.usage(a.errOut)
		return 2
	}
}

func (a *app) on(dryRun bool) int {
	cfg, exists, err := LoadConfig(a.configPath)
	if err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	if a.store.Exists() {
		fmt.Fprintf(a.errOut, "error: state already exists at %s\n", a.store.statePath())
		fmt.Fprintf(a.errOut, "phone mode already looks active; run off first, or remove that file if it is stale\n")
		return 1
	}

	if pct, status, ok := batteryState(a.host); ok && batteryBelowExit(cfg.BatteryExitPercent, pct, status) {
		msg := fmt.Sprintf("battery is %d%% (%s), below battery_exit_percent=%d", pct, status, cfg.BatteryExitPercent)
		if dryRun {
			fmt.Fprintf(a.out, "dry run: on would refuse: %s\n", msg)
		} else {
			fmt.Fprintf(a.errOut, "refusing phone mode: %s\n", msg)
		}
		return 1
	}

	if !exists {
		if dryRun {
			fmt.Fprintf(a.out, "would create default config at %s\n", a.configPath)
		} else if err := WriteDefaultConfig(a.configPath); err != nil {
			fmt.Fprintf(a.errOut, "warning: create default config: %v\n", err)
		} else {
			fmt.Fprintf(a.out, "created default config at %s\n", a.configPath)
		}
	}

	p, planErrs := buildOnPlan(cfg, a.host)
	p.state.SavedAt = a.now()

	if dryRun {
		fmt.Fprintf(a.out, "dry run: state would be saved to %s\n", a.store.statePath())
	} else {
		if err := a.host.PrimeRoot(); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return 1
		}
		if err := a.store.Save(p.state); err != nil {
			fmt.Fprintf(a.errOut, "error: save state: %v\n", err)
			return 1
		}
		if err := a.store.MarkActive(); err != nil {
			fmt.Fprintf(a.errOut, "error: mark phone mode active: %v\n", err)
			return 1
		}
		// A leftover idle profile belongs to the previous phone-mode session.
		if err := a.store.RemoveIdle(); err != nil {
			fmt.Fprintf(a.errOut, "error: clear stale idle state: %v\n", err)
			return 1
		}
		fmt.Fprintf(a.out, "snapshot saved to %s\n", a.store.statePath())
	}

	fmt.Fprintf(a.out, "changes:\n")
	errs := append(planErrs, runSteps(p.steps, dryRun, a.out)...)
	a.report(errs)

	if dryRun {
		fmt.Fprintf(a.out, "dry run: %d changes, nothing applied\n", len(p.steps))
	} else if len(errs) == 0 {
		fmt.Fprintf(a.out, "phone mode on: %d changes applied\n", len(p.steps))
	} else {
		fmt.Fprintf(a.errOut, "phone mode partially applied: %d change(s) failed\n", len(errs))
	}
	if len(errs) > 0 {
		return 1
	}
	return 0
}

func (a *app) off(dryRun bool) int {
	cfg, _, cfgErr := LoadConfig(a.configPath)
	if cfgErr != nil {
		fmt.Fprintf(a.errOut, "warning: %v; using defaults\n", cfgErr)
		cfg = DefaultConfig()
	}

	st, err := a.store.Load()
	haveState := err == nil
	graphicalActive, targetErr := systemUnitActive(a.host, "graphical.target")
	disruptive := targetErr != nil || !graphicalActive
	if !disruptive {
		fmt.Fprintln(a.out, "already in graphical mode: keeping the current target and logind")
	}
	var steps []step
	var planErrs []error
	if haveState {
		steps, planErrs = buildOffPlan(cfg, st, a.host, disruptive)
	} else {
		fmt.Fprintf(a.errOut, "warning: no state file at %s; restoring the config defaults\n", a.store.statePath())
		if !errors.Is(err, errNoState) {
			fmt.Fprintf(a.errOut, "warning: %v\n", err)
		}
		steps, planErrs = buildFallbackPlan(cfg, a.host, disruptive)
	}

	if !dryRun {
		if err := a.host.PrimeRoot(); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return 1
		}
	}

	fmt.Fprintf(a.out, "restores:\n")
	errs := append(planErrs, runSteps(steps, dryRun, a.out)...)

	if dryRun {
		fmt.Fprintf(a.out, "dry run: %d restores, nothing applied\n", len(steps))
		if haveState {
			fmt.Fprintf(a.out, "dry run: %s would be removed\n", a.store.statePath())
		}
		if a.store.IdleExists() {
			fmt.Fprintf(a.out, "dry run: %s would be removed\n", a.store.idlePath())
		}
		return 0
	}

	if err := a.store.ClearActive(); err != nil {
		errs = append(errs, fmt.Errorf("clear active marker: %w", err))
	}
	if a.store.IdleExists() {
		if err := a.store.RemoveIdle(); err != nil {
			errs = append(errs, fmt.Errorf("remove idle state: %w", err))
		}
	}
	if haveState {
		if len(errs) == 0 {
			if err := a.store.Remove(); err != nil {
				errs = append(errs, fmt.Errorf("remove state file: %w", err))
			} else {
				fmt.Fprintf(a.out, "state file removed: %s\n", a.store.statePath())
			}
		} else {
			fmt.Fprintf(a.errOut, "state kept at %s so off can be retried\n", a.store.statePath())
		}
	}

	a.report(errs)
	if len(errs) == 0 {
		fmt.Fprintln(a.out, "desktop mode restored")
	}
	if len(errs) > 0 {
		return 1
	}
	return 0
}

func (a *app) status() int {
	cfg, exists, err := LoadConfig(a.configPath)
	if err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}

	graphicalActive, targetErr := systemUnitActive(a.host, "graphical.target")
	mode := "desktop"
	if targetErr != nil {
		if a.store.Active() {
			mode = "phone (headless)"
		}
	} else if !graphicalActive {
		mode = "phone (headless)"
	}

	fmt.Fprintf(a.out, "mode: %s\n", mode)
	if exists {
		fmt.Fprintf(a.out, "config: %s\n", a.configPath)
	} else {
		fmt.Fprintf(a.out, "config: defaults (missing %s)\n", a.configPath)
	}

	st, stateErr := a.store.Load()
	switch {
	case stateErr == nil:
		fmt.Fprintf(a.out, "state: %s (saved %s)\n", a.store.statePath(), st.SavedAt.Format(time.RFC3339))
	case errors.Is(stateErr, errNoState):
		fmt.Fprintln(a.out, "state: none")
	default:
		fmt.Fprintf(a.out, "state: unreadable: %v\n", stateErr)
	}

	if targetErr != nil {
		fmt.Fprintf(a.out, "target: unknown (%v)\n", targetErr)
	} else if graphicalActive {
		fmt.Fprintln(a.out, "target: graphical.target (active)")
	} else {
		fmt.Fprintln(a.out, "target: multi-user.target (graphical inactive)")
	}

	fmt.Fprintf(a.out, "services: %s\n", a.serviceSummary(cfg))
	if linger, err := lingerEnabled(a.host, a.host.User()); err == nil {
		fmt.Fprintf(a.out, "linger: %s\n", onOff(linger))
	} else {
		fmt.Fprintf(a.out, "linger: unknown\n")
	}

	if pct, state, ok := batteryState(a.host); ok {
		if cfg.BatteryExitPercent > 0 {
			fmt.Fprintf(a.out, "battery: %d%% %s (exit threshold %d%%)\n", pct, state, cfg.BatteryExitPercent)
		} else {
			fmt.Fprintf(a.out, "battery: %d%% %s (exit threshold disabled)\n", pct, state)
		}
		if batteryBelowExit(cfg.BatteryExitPercent, pct, state) {
			fmt.Fprintf(a.out, "warning: below battery_exit_percent=%d; on would refuse\n", cfg.BatteryExitPercent)
		}
	} else {
		fmt.Fprintln(a.out, "battery: unknown")
	}

	if policies, err := cpufreqPolicies(a.host); err == nil {
		governors := make(map[string]bool)
		for _, dir := range policies {
			raw, _ := a.host.Read(dir + "/scaling_governor")
			governors[strings.TrimSpace(raw)] = true
		}
		names := make([]string, 0, len(governors))
		for name := range governors {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(a.out, "governor: %s (%d policies)\n", strings.Join(names, ", "), len(policies))

		if max, err := readUint(a.host, policies[0]+"/cpuinfo_max_freq"); err == nil {
			current, _ := readUint(a.host, policies[0]+"/scaling_max_freq")
			fmt.Fprintf(a.out, "max freq: %s\n", frequencyLabel(current, max))
		}
	} else {
		fmt.Fprintf(a.out, "governor: unknown (%v)\n", err)
	}

	if path, inverted, err := turboPath(a.host); err == nil {
		raw, _ := a.host.Read(path)
		fmt.Fprintf(a.out, "turbo: %s\n", onOff(turboEnabled(raw, inverted)))
	} else {
		fmt.Fprintln(a.out, "turbo: unknown")
	}

	if base, err := backlightBase(cfg, a.host); err == nil {
		max, _ := readUint(a.host, base+"/max_brightness")
		current, _ := readUint(a.host, base+"/brightness")
		fmt.Fprintf(a.out, "brightness: %s\n", brightnessLabel(current, max))
	} else {
		fmt.Fprintf(a.out, "brightness: unknown (%v)\n", err)
	}

	if radios, err := listRadios(a.host); err == nil {
		parts := make([]string, 0, len(radios))
		for _, r := range radios {
			parts = append(parts, r.Name+"("+r.Type+")="+blockedLabel(r.SoftBlocked))
		}
		fmt.Fprintf(a.out, "radios: %s\n", strings.Join(parts, " "))
	} else {
		fmt.Fprintf(a.out, "radios: unknown (%v)\n", err)
	}
	fmt.Fprintf(a.out, "lid: %s\n", lidSummary(effectiveLidActions(a.host)))

	if gpus, err := discreteGPUs(a.host); err == nil {
		if len(gpus) == 0 {
			fmt.Fprintln(a.out, "dgpu: none")
		} else {
			parts := make([]string, 0, len(gpus))
			for _, dir := range gpus {
				control, _ := a.host.Read(dir + "/power/control")
				parts = append(parts, filepath.Base(dir)+"="+strings.TrimSpace(control))
			}
			fmt.Fprintf(a.out, "dgpu: %s\n", strings.Join(parts, " "))
		}
	}

	active := a.store.Active() && ((targetErr == nil && !graphicalActive) || (targetErr != nil && mode == "phone (headless)"))
	if active {
		fmt.Fprintln(a.out, "battery mode: on (agents asked to stay light)")
	} else {
		fmt.Fprintln(a.out, "battery mode: off")
	}

	switch {
	case !cfg.Idle.Enabled:
		fmt.Fprintln(a.out, "idle profile: disabled")
	case a.store.IdleExists():
		fmt.Fprintln(a.out, "idle profile: applied (CPU capped, bot still online)")
	default:
		fmt.Fprintf(a.out, "idle profile: waiting for %d quiet minutes\n", cfg.Idle.QuietMinutes)
	}
	return 0
}

// phoneModeActive requires both the marker and an inactive graphical target.
// The marker alone is accepted when the target cannot be queried.
func (a *app) phoneModeActive() bool {
	if !a.store.Active() {
		return false
	}
	graphical, err := systemUnitActive(a.host, "graphical.target")
	return err != nil || !graphical
}

func (a *app) serviceSummary(cfg Config) string {
	if len(cfg.KeepServices) == 0 {
		return "none configured"
	}
	parts := make([]string, 0, len(cfg.KeepServices))
	for _, unit := range cfg.KeepServices {
		active, _, err := userUnitState(a.host, unit)
		if err != nil {
			parts = append(parts, unit+"=unknown")
			continue
		}
		parts = append(parts, unit+"="+onOff(active))
	}
	return strings.Join(parts, " ")
}

func (a *app) report(errs []error) {
	if len(errs) == 0 {
		return
	}
	fmt.Fprintf(a.errOut, "\n%d step(s) failed:\n", len(errs))
	for _, err := range errs {
		fmt.Fprintf(a.errOut, "  - %v\n", err)
	}
}

func (a *app) usage(w io.Writer) {
	fmt.Fprintf(w, `Usage: opencode-phone-mode <on|off|status|idle> [--dry-run]

  on      snapshot settings, then switch to headless phone mode
  off     restore the snapshot, then return to graphical.target
  status  show current mode, settings, and battery
  idle    apply or restore the quiet-time CPU profile in phone mode

  --dry-run   print every change with current and new values; apply nothing

config: %s
state:  %s
`, a.configPath, a.store.statePath())
}

func newApp() (*app, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &app{
		host:       newRealHost(),
		store:      stateStore{dir: filepath.Join(cacheDir, "opencode-phone-mode")},
		configPath: filepath.Join(configDir, "opencode-phone-mode", "config.toml"),
		out:        os.Stdout,
		errOut:     os.Stderr,
		now:        time.Now,
	}, nil
}
