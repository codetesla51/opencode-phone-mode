# Test notes

## Contract

`on` reads every managed setting, writes the complete snapshot to `state.json`,
writes the `active` marker, then applies changes. A setting whose current value
cannot be read is not changed, because it could not be restored later.

`off` restores saved values verbatim, then deletes `state.json` if every step
succeeded. With no state file it warns and applies `[restore]` defaults. A
failed restore keeps the state file so `off` can be retried.

`--dry-run` prints every change and performs no writes, no sudo, and no state
changes. Individual step failures never stop later steps; they are collected
and reported at the end.

## Attacks covered

- Config: missing file, defaults, overrides, unknown keys, out-of-range
  brightness/frequency/battery values, invalid lid actions, path traversal in
  `backlight_device`, and drift between `config.example.toml` and
  `DefaultConfig`.
- State: round-trip of every field, missing file, unknown version, marker
  lifecycle, private file mode, deletion.
- Plan: snapshot happens before writes, failed snapshot skips that setting,
  dry-run calls no apply functions, dry-run changes no host state, a failed
  step still lets later steps run, exact per-device restore, lid file content
  restored byte-for-byte, stale lid file removal, fallback plan values.
- Battery threshold: below/at/above, charging/full bypass, disabled threshold,
  zero capacity, unknown status.
- Frequency cap: normal, min clamp, 100%, zero min, min greater than max.
- Turbo: both intel_pstate inverted and cpufreq boost mappings.
- rfkill: modern string and older boolean `soft` encodings.
- Lid: commented defaults ignored, explicit overrides win.
- dGPU detection: boot VGA skipped, 3D controllers detected, non-display
  devices ignored.

## Bugs found and fixed

- The initial `capFrequency` test expected `min` to win when `min > max`; the
  function correctly clamps to `max`. Test expectation fixed.
- Restarting `keep_services` during `on` could kill the session that launched
  the command when it ran from inside one of those units. `on` now starts
  inactive units and leaves active units alone.
- Fallback `off` hardcoded current target/service labels in its dry-run output.
  It now reads live state for the "current" column.
- Real `WriteRoot` appended a newline for every write, which would have broken
  byte-for-byte lid-file restoration. Removed; sysfs accepts the value
  without a newline.

## Known gaps

- No integration test runs the real host as root. All privileged writes and
  systemd/rfkill calls are exercised through a fake. The first real `on`
  should be treated as an integration test.
- The dGPU write path (`power/control`, bbswitch, `nvidia-persistenced`) has
  detection tests but no hardware test; this machine has no discrete GPU.
- `rfkill -J` parsing assumes the util-linux JSON shape. There is no text
  fallback for very old versions.
- `powertop --auto-tune` has no portable undo and is deliberately not
  snapshotted. `off` reports nothing for it.
- No fuzz tests. The parsers are small and table-tested, but malformed
  `systemctl`/`systemd-analyze` output is only covered by fallback paths.
- `systemctl show -p ... --value` output is assumed stable across systemd
  versions.
- Mutation testing was performed by hand on the highest-risk branch logic: the
  battery boundary, frequency clamp, dry-run guard, inactive-service start
  condition, state-deletion condition, brightness lower bound, and radio flag.
  All seven mutants were killed; no automated mutation threshold is enforced.

## Run

```sh
make check
# or
gofmt -l . && go vet ./... && go test -race -count=1 ./...
```
