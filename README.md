# opencode-phone-mode

Switch a Linux laptop between its graphical desktop and a headless phone mode
for piloting OpenCode from a phone.

Phone mode snapshots every setting it touches, applies the power and radio
changes, and then isolates `multi-user.target`. Running `off` restores the
snapshot byte-for-byte and deletes it.

## Requirements

- Linux with systemd, sysfs cpufreq, and `rfkill`
- `sudo` for `on` and `off` (reads for `status` do not need it)
- Optional: `powertop` for `powertop --auto-tune`, `bbswitch` for dGPU power-off
- Go 1.22 or newer to build

## Build and install

```sh
make check
make install
```

This installs `~/.local/bin/opencode-phone-mode` and writes a default
`~/.config/opencode-phone-mode/config.toml` if one does not exist yet.

## Usage

Run it as your normal user, not with `sudo`. It asks for sudo only for the
privileged steps; running the whole binary as root would read root's config and
operate on root's user services.

```sh
opencode-phone-mode on
opencode-phone-mode status
opencode-phone-mode off

opencode-phone-mode on --dry-run
opencode-phone-mode off --dry-run
```

`--dry-run` prints every change as `setting: current -> new`, makes no
changes, does not ask for sudo, and does not write state.

`on` refuses to run when `state.json` already exists, because snapshotting
again would overwrite the values needed to restore the desktop. Run `off`
first, or delete the stale state file yourself.

`on` also refuses to go headless when the battery is below
`battery_exit_percent` and the machine is not charging. `status` prints the
same warning without blocking anything.

## Battery life

The main saving is the display and the desktop session, not the CPU knobs.
On this machine (HP laptop, 34.6 Wh battery, iGPU only), a discharge run in
phone-mode-style idle use went from 65% to 6% in 9h15m:

- roughly **6.4% per hour**
- roughly **2.2 W average**
- extrapolated **14-15 hours** from 100% down to near-empty

A 30-minute check at 65% while piloting showed the gauge barely moving. At
~2 W that is under 2%, so a flat reading is expected rather than surprising.

What actually saves power here:

- backlight at 0 and no compositor (`multi-user.target`)
- Bluetooth off
- the `active` marker telling agents to stay light, so builds and test suites
  do not start in the background

What does not necessarily save energy:

- the governor is already `powersave` on this machine, so that step changes
  nothing
- capping max frequency to 40% lowers peak power and heat, but for a fixed
  amount of work it mostly makes the work take longer. "Race to idle" can use
  less total energy. This is why the cap is configurable.

These numbers come from one discharge run on one machine and are not a
guarantee. Battery wear, Wi-Fi traffic, and workload change the result. The
watt column in `battery.log` repeats a constant value and is not usable; the
percentage-over-time is the trustworthy part. To measure your own, charge to
full, run phone mode, and check `status` at the same time each hour.

`battery_exit_percent` defaults to 15: below that and unplugged, `on` refuses
so the phone link survives. At ~14-15 hours total, 15% leaves roughly two hours
of headroom. Lower it if you want more runway.

## Configuration

The file is `~/.config/opencode-phone-mode/config.toml`. Every key is optional:
omitted keys use the default shown in [`config.example.toml`](config.example.toml),
which is also the template installed by `make install`. Unknown keys are
rejected so typos do not silently disable a feature. Values are validated when
the config is loaded.

### Main options

