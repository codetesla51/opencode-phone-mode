package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const idleTestNow = "2026-11-23T12:00:00Z"

func newIdleTestApp(t *testing.T) (*app, *fakeHost, *bytes.Buffer, *bytes.Buffer, time.Time, string) {
	t.Helper()
	a, fake, out, errOut := newTestApp(t)
	now, err := time.Parse(time.RFC3339, idleTestNow)
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	fake.outputs["systemctl show -p ActiveState --value -- graphical.target"] = "inactive"
	if err := a.store.MarkActive(); err != nil {
		t.Fatal(err)
	}

	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[idle]\nenabled = true\nquiet_minutes = 10\ncpu_max_freq_percent = 20\nactivity_log_dir = %q\n", logDir)
	if err := os.MkdirAll(filepath.Dir(a.configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, fake, out, errOut, now, logDir
}

func setBotLogTime(t *testing.T, fake *fakeHost, logDir string, when time.Time) string {
	t.Helper()
	path := filepath.Join(logDir, "bot-2026-11-23.log")
	if err := os.WriteFile(path, []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.globs[filepath.Join(logDir, botLogPattern)] = []string{path}
	fake.mtimes[path] = when
	return path
}

func TestIdleWaitsForQuietPeriod(t *testing.T) {
	a, fake, out, errOut, now, logDir := newIdleTestApp(t)
	setBotLogTime(t, fake, logDir, now.Add(-5*time.Minute))

	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("exit = %d, out: %s, err: %s", code, out, errOut)
	}
	if len(fake.writes) != 0 {
		t.Errorf("applied a change before quiet threshold: %v", fake.writes)
	}
	if a.store.IdleExists() {
		t.Error("idle state written before quiet threshold")
	}
	if !strings.Contains(out.String(), "waiting") {
		t.Errorf("output does not explain wait: %s", out)
	}
}

func TestIdleAppliesAfterQuietPeriod(t *testing.T) {
	a, fake, out, errOut, now, logDir := newIdleTestApp(t)
	setBotLogTime(t, fake, logDir, now.Add(-15*time.Minute))

	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("exit = %d, out: %s, err: %s", code, out, errOut)
	}
	path := "/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq"
	if !fake.wrote(path, "400000") {
		t.Errorf("frequency not capped at 20%%: %v", fake.writes)
	}
	saved, err := a.store.LoadIdle()
	if err != nil {
		t.Fatalf("idle state not saved: %v", err)
	}
	if len(saved.MaxFreq) != 1 || saved.MaxFreq[0].Path != path || saved.MaxFreq[0].Value != "2000000" {
		t.Errorf("saved wrong values: %+v", saved)
	}
	if !saved.AppliedAt.Equal(now) {
		t.Errorf("AppliedAt = %s, want %s", saved.AppliedAt, now)
	}
	if !fake.primed {
		t.Error("root was not primed before privileged write")
	}
}

func TestIdleDryRunChangesNothing(t *testing.T) {
	a, fake, out, errOut, now, logDir := newIdleTestApp(t)
	setBotLogTime(t, fake, logDir, now.Add(-15*time.Minute))

	if code := a.run([]string{"idle", "--dry-run"}); code != 0 {
		t.Fatalf("exit = %d, out: %s, err: %s", code, out, errOut)
	}
	if len(fake.writes) != 0 || a.store.IdleExists() || fake.primed {
		t.Error("dry run changed host, state, or asked for sudo")
	}
	if !strings.Contains(out.String(), "2000000/2000000") || !strings.Contains(out.String(), "400000/2000000") {
		t.Errorf("dry run did not show current -> new: %s", out)
	}
}

func TestIdleRestoresOnActivity(t *testing.T) {
	a, fake, out, errOut, now, logDir := newIdleTestApp(t)
	setBotLogTime(t, fake, logDir, now.Add(-15*time.Minute))
	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("apply exit = %d, err: %s", code, errOut)
	}
	setBotLogTime(t, fake, logDir, now)

	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("restore exit = %d, out: %s, err: %s", code, out, errOut)
	}
	path := "/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq"
	if !fake.wrote(path, "2000000") {
		t.Errorf("original frequency not restored: %v", fake.writes)
	}
	if a.store.IdleExists() {
		t.Error("idle state kept after successful restore")
	}
}

