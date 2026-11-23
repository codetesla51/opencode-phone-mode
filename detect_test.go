package main

import (
	"encoding/json"
	"testing"
)

func TestRfkillFlagParsing(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
		ok   bool
	}{
		{"string blocked", `"blocked"`, true, true},
		{"string unblocked", `"unblocked"`, false, true},
		{"bool true", `true`, true, true},
		{"bool false", `false`, false, true},
		{"garbage", `"maybe"`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var flag rfkillFlag
			err := json.Unmarshal([]byte(tt.data), &flag)
			if tt.ok && err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !tt.ok {
				if err == nil {
					t.Fatal("Unmarshal succeeded, want error")
				}
				return
			}
			if bool(flag) != tt.want {
				t.Errorf("flag = %v, want %v", bool(flag), tt.want)
			}
		})
	}
}

func TestMatchRadioFallsBackToType(t *testing.T) {
	current := []radio{{ID: 3, Name: "hci1", Type: "bluetooth"}}
	got := matchRadio(current, RadioState{Name: "hci0", Type: "bluetooth"})
	if got == nil {
		t.Fatal("matchRadio did not fall back to type")
	}
	if selector := rfkillSelector(*got); selector != "3" {
		t.Errorf("selector = %q, want numeric ID 3", selector)
	}
}

func TestListRadios(t *testing.T) {
	radios, err := listRadios(newFakeHost())
	if err != nil {
		t.Fatalf("listRadios: %v", err)
	}
	if len(radios) != 2 {
		t.Fatalf("got %d radios, want 2", len(radios))
	}
	if radios[0].Name != "hci0" || radios[0].Type != "bluetooth" || radios[0].SoftBlocked {
		t.Errorf("first radio = %+v", radios[0])
	}
	if radios[1].Name != "phy0" || radios[1].Type != "wlan" || radios[1].SoftBlocked {
		t.Errorf("second radio = %+v", radios[1])
	}
}

func TestRadioKeptAcceptsWifiAlias(t *testing.T) {
	cfg := DefaultConfig()
	cfg.KeepRadiosOn = []string{"WiFi"}
	if !radioKept(cfg, "wlan") {
		t.Error("wifi alias did not keep wlan")
	}
	if radioKept(cfg, "bluetooth") {
		t.Error("wifi alias kept bluetooth")
	}
}

func TestEffectiveLidActions(t *testing.T) {
	fake := newFakeHost()
	fake.outputs["systemd-analyze cat-config systemd/logind.conf"] = `
[Login]
#HandleLidSwitch=suspend
HandleLidSwitch=lock
HandleLidSwitchExternalPower=ignore
`
	actions := effectiveLidActions(fake)
	if actions["HandleLidSwitch"] != "lock" {
		t.Errorf("HandleLidSwitch = %q, want lock", actions["HandleLidSwitch"])
	}
	if actions["HandleLidSwitchExternalPower"] != "ignore" {
		t.Errorf("ExternalPower = %q, want ignore", actions["HandleLidSwitchExternalPower"])
	}
	if actions["HandleLidSwitchDocked"] != "ignore" {
		t.Errorf("Docked = %q, want default ignore", actions["HandleLidSwitchDocked"])
	}
}

func TestBatteryState(t *testing.T) {
	pct, status, ok := batteryState(newFakeHost())
	if !ok {
		t.Fatal("batteryState not ok")
	}
	if pct != 80 || status != "Discharging" {
		t.Errorf("got %d%% %q, want 80%% Discharging", pct, status)
	}
}

func TestBatteryBelowExit(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		pct       int
		status    string
		want      bool
	}{
		{"below discharging", 15, 10, "Discharging", true},
		{"at threshold", 15, 15, "Discharging", false},
		{"above", 15, 80, "Discharging", false},
		{"below but charging", 15, 10, "Charging", false},
		{"below but full", 15, 10, "Full", false},
		{"threshold disabled", 0, 1, "Discharging", false},
		{"unknown capacity", 15, 0, "Discharging", false},
		{"unknown status", 15, 10, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := batteryBelowExit(tt.threshold, tt.pct, tt.status); got != tt.want {
				t.Errorf("batteryBelowExit = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCapFrequency(t *testing.T) {
	tests := []struct {
		name string
		max  uint64
		min  uint64
		pct  int
		want uint64
	}{
		{"half", 3000000, 400000, 50, 1500000},
		{"clamped to min", 3000000, 400000, 10, 400000},
		{"full", 3000000, 400000, 100, 3000000},
		{"no min", 2000000, 0, 40, 800000},
		{"min above max", 1000, 2000, 50, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capFrequency(tt.max, tt.min, tt.pct); got != tt.want {
				t.Errorf("capFrequency = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTurboMapping(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		inverted bool
		value    string
	}{
		{"intel on", true, true, "0"},
		{"intel off", false, true, "1"},
		{"boost on", true, false, "1"},
		{"boost off", false, false, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := turboValue(tt.enabled, tt.inverted); got != tt.value {
				t.Errorf("turboValue = %q, want %q", got, tt.value)
			}
			if got := turboEnabled(tt.value, tt.inverted); got != tt.enabled {
				t.Errorf("turboEnabled = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestDiscreteGPUsSkipsBootVGA(t *testing.T) {
	fake := newFakeHost()
	fake.globs["/sys/bus/pci/devices/*"] = []string{
		"/sys/bus/pci/devices/0000:00:02.0",
		"/sys/bus/pci/devices/0000:01:00.0",
		"/sys/bus/pci/devices/0000:02:00.0",
	}
	fake.files["/sys/bus/pci/devices/0000:00:02.0/class"] = "0x030000\n"
	fake.files["/sys/bus/pci/devices/0000:00:02.0/boot_vga"] = "1\n"
	fake.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x030200\n"
	fake.files["/sys/bus/pci/devices/0000:01:00.0/boot_vga"] = "0\n"
	fake.files["/sys/bus/pci/devices/0000:02:00.0/class"] = "0x020000\n"

	gpus, err := discreteGPUs(fake)
	if err != nil {
		t.Fatalf("discreteGPUs: %v", err)
	}
	if len(gpus) != 1 || gpus[0] != "/sys/bus/pci/devices/0000:01:00.0" {
		t.Errorf("gpus = %v, want only the non-boot 3D controller", gpus)
	}
}

func TestLingerEnabled(t *testing.T) {
	fake := newFakeHost()
	fake.outputs["loginctl show-user testuser -p Linger --value"] = "yes"
	enabled, err := lingerEnabled(fake, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("lingerEnabled = false, want true")
	}
}