| Key | Default | Values and effect |
|---|---|---|
| `keep_services` | `["opencode-serve.service", "opencode-telegram.service"]` | systemd **user** units to survive the switch. Active units are left running; inactive ones are enabled and started. Never restarted, so `on` cannot kill the session that launched it. |
| `keep_radios_on` | `["wlan"]` | rfkill types left unblocked: `wlan`, `bluetooth`, `wwan`, `gps`, `nfc`, `ufs`, `wimax`. `wifi` is an alias for `wlan`. Every other type is soft-blocked. |
| `manage_backlight` | `true` | `false` leaves the backlight untouched. |
| `brightness` | `0` | Backlight percentage, 0-100. `-1` leaves it untouched. |
| `backlight_device` | `""` | Device name under `/sys/class/backlight`, e.g. `intel_backlight`. Empty picks the first device. |
| `manage_governor` | `true` | `false` leaves the governor untouched. |
| `governor` | `"powersave"` | Any governor from `scaling_available_governors`, e.g. `powersave`, `performance`, `schedutil`, `ondemand`. |
| `manage_max_freq` | `true` | `false` leaves `scaling_max_freq` untouched. |
| `cpu_max_freq_percent` | `40` | Cap each CPU policy to 1-100% of its hardware maximum; clamped at `scaling_min_freq`. |
| `manage_turbo` | `true` | `false` leaves turbo untouched. |
| `turbo` | `false` | Turbo on/off. |
| `powertop` | `true` | Run `powertop --auto-tune`. Needs `powertop` installed and has no portable undo. `false` skips it. |
| `disable_dgpu` | `true` | Runtime-suspend non-boot GPUs, plus bbswitch/nvidia-persistenced when present. `false` leaves GPU settings alone. |
| `manage_lid` | `true` | `false` leaves logind lid settings untouched. |
| `lid_switch` | `"ignore"` | `ignore`, `suspend`, `hibernate`, `hybrid-sleep`, `suspend-then-hibernate`, `poweroff`, `reboot`, `halt`, `kexec`, `lock`. |
| `manage_radios` | `true` | `false` leaves every rfkill device as it was. |
| `manage_services` | `true` | `false` leaves linger and `keep_services` as they were. |
| `isolate_target` | `true` | `false` keeps the current target; no isolation happens. |
| `battery_exit_percent` | `15` | 0-100; `0` disables the guard. `status` warns below it and `on` refuses while discharging. Charging/full always pass. |

Any `manage_*` switch set to `false` means that setting is neither snapshotted
nor changed, so `off` will not restore it either.

### Restore fallback (`[restore]`)

Used only when `off` runs without `state.json`. With a state file present,
`off` restores the recorded values exactly and ignores this table.

| Key | Default | Effect |
|---|---|---|
| `brightness` | `100` | Backlight percentage, 0-100. |
| `governor` | `""` | Governor name, or empty to leave it alone. |
| `cpu_max_freq_percent` | `100` | Frequency cap, 1-100% of hardware maximum. |
| `turbo` | `true` | Turbo on/off. |
| `lid_switch` | `"suspend"` | Lid action (same values as above), or empty to remove our drop-in and use the system default. |
| `bluetooth` | `true` | Leave Bluetooth unblocked and its service running. |
| `services_running` | `true` | Enable linger and start `keep_services`. |

`off` prints a warning when it falls back to these values. Restores are
attempted independently; if any step fails, the state file is kept so `off`
can be retried. Use `on --dry-run` and `off --dry-run` to preview the effect of
a config change before applying it.

## What is snapshotted

- backlight raw value
- effective lid action and the exact contents of
  `/etc/systemd/logind.conf.d/99-opencode-phone-mode.conf`
- every cpufreq policy's governor and `scaling_max_freq`
- turbo control value (`no_turbo` or `boost`)
- rfkill soft-block state per device
- Bluetooth service state
- `keep_services` active/enabled state and logind linger
- non-boot GPU `power/control`, `bbswitch`, and `nvidia-persistenced`
- the phone-mode marker at `~/.cache/opencode-phone-mode/active`

## Caveats

- `powertop --auto-tune` changes kernel tunables with no portable undo. `off`
  cannot restore them; disable `powertop` if that matters.
- "Discrete GPU off" uses runtime suspend (`power/control=auto`) and, when
  present, `bbswitch` plus stopping `nvidia-persistenced`. Vendor-specific
  power-off tools are not invoked.
- `on` isolates `multi-user.target` as its last step. If the command is running
  from a graphical terminal, that terminal closes.
- If a keep_service was inactive before `on`, `off` stops it to restore the
  snapshot. If that unit hosts your session, run `off` from a terminal or SSH
  so the stop cannot interrupt the restore.
- The old `active` marker is still maintained alongside `state.json` for the
  existing agent battery-mode rule.

## Tests

```sh
make test
```

The tests use a fake host and cover config parsing and validation, snapshot
round-trips, dry-run behavior, fallback restores, exact lid-file restoration,
and continue-after-failure semantics. See [`TEST_NOTES.md`](TEST_NOTES.md) for
known gaps.
