// Package history keeps a record of every run on disk: metadata (who ran what, with which
// parameters, and how it ended), the full output, and files the script produced.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/catalinmuraru/runwarden/internal/executor"
)

// StatusInterrupted marks runs that were still running when the server stopped.
const StatusInterrupted = "interrupted"

// maxFilesBytes caps the output files copied for one run.
const maxFilesBytes = 100 << 20

// ErrNotFound is returned for unknown runs or files.
var ErrNotFound = errors.New("not found")

var validID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.\-]*$`)

// Meta describes one run. Secure parameter values are masked.
type Meta struct {
	ID              string         `json:"id"`
	Script          string         `json:"script"`
	User            string         `json:"user"`
	Args            []string       `json:"args"`
	Values          map[string]any `json:"values,omitempty"`
	Status          string         `json:"status"`
	ExitCode        *int           `json:"exit_code,omitempty"`
	Started         time.Time      `json:"started"`
	Finished        *time.Time     `json:"finished,omitempty"`
	OutputTruncated bool           `json:"output_truncated,omitempty"`
	Files           []string       `json:"files,omitempty"`
}

// OutputFiles says which files a run produces for download.
type OutputFiles struct {
	Patterns []string // globs; relative ones resolve against Dir
	Dir      string
}

// Store is the on-disk history under <data dir>/history.
type Store struct {
	dir string
	mu  sync.Mutex // serializes meta.json writes
	wg  sync.WaitGroup
}

// Open opens (creating if needed) the history in dataDir. Runs left in "running" state by a
// previous process are marked interrupted.
func Open(dataDir string) (*Store, error) {
	s := &Store{dir: filepath.Join(dataDir, "history")}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return nil, err
	}
	metas, err := s.all()
	if err != nil {
		return nil, err
	}
	for _, m := range metas {
		if m.Status == string(executor.StatusRunning) {
			m.Status = StatusInterrupted
			if err := s.writeMeta(m); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

// Wait blocks until all tracked runs have been written to disk.
func (s *Store) Wait() { s.wg.Wait() }

// Track records a run: its metadata now, its output while it runs, and its result and output
// files when it ends. onDone, if set, is called after everything is written.
func (s *Store) Track(run *executor.Run, meta Meta, files OutputFiles, onDone func()) error {
	if !validID.MatchString(run.ID) {
		return fmt.Errorf("invalid run id %q", run.ID)
	}
	dir := filepath.Join(s.dir, run.ID)
	if err := os.Mkdir(dir, 0o750); err != nil {
		return err
	}
	meta.ID = run.ID
	meta.Started = run.Started
	if meta.Args == nil {
		meta.Args = []string{} // clients get [] rather than null for runs without arguments
	}
	meta.Status = string(executor.StatusRunning)
	if err := s.writeMeta(meta); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // dir built from a validated run id
	if err != nil {
		return err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		offset := 0
		for {
			changed := run.Changed()
			data, next, done := run.Output(offset)
			if _, err := out.Write(data); err != nil {
				slog.Error("history: write output", "run", run.ID, "err", err)
			}
			offset = next
			if done {
				break
			}
			<-changed
		}
		_ = out.Close()

		res := run.Wait()
		finished := res.Finished
		code := res.ExitCode
		meta.Status = string(res.Status)
		meta.ExitCode = &code
		meta.Finished = &finished
		meta.OutputTruncated = res.OutputTruncated
		meta.Files = copyFiles(files, filepath.Join(dir, "files"))
		if err := s.writeMeta(meta); err != nil {
			slog.Error("history: write meta", "run", run.ID, "err", err)
		}
		if onDone != nil {
			onDone()
		}
	}()
	return nil
}

// Filter narrows List.
type Filter struct {
	Script  string
	Limit   int             // default 100
	Visible func(Meta) bool // if set, only runs it accepts are listed (applied before Limit)
}

// List returns runs newest first.
func (s *Store) List(f Filter) ([]Meta, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, m := range all {
		if (f.Script != "" && m.Script != f.Script) || (f.Visible != nil && !f.Visible(m)) {
			continue
		}
		out = append(out, m)
		if len(out) == f.Limit {
			break
		}
	}
	return out, nil
}

// Get returns one run's metadata.
func (s *Store) Get(id string) (Meta, error) {
	if !validID.MatchString(id) {
		return Meta{}, ErrNotFound
	}
	return s.readMeta(filepath.Join(s.dir, id, "meta.json"))
}

// OpenOutput opens a run's stored output.
func (s *Store) OpenOutput(id string) (*os.File, error) {
	if !validID.MatchString(id) {
		return nil, ErrNotFound
	}
	return openExisting(filepath.Join(s.dir, id, "output.log"))
}

// OpenFile opens an output file of a run by its listed name.
func (s *Store) OpenFile(id, name string) (*os.File, error) {
	if !validID.MatchString(id) || name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, ErrNotFound
	}
	return openExisting(filepath.Join(s.dir, id, "files", name))
}

func openExisting(path string) (*os.File, error) {
	f, err := os.Open(path) //nolint:gosec // path built from a validated id and name
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// all reads every run's metadata, newest first. Unreadable entries are skipped.
func (s *Store) all() ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() || !validID.MatchString(e.Name()) {
			continue
		}
		m, err := s.readMeta(filepath.Join(s.dir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b Meta) int {
		if c := b.Started.Compare(a.Started); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

func (s *Store) readMeta(path string) (Meta, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path built from a validated id
	if errors.Is(err, fs.ErrNotExist) {
		return Meta{}, ErrNotFound
	}
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// writeMeta replaces meta.json atomically.
func (s *Store) writeMeta(m Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, m.ID, "meta.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// copyFiles copies regular files matching the patterns into dest and returns their names.
// Symlinks and directories are skipped; the total size is capped.
func copyFiles(files OutputFiles, dest string) []string {
	var names []string
	var total int64
	for _, pattern := range files.Patterns {
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(files.Dir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			slog.Warn("history: bad output_files pattern", "pattern", pattern, "err", err)
			continue
		}
		for _, src := range matches {
			info, err := os.Lstat(src)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if total+info.Size() > maxFilesBytes {
				slog.Warn("history: output files exceed the size limit; skipping", "file", src)
				continue
			}
			name := uniqueName(names, filepath.Base(src))
			if err := copyFile(src, filepath.Join(dest, name)); err != nil {
				slog.Warn("history: copy output file", "file", src, "err", err)
				continue
			}
			total += info.Size()
			names = append(names, name)
		}
	}
	return names
}

func uniqueName(taken []string, name string) string {
	candidate := name
	for i := 2; slices.Contains(taken, candidate); i++ {
		candidate = fmt.Sprintf("%d_%s", i, name)
	}
	return candidate
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	in, err := os.Open(src) //nolint:gosec // src matched an operator-configured pattern
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // dst inside the run's history dir
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