func TestIdleRestoreFailureKeepsState(t *testing.T) {
	a, fake, _, errOut, now, logDir := newIdleTestApp(t)
	setBotLogTime(t, fake, logDir, now.Add(-15*time.Minute))
	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("apply exit = %d, err: %s", code, errOut)
	}
	fake.writeErrs["/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq"] = errBoom
	setBotLogTime(t, fake, logDir, now)

	if code := a.run([]string{"idle"}); code != 1 {
		t.Fatalf("exit = %d, want 1; err: %s", code, errOut)
	}
	if !a.store.IdleExists() {
		t.Error("idle state removed after failed restore")
	}
	if !strings.Contains(errOut.String(), "restore idle policy0") {
		t.Errorf("failure not reported: %s", errOut)
	}
}

func TestIdleIgnoresDesktopMode(t *testing.T) {
	a, fake, out, _, now, logDir := newIdleTestApp(t)
	fake.outputs["systemctl show -p ActiveState --value -- graphical.target"] = "active"
	setBotLogTime(t, fake, logDir, now.Add(-15*time.Minute))

	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(fake.writes) != 0 {
		t.Errorf("changed frequency outside phone mode: %v", fake.writes)
	}
	if !strings.Contains(out.String(), "phone mode is not active") {
		t.Errorf("output = %s", out)
	}
}

func TestIdleDisabled(t *testing.T) {
	a, fake, out, _, now, logDir := newIdleTestApp(t)
	setBotLogTime(t, fake, logDir, now.Add(-15*time.Minute))
	data, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "enabled = true", "enabled = false", 1))
	if err := os.WriteFile(a.configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(fake.writes) != 0 {
		t.Errorf("changed frequency while disabled: %v", fake.writes)
	}
	if !strings.Contains(out.String(), "disabled") {
		t.Errorf("output = %s", out)
	}
}

func TestIdleWithoutBotLogsDoesNotApply(t *testing.T) {
	a, fake, out, _, _, _ := newIdleTestApp(t)
	if code := a.run([]string{"idle"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(fake.writes) != 0 || a.store.IdleExists() {
		t.Error("applied profile without an activity signal")
	}
	if !strings.Contains(out.String(), "no bot logs") {
		t.Errorf("output = %s", out)
	}
}

func TestIdleUsesNewestBotLog(t *testing.T) {
	a, fake, _, _, now, logDir := newIdleTestApp(t)
	oldPath := setBotLogTime(t, fake, logDir, now.Add(-2*time.Hour))
	newPath := filepath.Join(logDir, "bot-2026-11-22.log")
	if err := os.WriteFile(newPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.globs[filepath.Join(logDir, botLogPattern)] = []string{oldPath, newPath}
	fake.mtimes[newPath] = now.Add(-3 * time.Minute)

	last, found, errs := lastBotActivity(loadIdleTestConfig(t, a), a.host)
	if len(errs) != 0 || !found {
		t.Fatalf("found = %v, errs = %v", found, errs)
	}
	if !last.Equal(now.Add(-3 * time.Minute)) {
		t.Errorf("last = %s, want the newest log", last)
	}
}

func loadIdleTestConfig(t *testing.T, a *app) Config {
	t.Helper()
	cfg, _, err := LoadConfig(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOnClearsStaleIdleState(t *testing.T) {
	a, _, _, errOut, _, _ := newIdleTestApp(t)
	if err := a.store.SaveIdle(IdleState{Version: idleVersion, AppliedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a.store.statePath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if code := a.run([]string{"on"}); code != 0 {
		t.Fatalf("exit = %d, err: %s", code, errOut)
	}
	if a.store.IdleExists() {
		t.Error("stale idle state survived on")
	}
}

func TestOffClearsIdleState(t *testing.T) {
	a, _, _, errOut, _, _ := newIdleTestApp(t)
	saveTestState(t, a.store)
	if err := a.store.SaveIdle(IdleState{Version: idleVersion, AppliedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if code := a.run([]string{"off"}); code != 0 {
		t.Fatalf("exit = %d, err: %s", code, errOut)
	}
	if a.store.IdleExists() {
		t.Error("idle state survived off")
	}
}
