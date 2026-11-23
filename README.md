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

## Configuration

The file is `~/.config/opencode-phone-mode/config.toml`. Missing keys use the
defaults documented in [`config.example.toml`](config.example.toml). Unknown
keys are rejected so typos do not silently disable a feature.

Every behavior can be disabled:

- `manage_backlight`, `manage_governor`, `manage_max_freq`, `manage_turbo`
- `manage_lid`, `manage_radios`, `manage_services`, `isolate_target`
- `powertop`, `disable_dgpu`

`keep_services` lists systemd **user** units. Active units are left running;
inactive ones are enabled and started. They are never restarted, so `on` cannot
kill the session that launched it.

`keep_radios_on` lists rfkill types to leave enabled. `wifi` is accepted as an
alias for `wlan`. Every other radio type is soft-blocked.

If `off` runs without `state.json`, it prints a warning and uses the `[restore]`
section as the fallback desktop values. Restores are attempted independently;
if any step fails, the state file is kept so `off` can be retried.

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
