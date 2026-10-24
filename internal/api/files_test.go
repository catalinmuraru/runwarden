//go:build unix

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/executor"
)

// fileTree creates:
//
//	app.log  notes.txt  secret.log  link.log -> /etc/hostname
//	2026/ (10/report.log, tmp/x.log)   outside-link -> /etc
func fileTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"app.log", "notes.txt", "secret.log", "2026/10/report.log", "2026/tmp/x.log"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink("/etc/hostname", filepath.Join(dir, "link.log"))
	_ = os.Symlink("/etc", filepath.Join(dir, "outside-link"))
	return dir
}

func (e *testEnv) entries(param, path string) (int, []fileEntry) {
	e.t.Helper()
	code, body := e.do(http.MethodGet, "/api/scripts/Pick/parameters/"+param+"/files?path="+path, nil)
	var resp struct{ Entries []fileEntry }
	_ = json.Unmarshal(body, &resp)
	return code, resp.Entries
}

func TestParameterFiles(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	dir := fileTree(t)
	for _, p := range e.script("Pick").Parameters {
		p.FileDir = dir
	}

	names := func(es []fileEntry) []string {
		var out []string
		for _, x := range es {
			n := x.Name
			if x.Dir {
				n += "/"
			}
			out = append(out, n)
		}
		return out
	}

	if code, got := e.entries("Flat", ""); code != http.StatusOK || !slices.Equal(names(got), []string{"app.log"}) {
		t.Errorf("flat: %d %q (want only allowed, non-excluded files, no symlinks)", code, names(got))
	}
	if code, _ := e.entries("Flat", "2026"); code != http.StatusNotFound {
		t.Errorf("flat with a path = %d, want 404", code)
	}
	if code, got := e.entries("Tree", ""); code != http.StatusOK || !slices.Equal(names(got), []string{"2026/", "app.log", "notes.txt", "secret.log"}) {
		t.Errorf("tree root: %d %q", code, names(got))
	}
	if code, got := e.entries("Tree", "2026"); code != http.StatusOK || !slices.Equal(names(got), []string{"10/"}) {
		t.Errorf("tree 2026: %d %q (tmp is excluded)", code, names(got))
	}
	if code, got := e.entries("Tree", "2026/10"); code != http.StatusOK || !slices.Equal(names(got), []string{"report.log"}) {
		t.Errorf("tree 2026/10: %d %q", code, names(got))
	}
	if code, got := e.entries("Folder", ""); code != http.StatusOK || !slices.Equal(names(got), []string{"2026/"}) {
		t.Errorf("folders: %d %q", code, names(got))
	}

	for _, bad := range []string{"..", "2026/..", "outside-link", "2026/tmp", "nope"} {
		if code, _ := e.entries("Tree", bad); code != http.StatusNotFound && code != http.StatusBadRequest {
			t.Errorf("path %q = %d, want 400/404", bad, code)
		}
	}
	if code, _ := e.as("bob", "").entries("Tree", ""); code != http.StatusNotFound {
		t.Errorf("bob (not allowed) = %d, want 404", code)
	}
	if code, _ := e.entries("Nope", ""); code != http.StatusNotFound {
		t.Errorf("unknown parameter = %d", code)
	}
}
