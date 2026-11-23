package main

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	saved := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	want := State{
		Version: stateVersion,
		SavedAt: saved,
		Brightness: &SysValue{
			Path:  "/sys/class/backlight/intel_backlight/brightness",
			Value: "500",
		},
		Lid: &LidState{
			Path:      lidDropInPath,
			Existed:   true,
			Content:   "[Login]\nHandleLidSwitch=lock\n",
			Effective: map[string]string{"HandleLidSwitch": "lock"},
		},
		Governor: []SysValue{{Path: "/sys/policy0/scaling_governor", Value: "powersave"}},
		MaxFreq:  []SysValue{{Path: "/sys/policy0/scaling_max_freq", Value: "2000000"}},
		Turbo:    &SysValue{Path: "/sys/intel_pstate/no_turbo", Value: "0"},
		Radios: []RadioState{
			{Name: "hci0", Type: "bluetooth", SoftBlocked: false},
			{Name: "phy0", Type: "wlan", SoftBlocked: true},
		},
		Bluetooth: &ServiceState{Name: "bluetooth.service", Scope: "system", Active: true},
		Services: []ServiceState{
			{Name: "agent.service", Scope: "user", Active: true, Enabled: "enabled"},
		},
		Linger:   boolPtr(false),
		DGPU:     []SysValue{{Path: "/sys/gpu/power/control", Value: "on"}},
		BBSwitch: &SysValue{Path: "/proc/acpi/bbswitch", Value: "0000:01:00.0 ON"},
		Nvidia:   &ServiceState{Name: "nvidia-persistenced.service", Scope: "system", Active: true},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestStateMissing(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	_, err := store.Load()
	if !errors.Is(err, errNoState) {
		t.Fatalf("Load error = %v, want errNoState", err)
	}
	if store.Exists() {
		t.Fatal("Exists = true, want false")
	}
	if err := store.Remove(); err != nil {
		t.Fatalf("Remove on missing state: %v", err)
	}
}

func TestStateRejectsWrongVersion(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	if err := store.Save(State{Version: 99, SavedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load accepted an unknown version")
	}
}

func TestStateMarker(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	if store.Active() {
		t.Fatal("Active = true before MarkActive")
	}
	if err := store.MarkActive(); err != nil {
		t.Fatalf("MarkActive: %v", err)
	}
	if !store.Active() {
		t.Fatal("Active = false after MarkActive")
	}
	if err := store.ClearActive(); err != nil {
		t.Fatalf("ClearActive: %v", err)
	}
	if store.Active() {
		t.Fatal("Active = true after ClearActive")
	}
}

func TestStateFileIsPrivate(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	if err := store.Save(State{Version: stateVersion, SavedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("state mode = %o, want 600", mode)
	}
}

func TestStateRemove(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	if err := store.Save(State{Version: stateVersion, SavedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if store.Exists() {
		t.Fatal("state still exists after Remove")
	}
}
