package main

import (
	"io"
	"strings"
	"testing"
)

func phoneConfig() Config {
	cfg := DefaultConfig()
	cfg.Brightness = 10
	cfg.CPUMaxFreqPercent = 50
	return cfg
}

func TestBuildOnPlanSnapshotsBeforeChanges(t *testing.T) {
	fake := newFakeHost()
	cfg := phoneConfig()

	p, errs := buildOnPlan(cfg, fake)
	if len(errs) != 0 {
		t.Fatalf("snapshot errors: %v", errs)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("buildOnPlan wrote before returning: %v", fake.writes)
	}

	if p.state.Brightness == nil || p.state.Brightness.Value != "500" {
		t.Errorf("brightness snapshot = %+v, want 500", p.state.Brightness)
	}
	if len(p.state.Governor) != 1 || p.state.Governor[0].Value != "powersave" {
		t.Errorf("governor snapshot = %+v", p.state.Governor)
	}
	if len(p.state.MaxFreq) != 1 || p.state.MaxFreq[0].Value != "2000000" {
		t.Errorf("max freq snapshot = %+v", p.state.MaxFreq)
	}
	if p.state.Turbo == nil || p.state.Turbo.Value != "0" {
		t.Errorf("turbo snapshot = %+v, want 0", p.state.Turbo)
	}
	if p.state.Lid == nil || p.state.Lid.Existed {
		t.Errorf("lid snapshot = %+v, want not existed", p.state.Lid)
	}
	if len(p.state.Radios) != 2 {
		t.Errorf("radios snapshot = %+v, want 2", p.state.Radios)
	}
	if p.state.Bluetooth == nil || !p.state.Bluetooth.Active {
		t.Errorf("bluetooth snapshot = %+v, want active", p.state.Bluetooth)
	}
	if len(p.state.Services) != 2 {
		t.Errorf("services snapshot = %+v, want 2", p.state.Services)
	}
	if p.state.Linger == nil || *p.state.Linger {
		t.Errorf("linger snapshot = %+v, want false", p.state.Linger)
	}

	if errs := runSteps(p.steps, false, io.Discard); len(errs) != 0 {
		t.Fatalf("runSteps: %v", errs)
	}

	if !fake.wrote("/sys/class/backlight/intel_backlight/brightness", "100") {
		t.Errorf("brightness not set to 10%%: %v", fake.writes)
	}
	if !fake.wrote("/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq", "1000000") {
		t.Errorf("max freq not capped to 1 GHz: %v", fake.writes)
	}
	if !fake.wrote("/sys/devices/system/cpu/intel_pstate/no_turbo", "1") {
		t.Errorf("turbo not disabled: %v", fake.writes)
	}
	if !fake.wrote("/sys/devices/system/cpu/cpufreq/policy0/scaling_governor", "powersave") {
		t.Errorf("governor not written: %v", fake.writes)
	}
	lid, ok := fake.writeFor(lidDropInPath)
	if !ok || !strings.Contains(lid, "HandleLidSwitch=ignore") {
		t.Errorf("lid drop-in = %q, %v", lid, ok)
	}
	if !fake.ran("rfkill block hci0") {
		t.Error("bluetooth radio was not blocked")
	}
	if fake.ran("rfkill block phy0") {
		t.Error("wifi radio was blocked")
	}
	if !fake.ran("systemctl stop bluetooth.service") {
		t.Error("bluetooth.service was not stopped")
	}
	if !fake.ran("loginctl enable-linger testuser") {
		t.Error("linger was not enabled")
	}
	if fake.ran("systemctl --user restart -- opencode-serve.service") {
		t.Error("an already active service was restarted; this can kill the calling session")
	}
	if !fake.ran("systemctl isolate multi-user.target") {
		t.Error("multi-user.target was not isolated")
	}
}

