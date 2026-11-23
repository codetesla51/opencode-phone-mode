package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestApp(t *testing.T) (*app, *fakeHost, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	fake := newFakeHost()
	a := &app{
		host:       fake,
		store:      stateStore{dir: filepath.Join(root, "cache", "opencode-phone-mode")},
		configPath: filepath.Join(root, "config", "opencode-phone-mode", "config.toml"),
		out:        out,
		errOut:     errOut,
		now:        func() time.Time { return time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC) },
	}
	return a, fake, out, errOut
}

func saveTestState(t *testing.T, store stateStore) {
	t.Helper()
	st := fullState()
	st.SavedAt = time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkActive(); err != nil {
		t.Fatal(err)
	}
}

func TestOnDryRunChangesNothing(t *testing.T) {
	a, fake, out, _ := newTestApp(t)
	code := a.run([]string{"on", "--dry-run"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nout: %s\nerr: %s", code, out, a.errOut)
	}
	if a.store.Exists() || a.store.Active() {
		t.Error("dry-run wrote state or marker")
	}
	if len(fake.writes) != 0 || len(fake.removed) != 0 {
		t.Errorf("dry-run changed host: writes=%v removed=%v", fake.writes, fake.removed)
	}
	if _, err := os.Stat(a.configPath); !os.IsNotExist(err) {
		t.Errorf("dry-run created config: %v", err)
	}
	if !strings.Contains(out.String(), "brightness") {
		t.Errorf("dry-run output missing changes:\n%s", out)
	}
	if fake.primed {
		t.Error("dry-run asked for sudo")
	}
}

func TestOnRefusesLowBattery(t *testing.T) {
	a, fake, _, errOut := newTestApp(t)
	fake.files["/sys/class/power_supply/BAT0/capacity"] = "10\n"

	code := a.run([]string{"on"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "battery_exit_percent") {
		t.Errorf("error does not mention threshold:\n%s", errOut)
	}
	if a.store.Exists() || a.store.Active() {
		t.Error("state written despite refusal")
	}
	if len(fake.writes) != 0 {
		t.Errorf("host changed despite refusal: %v", fake.writes)
	}
	if fake.primed {
		t.Error("sudo was requested despite refusal")
	}
	if _, err := os.Stat(a.configPath); !os.IsNotExist(err) {
		t.Errorf("config was created despite refusal: %v", err)
	}
}

func TestOnRefusesExistingState(t *testing.T) {
	a, fake, _, errOut := newTestApp(t)
	saveTestState(t, a.store)

	code := a.run([]string{"on"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "state already exists") {
		t.Errorf("error = %s", errOut)
	}
	if _, err := a.store.Load(); err != nil {
		t.Errorf("existing state was damaged: %v", err)
	}
	if len(fake.writes) != 0 {
		t.Errorf("host changed despite refusal: %v", fake.writes)
	}
	if fake.primed {
		t.Error("sudo was requested despite refusal")
	}
}

func TestOnSavesSnapshotAndContinuesAfterFailure(t *testing.T) {
	a, fake, out, errOut := newTestApp(t)
	fake.writeErrs["/sys/devices/system/cpu/cpufreq/policy0/scaling_governor"] = errBoom

	code := a.run([]string{"on"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nout: %s\nerr: %s", code, out, errOut)
	}
	if !a.store.Exists() {
		t.Fatal("state was not saved before changes")
	}
	if !a.store.Active() {
		t.Fatal("active marker was not written")
	}
	if !strings.Contains(errOut.String(), "governor") {
		t.Errorf("failure not reported:\n%s", errOut)
	}
	if !fake.wrote("/sys/class/backlight/intel_backlight/brightness", "0") {
		t.Errorf("later settings were not applied: %v", fake.writes)
	}
	if !fake.ran("systemctl isolate multi-user.target") {
		t.Error("target was not isolated after an earlier failure")
	}
	if !fake.primed {
		t.Error("sudo was not primed")
	}
}

func TestOffRestoresStateAndDeletesIt(t *testing.T) {
	a, fake, out, errOut := newTestApp(t)
	saveTestState(t, a.store)

	code := a.run([]string{"off"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nout: %s\nerr: %s", code, out, errOut)
	}
	if a.store.Exists() {
		t.Error("state file still exists")
	}
	if a.store.Active() {
		t.Error("active marker still exists")
	}
	if !strings.Contains(out.String(), "desktop mode restored") {
		t.Errorf("output = %s", out)
	}
	if !fake.wrote("/sys/class/backlight/intel_backlight/brightness", "500") {
		t.Errorf("brightness not restored: %v", fake.writes)
	}
	if !fake.ran("systemctl isolate graphical.target") {
		t.Error("graphical.target not isolated")
	}
}

func TestOffWithoutStateUsesFallback(t *testing.T) {
	a, fake, out, errOut := newTestApp(t)
	if err := a.store.MarkActive(); err != nil {
		t.Fatal(err)
	}

	code := a.run([]string{"off"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nout: %s\nerr: %s", code, out, errOut)
	}
	if !strings.Contains(errOut.String(), "no state file") || !strings.Contains(errOut.String(), "config defaults") {
		t.Errorf("warning missing:\n%s", errOut)
	}
	if a.store.Active() {
		t.Error("active marker still exists")
	}
	if !fake.wrote("/sys/class/backlight/intel_backlight/brightness", "1000") {
		t.Errorf("brightness fallback not applied: %v", fake.writes)
	}
	lid, ok := fake.writeFor(lidDropInPath)
	if !ok || !strings.Contains(lid, "HandleLidSwitch=suspend") {
		t.Errorf("lid fallback = %q, %v", lid, ok)
	}
	if !fake.ran("systemctl isolate graphical.target") {
		t.Error("graphical.target not isolated")
	}
}

func TestOffKeepsStateWhenRestoreFails(t *testing.T) {
	a, fake, _, errOut := newTestApp(t)
	saveTestState(t, a.store)
	fake.writeErrs["/sys/class/backlight/intel_backlight/brightness"] = errBoom

	code := a.run([]string{"off"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !a.store.Exists() {
		t.Error("state was deleted despite a failed restore")
	}
	if a.store.Active() {
		t.Error("active marker should still be cleared in desktop mode")
	}
	if !strings.Contains(errOut.String(), "brightness") {
		t.Errorf("failure not reported:\n%s", errOut)
	}
	if !strings.Contains(errOut.String(), "state kept") {
		t.Errorf("missing retry hint:\n%s", errOut)
	}
	if !fake.wrote("/sys/devices/system/cpu/cpufreq/policy0/scaling_governor", "powersave") {
		t.Errorf("later restores did not run: %v", fake.writes)
	}
}

func TestOffDryRunKeepsState(t *testing.T) {
	a, fake, _, _ := newTestApp(t)
	saveTestState(t, a.store)

	code := a.run([]string{"off", "--dry-run"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !a.store.Exists() || !a.store.Active() {
		t.Error("dry-run removed state or marker")
	}
	if len(fake.writes) != 0 || len(fake.removed) != 0 {
		t.Errorf("dry-run changed host: writes=%v removed=%v", fake.writes, fake.removed)
	}
	if fake.primed {
		t.Error("dry-run asked for sudo")
	}
}

func TestStatusReportsPhoneMode(t *testing.T) {
	a, fake, out, errOut := newTestApp(t)
	if err := os.MkdirAll(filepath.Dir(a.configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.configPath, []byte("brightness = 10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveTestState(t, a.store)
	fake.outputs["systemctl show -p ActiveState --value -- graphical.target"] = "inactive"

	code := a.run([]string{"status"})
	if code != 0 {
		t.Fatalf("exit = %d\nout: %s\nerr: %s", code, out, errOut)
	}
	text := out.String()
	for _, want := range []string{
		"mode: phone (headless)",
		"state: ",
		"battery: 80% Discharging",
		"battery mode: on",
		"services: opencode-serve.service=on",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status missing %q:\n%s", want, text)
		}
	}
}

func TestUsage(t *testing.T) {
	a, _, _, _ := newTestApp(t)
	if code := a.run(nil); code != 2 {
		t.Errorf("no args exit = %d, want 2", code)
	}
	if code := a.run([]string{"bogus"}); code != 2 {
		t.Errorf("bad command exit = %d, want 2", code)
	}
	if code := a.run([]string{"help"}); code != 0 {
		t.Errorf("help exit = %d, want 0", code)
	}
}
