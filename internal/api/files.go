package api

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/params"
)

type fileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

// parameterFiles lists what a server_file parameter can select in one directory:
// ?path=a/b lists file_dir/a/b (recursive parameters only). Directories are listed when they
// can be selected or entered; files when they can be selected. Symlinks are never listed.
func (s *Server) parameterFiles(w http.ResponseWriter, r *http.Request) {
	sc := s.allowedScript(r, r.PathValue("name"))
	if sc == nil {
		writeError(w, http.StatusNotFound, "unknown script")
		return
	}
	p := sc.Parameter(r.PathValue("param"))
	if p == nil || p.Type != params.TypeServerFile {
		writeError(w, http.StatusNotFound, "unknown parameter")
		return
	}

	var segments []string
	for _, seg := range strings.Split(r.URL.Query().Get("path"), "/") {
		if seg == "" {
			continue
		}
		if !params.ValidSegment(seg) {
			writeError(w, http.StatusBadRequest, "invalid path")
			return
		}
		segments = append(segments, seg)
	}
	if (len(segments) > 0 && !p.FileRecursive) || p.Excluded(segments) {
		writeError(w, http.StatusNotFound, "unknown directory")
		return
	}

	dir := filepath.Join(append([]string{p.FileDir}, segments...)...)
	if !inside(p.FileDir, dir) {
		writeError(w, http.StatusNotFound, "unknown directory")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "unknown directory")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not list the directory")
		return
	}

	out := []fileEntry{}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		candidate := append(slices.Clone(segments), e.Name())
		if e.IsDir() {
			// Directories are shown to navigate into (recursive) or to pick (file_type dir).
			if (p.FileRecursive || p.FileType == params.FileTypeDir) && !p.Excluded(candidate) {
				out = append(out, fileEntry{Name: e.Name(), Dir: true})
			}
			continue
		}
		if !e.Type().IsRegular() || p.FileType == params.FileTypeDir {
			continue
		}
		v := params.Scalar(e.Name())
		if p.FileRecursive {
			v = params.List(candidate...)
		}
		if p.Validate(v) == nil {
			out = append(out, fileEntry{Name: e.Name()})
		}
	}
	slices.SortFunc(out, func(a, b fileEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

// inside reports whether dir resolves (following symlinks) to base or a directory below it.
func inside(base, dir string) bool {
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return false
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realBase, realDir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
