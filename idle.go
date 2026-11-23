package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"time"
)

const botLogPattern = "bot-*.log"

// lastBotActivity uses the newest bot log mtime as the activity signal. A log
// write means a message was handled or a reply was delivered, so activity is
// detected without touching the bot or its config.
func lastBotActivity(cfg Config, h host) (time.Time, bool, []error) {
	paths, err := h.Glob(filepath.Join(cfg.activityLogDir(), botLogPattern))
	if err != nil {
		return time.Time{}, false, []error{fmt.Errorf("find bot logs: %w", err)}
	}
	var newest time.Time
	var errs []error
	found := false
	for _, path := range paths {
		at, err := h.ModTime(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read bot log time %s: %w", path, err))
			continue
		}
		if !found || at.After(newest) {
			newest = at
			found = true
		}
	}
	return newest, found, errs
}

// buildIdlePlan snapshots the exact frequency values it is about to replace.
// The snapshot is saved before any write so activity can restore it.
func buildIdlePlan(cfg Config, h host, now time.Time) (IdleState, []step, []error) {
	st := IdleState{Version: idleVersion, AppliedAt: now}
	var steps []step
	var errs []error
	note := func(err error) { errs = append(errs, err) }

	policies, err := cpufreqPolicies(h)
	if err != nil {
		note(fmt.Errorf("idle cpufreq: %w", err))
		return st, steps, errs
	}
	for _, dir := range policies {
		path := dir + "/scaling_max_freq"
		max, err := readUint(h, dir+"/cpuinfo_max_freq")
		if err != nil {
			note(fmt.Errorf("idle snapshot %s: %w", path, err))
			continue
		}
		current, err := readUint(h, path)
		if err != nil {
			note(fmt.Errorf("idle snapshot %s: %w", path, err))
			continue
		}
		min := readUintOr(h, dir+"/scaling_min_freq", 0)
		want := capFrequency(max, min, cfg.Idle.CPUMaxFreqPercent)
		if want >= current {
			continue
		}
		saved := strconv.FormatUint(current, 10)
		st.MaxFreq = append(st.MaxFreq, SysValue{Path: path, Value: saved})
		steps = append(steps, step{
			setting: "idle max freq " + filepath.Base(dir),
			current: frequencyLabel(current, max),
			next:    frequencyLabel(want, max),
			apply:   func() error { return h.WriteRoot(path, strconv.FormatUint(want, 10)) },
		})
	}
	return st, steps, errs
}

// buildIdleRestorePlan writes back the values saved when the idle profile was
// applied. Failed reads are reported, but the write is still attempted so a
// disappeared device cannot block the other policies.
func buildIdleRestorePlan(st IdleState, h host) ([]step, []error) {
	var steps []step
	var errs []error
	for _, value := range st.MaxFreq {
		v := value
		max := readUintOr(h, filepath.Dir(v.Path)+"/cpuinfo_max_freq", 0)
		current, err := readUint(h, v.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("read idle cap %s: %w", v.Path, err))
		}
		want, err := strconv.ParseUint(v.Value, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("saved idle cap %s: %w", v.Path, err))
			continue
		}
		if current == want {
			continue
		}
		steps = append(steps, step{
			setting: "restore idle " + filepath.Base(filepath.Dir(v.Path)),
			current: frequencyLabel(current, max),
			next:    frequencyLabel(want, max),
			apply:   func() error { return h.WriteRoot(v.Path, v.Value) },
		})
	}
	return steps, errs
}

