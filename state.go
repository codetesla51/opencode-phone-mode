package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const stateVersion = 1

var errNoState = errors.New("no state file")

// SysValue is one sysfs setting that can be read back and restored verbatim.
type SysValue struct {
	Path  string `json:"path"`
	Value string `json:"value"`
}

type LidState struct {
	Path      string            `json:"path"`
	Existed   bool              `json:"existed"`
	Content   string            `json:"content,omitempty"`
	Effective map[string]string `json:"effective,omitempty"`
}

type RadioState struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	SoftBlocked bool   `json:"soft_blocked"`
}

type ServiceState struct {
	Name    string `json:"name"`
	Scope   string `json:"scope"` // "user" or "system"
	Active  bool   `json:"active"`
	Enabled string `json:"enabled,omitempty"` // systemctl UnitFileState
}

// State is written before phone mode changes anything. off restores exactly
// these values and then deletes the file.
type State struct {
	Version    int            `json:"version"`
	SavedAt    time.Time      `json:"saved_at"`
	Brightness *SysValue      `json:"brightness,omitempty"`
	Lid        *LidState      `json:"lid,omitempty"`
	Governor   []SysValue     `json:"governor,omitempty"`
	MaxFreq    []SysValue     `json:"max_freq,omitempty"`
	Turbo      *SysValue      `json:"turbo,omitempty"`
	Radios     []RadioState   `json:"radios,omitempty"`
	Bluetooth  *ServiceState  `json:"bluetooth,omitempty"`
	Services   []ServiceState `json:"services,omitempty"`
	Linger     *bool          `json:"linger,omitempty"`
	DGPU       []SysValue     `json:"dgpu,omitempty"`
	BBSwitch   *SysValue      `json:"bbswitch,omitempty"`
	Nvidia     *ServiceState  `json:"nvidia_persistenced,omitempty"`
}

type stateStore struct {
	dir string
}

func (s stateStore) statePath() string  { return filepath.Join(s.dir, "state.json") }
func (s stateStore) markerPath() string { return filepath.Join(s.dir, "active") }

func (s stateStore) Save(st State) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(s.dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmpName, s.statePath()); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func (s stateStore) Load() (State, error) {
	data, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, errNoState
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("parse state: %w", err)
	}
	if st.Version != stateVersion {
		return State{}, fmt.Errorf("state version %d is not supported", st.Version)
	}
	return st, nil
}

func (s stateStore) Remove() error {
	err := os.Remove(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s stateStore) Exists() bool {
	_, err := os.Stat(s.statePath())
	return err == nil
}

func (s stateStore) MarkActive() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.markerPath(), nil, 0o644)
}

func (s stateStore) ClearActive() error {
	err := os.Remove(s.markerPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s stateStore) Active() bool {
	_, err := os.Stat(s.markerPath())
	return err == nil
}
