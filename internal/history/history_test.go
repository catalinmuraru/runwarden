//go:build unix

package history

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
)

func runProduce(t *testing.T, s *Store, ex *executor.Executor, work string) string {
	t.Helper()
	script, _ := filepath.Abs("testdata/produce.sh")
	run, err := ex.Start(executor.Request{Script: &config.Script{Name: "Produce", Command: []string{script}, WorkingDirectory: work}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	meta := Meta{Script: "Produce", User: "alice", Args: []string{"******"}, Values: map[string]any{"Token": "******"}}
	files := OutputFiles{Patterns: []string{"*.txt", "/nonexistent/*.log"}, Dir: work}
	if err := s.Track(run, meta, files, func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("tracking did not finish")
	}
	return run.ID
}

func TestTrackAndReopen(t *testing.T) {
	data, work := t.TempDir(), t.TempDir()
	s, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	id := runProduce(t, s, executor.New(executor.Limits{}, executor.EnvPolicy{}), work)

	s, err = Open(data) // a fresh process sees the same history
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "failed" || m.ExitCode == nil || *m.ExitCode != 2 || m.User != "alice" || m.Finished == nil {
		t.Errorf("meta = %+v", m)
	}
	if m.Values["Token"] != "******" {
		t.Errorf("values = %v", m.Values)
	}
	if !slices.Equal(m.Files, []string{"out.txt"}) {
		t.Errorf("files = %q (symlinks must be skipped)", m.Files)
	}

	f, err := s.OpenOutput(id)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(f)
	_ = f.Close()
	if string(out) != "hello\n" {
		t.Errorf("output = %q", out)
	}
	f, err = s.OpenFile(id, "out.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(f)
	_ = f.Close()
	if string(body) != "file body\n" {
		t.Errorf("file = %q", body)
	}

	list, _ := s.List(Filter{})
	if len(list) != 1 || list[0].ID != id {
		t.Errorf("list = %+v", list)
	}
	if list, _ := s.List(Filter{Script: "Other"}); len(list) != 0 {
		t.Errorf("filtered list = %+v", list)
	}
}

func TestArgsNeverNull(t *testing.T) {
	data, work := t.TempDir(), t.TempDir()
	s, _ := Open(data)
	script, _ := filepath.Abs("testdata/produce.sh")
	run, err := executor.New(executor.Limits{}, executor.EnvPolicy{}).Start(executor.Request{
		Script: &config.Script{Name: "NoArgs", Command: []string{script}, WorkingDirectory: work},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	if err := s.Track(run, Meta{Script: "NoArgs"}, OutputFiles{}, func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	<-done
	raw, _ := os.ReadFile(filepath.Join(data, "history", run.ID, "meta.json")) //nolint:gosec // test temp dir
	if !strings.Contains(string(raw), `"args": []`) {
		t.Errorf("meta.json = %s", raw)
	}
}

func TestListOrderAndLimit(t *testing.T) {
	data := t.TempDir()
	s, _ := Open(data)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"a1", "a2", "a3"} {
		if err := os.Mkdir(filepath.Join(s.dir, id), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := s.writeMeta(Meta{ID: id, Script: "S", Status: "finished", Started: base.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.List(Filter{Limit: 2})
	if len(list) != 2 || list[0].ID != "a3" || list[1].ID != "a2" {
		t.Errorf("list = %+v", list)
	}
}

func TestInterruptedOnOpen(t *testing.T) {
	data := t.TempDir()
	s, _ := Open(data)
	if err := os.Mkdir(filepath.Join(s.dir, "r1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := s.writeMeta(Meta{ID: "r1", Status: "running", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s, _ = Open(data)
	if m, _ := s.Get("r1"); m.Status != StatusInterrupted {
		t.Errorf("status = %s, want %s", m.Status, StatusInterrupted)
	}
}

func TestInvalidPaths(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, id := range []string{"../x", "", ".hidden", "a/b"} {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v", id, err)
		}
		if _, err := s.OpenOutput(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenOutput(%q) = %v", id, err)
		}
	}
	for _, name := range []string{"../meta.json", "..", ".x", "", "a/b"} {
		if _, err := s.OpenFile("r1", name); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenFile(%q) = %v", name, err)
		}
	}
}

func TestUniqueName(t *testing.T) {
	if got := uniqueName([]string{"a.txt", "2_a.txt"}, "a.txt"); got != "3_a.txt" {
		t.Errorf("got %q", got)
	}
}