func TestBuildOnPlanSkipsSettingWhenSnapshotFails(t *testing.T) {
	fake := newFakeHost()
	delete(fake.files, "/sys/class/backlight/intel_backlight/brightness")

	p, errs := buildOnPlan(phoneConfig(), fake)
	if len(errs) == 0 {
		t.Fatal("expected a snapshot error for brightness")
	}
	if p.state.Brightness != nil {
		t.Errorf("brightness snapshot = %+v, want nil", p.state.Brightness)
	}
	for _, s := range p.steps {
		if s.setting == "brightness" {
			t.Fatal("brightness step planned without a snapshot")
		}
	}
	if len(p.state.Governor) == 0 {
		t.Error("other settings were not snapshotted")
	}
}

func TestBuildOnPlanStartsInactiveKeepService(t *testing.T) {
	fake := newFakeHost()
	fake.outputs["systemctl --user show -p ActiveState --value -- opencode-serve.service"] = "inactive"

	p, errs := buildOnPlan(phoneConfig(), fake)
	if len(errs) != 0 {
		t.Fatalf("snapshot errors: %v", errs)
	}
	if errs := runSteps(p.steps, false, io.Discard); len(errs) != 0 {
		t.Fatalf("runSteps: %v", errs)
	}
	if !fake.ran("systemctl --user start -- opencode-serve.service") {
		t.Errorf("inactive service was not started: %v", fake.runs)
	}
}

func TestRunStepsDryRunMakesNoChanges(t *testing.T) {
	called := false
	steps := []step{{
		setting: "x",
		current: "a",
		next:    "b",
		apply:   func() error { called = true; return nil },
	}}
	errs := runSteps(steps, true, io.Discard)
	if len(errs) != 0 {
		t.Fatalf("runSteps: %v", errs)
	}
	if called {
		t.Fatal("apply ran during dry-run")
	}
}

func TestRunStepsContinuesAfterFailure(t *testing.T) {
	second := false
	steps := []step{
		{setting: "first", current: "a", next: "b", apply: func() error { return errBoom }},
		{setting: "second", current: "a", next: "b", apply: func() error { second = true; return nil }},
	}
	errs := runSteps(steps, false, io.Discard)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "first") {
		t.Fatalf("errs = %v, want one first-step error", errs)
	}
	if !second {
		t.Fatal("second step did not run after the first failed")
	}
}

func fullState() State {
	return State{
		Version:    stateVersion,
		Brightness: &SysValue{Path: "/sys/class/backlight/intel_backlight/brightness", Value: "500"},
		Lid: &LidState{
			Path:      lidDropInPath,
			Existed:   false,
			Effective: map[string]string{"HandleLidSwitch": "suspend"},
		},
		Governor: []SysValue{{Path: "/sys/devices/system/cpu/cpufreq/policy0/scaling_governor", Value: "powersave"}},
		MaxFreq:  []SysValue{{Path: "/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq", Value: "2000000"}},
		Turbo:    &SysValue{Path: "/sys/devices/system/cpu/intel_pstate/no_turbo", Value: "0"},
		Radios: []RadioState{
			{Name: "hci0", Type: "bluetooth", SoftBlocked: false},
			{Name: "phy0", Type: "wlan", SoftBlocked: false},
		},
		Bluetooth: &ServiceState{Name: "bluetooth.service", Scope: "system", Active: true},
		Services: []ServiceState{
			{Name: "opencode-serve.service", Scope: "user", Active: true, Enabled: "disabled"},
			{Name: "opencode-telegram.service", Scope: "user", Active: false, Enabled: "enabled"},
		},
		Linger: boolPtr(false),
	}
}

