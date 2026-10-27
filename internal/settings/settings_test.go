package settings

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Get().Theme; got != ThemeSystem {
		t.Fatalf("theme = %q, want system", got)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTheme(ThemeDark); err != nil {
		t.Fatal(err)
	}
	if got := st.Get().Theme; got != ThemeDark {
		t.Fatalf("theme = %q, want dark", got)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get().Theme; got != ThemeDark {
		t.Fatalf("reopened theme = %q, want dark", got)
	}
	if _, err := os.Stat(filepath.Join(dir, settingsFile+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary file left behind: %v", err)
	}
}

func TestBadValue(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	if err := st.SetTheme("purple"); err == nil {
		t.Fatal("want an error for an unknown theme")
	}
	if got := st.Get().Theme; got != ThemeSystem {
		t.Fatalf("theme = %q after a rejected change, want system", got)
	}
	if _, err := os.Stat(filepath.Join(dir, settingsFile)); !os.IsNotExist(err) {
		t.Fatal("a rejected change must not write the file")
	}

	// An unknown value in the file reads back as system.
	if err := os.WriteFile(filepath.Join(dir, settingsFile), []byte(`{"theme":"purple"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Get().Theme; got != ThemeSystem {
		t.Fatalf("theme = %q, want system", got)
	}
}

func TestCorruptFile(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settingsFile), []byte(`{"theme":`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Get().Theme; got != ThemeSystem {
		t.Fatalf("theme = %q, want system", got)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("want a warning, got %q", logs.String())
	}
	// Saving replaces the corrupt file.
	if err := st.SetTheme(ThemeLight); err != nil {
		t.Fatal(err)
	}
	if again, _ := Open(dir); again.Get().Theme != ThemeLight {
		t.Fatalf("theme = %q, want light", again.Get().Theme)
	}
}
