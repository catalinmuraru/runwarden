// Package settings keeps app-wide settings that admins change in the Admin area, such as the
// colour theme, in DATA_DIR/settings.json.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const settingsFile = "settings.json"

// Themes.
const (
	ThemeSystem = "system" // follow the operating system
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// ValidTheme reports whether t is a known theme.
func ValidTheme(t string) bool {
	return t == ThemeSystem || t == ThemeLight || t == ThemeDark
}

// Settings are the app-wide settings.
type Settings struct {
	Theme string `json:"theme"`
}

func (s *Settings) normalize() {
	if !ValidTheme(s.Theme) {
		s.Theme = ThemeSystem
	}
}

// Store keeps the settings in memory and in DATA_DIR/settings.json.
type Store struct {
	path string

	mu sync.RWMutex
	s  Settings
}

// Open loads the settings from dataDir. A missing file gives the defaults; an unreadable one
// gives the defaults and a warning, so a bad file never keeps the server from starting.
func Open(dataDir string) (*Store, error) {
	st := &Store{path: filepath.Join(dataDir, settingsFile)}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(st.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &st.s); err != nil {
			slog.Warn("settings: the file is not valid, using the defaults", "file", st.path, "err", err)
			st.s = Settings{}
		} else if st.s.Theme != "" && !ValidTheme(st.s.Theme) {
			slog.Warn("settings: unknown theme, using the system theme", "file", st.path, "theme", st.s.Theme)
		}
	}
	st.s.normalize()
	return st, nil
}

// Get returns the current settings.
func (st *Store) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.s
}

// SetTheme saves the theme: system, light or dark.
func (st *Store) SetTheme(theme string) error {
	if !ValidTheme(theme) {
		return fmt.Errorf("unknown theme %q: use system, light or dark", theme)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.s
	next.Theme = theme
	if err := st.write(next); err != nil {
		return err
	}
	st.s = next
	return nil
}

func (st *Store) write(s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}
