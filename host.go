package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// host is the small slice of the machine the planner touches. Tests use a
// fake so snapshot/restore behavior can be exercised without root.
type host interface {
	Read(path string) (string, error)
	WriteRoot(path, value string) error
	RemoveRoot(path string) error
	MkdirRoot(path string) error
	Run(name string, args ...string) (string, error)
	RunRoot(name string, args ...string) (string, error)
	Glob(pattern string) ([]string, error)
	Look(name string) bool
	User() string
	PrimeRoot() error
}

type realHost struct {
	isRoot bool
}

func newRealHost() *realHost {
	return &realHost{isRoot: os.Geteuid() == 0}
}

func (h *realHost) Read(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (h *realHost) WriteRoot(path, value string) error {
	if err := h.MkdirRoot(filepath.Dir(path)); err != nil {
		return err
	}
	if h.isRoot {
		return os.WriteFile(path, []byte(value), 0o644)
	}
	cmd := exec.Command("sudo", "tee", path)
	cmd.Stdin = strings.NewReader(value)
	var out bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return commandError(cmd.Args, out.String(), err)
	}
	return nil
}

func (h *realHost) RemoveRoot(path string) error {
	if h.isRoot {
		return os.Remove(path)
	}
	_, err := h.run("sudo", "rm", "-f", path)
	return err
}

func (h *realHost) MkdirRoot(path string) error {
	if h.isRoot {
		return os.MkdirAll(path, 0o755)
	}
	_, err := h.run("sudo", "mkdir", "-p", path)
	return err
}

func (h *realHost) Run(name string, args ...string) (string, error) {
	return h.run(name, args...)
}

func (h *realHost) RunRoot(name string, args ...string) (string, error) {
	if h.isRoot {
		return h.run(name, args...)
	}
	return h.run("sudo", append([]string{name}, args...)...)
}

func (h *realHost) Glob(pattern string) ([]string, error) {
	return filepath.Glob(pattern)
}

func (h *realHost) Look(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (h *realHost) User() string {
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func (h *realHost) PrimeRoot() error {
	if h.isRoot {
		return nil
	}
	cmd := exec.Command("sudo", "-v")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo is required for on/off: %w", err)
	}
	return nil
}

func (h *realHost) run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return "", commandError(cmd.Args, out.String(), err)
	}
	return strings.TrimSpace(out.String()), nil
}

func commandError(args []string, output string, err error) error {
	msg := strings.TrimSpace(output)
	if msg == "" {
		return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, msg)
}