func TestBuildOffPlanRestoresExactValues(t *testing.T) {
	fake := newFakeHost()
	steps, errs := buildOffPlan(phoneConfig(), fullState(), fake)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if errs := runSteps(steps, false, io.Discard); len(errs) != 0 {
		t.Fatalf("runSteps: %v", errs)
	}

	if !fake.wrote("/sys/class/backlight/intel_backlight/brightness", "500") {
		t.Errorf("brightness not restored: %v", fake.writes)
	}
	if !fake.wrote("/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq", "2000000") {
		t.Errorf("max freq not restored: %v", fake.writes)
	}
	if !fake.wrote("/sys/devices/system/cpu/intel_pstate/no_turbo", "0") {
		t.Errorf("turbo not restored: %v", fake.writes)
	}
	if !fake.wrote("/sys/devices/system/cpu/cpufreq/policy0/scaling_governor", "powersave") {
		t.Errorf("governor not restored: %v", fake.writes)
	}
	if len(fake.removed) != 1 || fake.removed[0] != lidDropInPath {
		t.Errorf("lid drop-in removal = %v", fake.removed)
	}
	if !fake.ran("rfkill unblock hci0") || !fake.ran("rfkill unblock phy0") {
		t.Errorf("radios not unblocked: %v", fake.runs)
	}
	if !fake.ran("systemctl start bluetooth.service") {
		t.Error("bluetooth.service not started")
	}
	if !fake.ran("systemctl --user disable -- opencode-serve.service") {
		t.Error("opencode-serve not disabled back")
	}
	if !fake.ran("systemctl --user start -- opencode-serve.service") {
		t.Error("opencode-serve not started back")
	}
	if !fake.ran("systemctl --user enable -- opencode-telegram.service") {
		t.Error("opencode-telegram not enabled back")
	}
	if !fake.ran("systemctl --user stop -- opencode-telegram.service") {
		t.Error("opencode-telegram not stopped back")
	}
	if !fake.ran("loginctl disable-linger testuser") {
		t.Error("linger not disabled back")
	}
	if !fake.ran("systemctl isolate graphical.target") {
		t.Error("graphical.target not isolated")
	}
}

func TestBuildOffPlanRestoresLidFileContent(t *testing.T) {
	fake := newFakeHost()
	st := fullState()
	st.Lid = &LidState{
		Path:    lidDropInPath,
		Existed: true,
		Content: "[Login]\nHandleLidSwitch=lock\n",
	}
	steps, errs := buildOffPlan(phoneConfig(), st, fake)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if errs := runSteps(steps, false, io.Discard); len(errs) != 0 {
		t.Fatalf("runSteps: %v", errs)
	}
	content, ok := fake.writeFor(lidDropInPath)
	if !ok {
		t.Fatal("lid drop-in was not restored")
	}
	if content != "[Login]\nHandleLidSwitch=lock\n" {
		t.Errorf("lid content = %q, want exact previous content", content)
	}
	if len(fake.removed) != 0 {
		t.Errorf("removed = %v, want none", fake.removed)
	}
}

func TestBuildFallbackPlanUsesRestoreDefaults(t *testing.T) {
	fake := newFakeHost()
	fake.outputs["systemctl --user show -p ActiveState --value -- opencode-serve.service"] = "inactive"
	fake.outputs["systemctl --user show -p UnitFileState --value -- opencode-serve.service"] = "disabled"
	fake.outputs["systemctl --user show -p ActiveState --value -- opencode-telegram.service"] = "inactive"
	fake.outputs["systemctl --user show -p UnitFileState --value -- opencode-telegram.service"] = "disabled"

	steps, errs := buildFallbackPlan(DefaultConfig(), fake)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if errs := runSteps(steps, false, io.Discard); len(errs) != 0 {
		t.Fatalf("runSteps: %v", errs)
	}

	if !fake.wrote("/sys/class/backlight/intel_backlight/brightness", "1000") {
		t.Errorf("brightness not restored to 100%%: %v", fake.writes)
	}
	lid, ok := fake.writeFor(lidDropInPath)
	if !ok || !strings.Contains(lid, "HandleLidSwitch=suspend") {
		t.Errorf("lid fallback = %q, %v", lid, ok)
	}
	if !fake.wrote("/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq", "2000000") {
		t.Errorf("max freq not restored to 100%%: %v", fake.writes)
	}
	if !fake.wrote("/sys/devices/system/cpu/intel_pstate/no_turbo", "0") {
		t.Errorf("turbo not restored: %v", fake.writes)
	}
	if !fake.ran("rfkill unblock hci0") || !fake.ran("rfkill unblock phy0") {
		t.Errorf("radios not unblocked: %v", fake.runs)
	}
	if !fake.ran("loginctl enable-linger testuser") {
		t.Error("linger not enabled")
	}
	if !fake.ran("systemctl --user start -- opencode-serve.service") {
		t.Error("service not started")
	}
	if !fake.ran("systemctl isolate graphical.target") {
		t.Error("graphical.target not isolated")
	}
}