func (a *app) idle(dryRun bool) int {
	cfg, _, err := LoadConfig(a.configPath)
	if err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	if !cfg.Idle.Enabled {
		fmt.Fprintln(a.out, "idle: disabled")
		return 0
	}
	if !a.phoneModeActive() {
		fmt.Fprintln(a.out, "idle: phone mode is not active")
		return 0
	}

	quietFor := time.Duration(cfg.Idle.QuietMinutes) * time.Minute
	now := a.now()
	last, found, activityErrs := lastBotActivity(cfg, a.host)
	a.report(activityErrs)

	saved, err := a.store.LoadIdle()
	haveIdle := err == nil
	if err != nil && !errors.Is(err, errNoState) {
		fmt.Fprintf(a.errOut, "warning: unreadable idle state: %v\n", err)
	}

	// No activity log is a reason to undo an applied profile, but not to apply
	// one: without a signal we cannot know the machine is quiet.
	if haveIdle && (!found || now.Sub(last) < quietFor || last.After(saved.AppliedAt)) {
		return a.restoreIdle(saved, dryRun)
	}
	if !found {
		fmt.Fprintf(a.out, "idle: no bot logs in %s\n", cfg.activityLogDir())
		return 0
	}
	if haveIdle {
		fmt.Fprintf(a.out, "idle: profile applied %s ago, still quiet\n", now.Sub(saved.AppliedAt).Round(time.Minute))
		return 0
	}
	quietTime := now.Sub(last)
	if quietTime < quietFor {
		fmt.Fprintf(a.out, "idle: bot active %s ago, waiting %s\n", quietTime.Round(time.Minute), (quietFor - quietTime).Round(time.Minute))
		return 0
	}
	return a.applyIdle(cfg, now, dryRun)
}

func (a *app) applyIdle(cfg Config, now time.Time, dryRun bool) int {
	saved, steps, planErrs := buildIdlePlan(cfg, a.host, now)
	if len(steps) == 0 {
		fmt.Fprintln(a.out, "idle: CPU cap is already at or below the idle profile")
		a.report(planErrs)
		if len(planErrs) > 0 {
			return 1
		}
		return 0
	}

	if dryRun {
		fmt.Fprintf(a.out, "idle dry run: would save %s and restore from it on activity\n", a.store.idlePath())
		fmt.Fprintln(a.out, "idle changes:")
		a.report(planErrs)
		runSteps(steps, true, a.out)
		fmt.Fprintf(a.out, "idle dry run: %d changes, nothing applied\n", len(steps))
		return 0
	}

	if err := a.host.PrimeRoot(); err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	// Save before writing: a partially applied profile must still be restorable.
	if err := a.store.SaveIdle(saved); err != nil {
		fmt.Fprintf(a.errOut, "error: save idle state: %v\n", err)
		return 1
	}
	fmt.Fprintln(a.out, "idle changes:")
	errs := append(planErrs, runSteps(steps, false, a.out)...)
	a.report(errs)
	if len(errs) > 0 {
		fmt.Fprintf(a.errOut, "idle profile partially applied; idle state kept at %s\n", a.store.idlePath())
		return 1
	}
	fmt.Fprintf(a.out, "idle profile applied (saved at %s)\n", a.store.idlePath())
	return 0
}

func (a *app) restoreIdle(saved IdleState, dryRun bool) int {
	steps, planErrs := buildIdleRestorePlan(saved, a.host)
	if dryRun {
		fmt.Fprintln(a.out, "idle restores:")
		a.report(planErrs)
		runSteps(steps, true, a.out)
		fmt.Fprintf(a.out, "idle dry run: %d restores, nothing applied\n", len(steps))
		return 0
	}
	if len(steps) == 0 {
		if err := a.store.RemoveIdle(); err != nil {
			fmt.Fprintf(a.errOut, "error: remove idle state: %v\n", err)
			return 1
		}
		fmt.Fprintln(a.out, "idle: activity detected, frequency already restored")
		return 0
	}
	if err := a.host.PrimeRoot(); err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(a.out, "idle restores:")
	errs := append(planErrs, runSteps(steps, false, a.out)...)
	if len(errs) == 0 {
		if err := a.store.RemoveIdle(); err != nil {
			errs = append(errs, fmt.Errorf("remove idle state: %w", err))
		}
	}
	a.report(errs)
	if len(errs) > 0 {
		fmt.Fprintf(a.errOut, "idle state kept at %s so restore can be retried\n", a.store.idlePath())
		return 1
	}
	fmt.Fprintln(a.out, "idle: activity detected, previous frequencies restored")
	return 0
}
