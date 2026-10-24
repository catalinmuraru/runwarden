package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
	"github.com/catalinmuraru/runwarden/internal/params"
)

type startBody struct {
	Values map[string]any `json:"values"`
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	sc := s.script(r.PathValue("name"))
	if sc == nil {
		writeError(w, http.StatusNotFound, "unknown script")
		return
	}

	uploadDir, err := s.newUploadDir()
	if err != nil {
		slog.Error("create upload dir", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start the run")
		return
	}
	started := false
	defer func() {
		if !started {
			_ = os.RemoveAll(uploadDir)
		}
	}()

	raw, status, err := s.readRunInput(w, r, sc, uploadDir)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	values, err := sc.Values(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid parameters", errorList(err)...)
		return
	}
	if problems := s.checkDynamicValues(r, sc, raw, values); len(problems) > 0 {
		writeError(w, http.StatusBadRequest, "invalid parameters", problems...)
		return
	}

	run, err := s.exec.Start(executor.Request{Script: sc, Values: values})
	switch {
	case errors.Is(err, executor.ErrBusy):
		writeError(w, http.StatusTooManyRequests, "too many runs in progress, try again later")
		return
	case err != nil:
		slog.Error("start run", "script", sc.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "could not start the run")
		return
	}
	started = true

	user := userFrom(r.Context())
	masked := params.MaskValues(sc.Parameters, values)
	meta := history.Meta{Script: sc.Name, User: user.Name, Args: run.MaskedArgs, Values: masked}
	files := history.OutputFiles{Patterns: sc.OutputFiles, Dir: sc.WorkingDirectory}
	err = s.hist.Track(run, meta, files, func() {
		s.exec.Forget(run.ID)
		_ = os.RemoveAll(uploadDir)
	})
	if err != nil {
		// A run that cannot be recorded must not continue unaudited.
		slog.Error("record run; killing it", "run", run.ID, "err", err)
		run.Kill()
		writeError(w, http.StatusInternalServerError, "could not record the run")
		return
	}
	audit(r, "run.start", "script", sc.Name, "run", run.ID, "values", masked)

	w.Header().Set("Location", "/api/runs/"+run.ID)
	writeJSON(w, http.StatusCreated, map[string]string{"id": run.ID, "status": string(executor.StatusRunning)})
}

// readRunInput reads parameter values from a JSON body or a multipart form. Files for
// file_upload parameters are only accepted as uploads, never as paths in the values.
func (s *Server) readRunInput(w http.ResponseWriter, r *http.Request, sc *config.Script, uploadDir string) (map[string]any, int, error) {
	raw := map[string]any{}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))

	if mediaType != "multipart/form-data" {
		var body startBody
		if err := decodeJSON(w, r, &body); err != nil {
			return nil, http.StatusBadRequest, err
		}
		if body.Values != nil {
			raw = body.Values
		}
		if err := rejectFileValues(sc, raw); err != nil {
			return nil, http.StatusBadRequest, err
		}
		return raw, 0, nil
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid multipart body: %w", err)
	}
	uploads := map[string]any{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, uploadStatus(err), fmt.Errorf("invalid multipart body: %w", err)
		}
		name := part.FormName()
		if part.FileName() == "" {
			if name != "values" {
				continue
			}
			dec := json.NewDecoder(io.LimitReader(part, maxJSONBody))
			dec.UseNumber()
			if err := dec.Decode(&raw); err != nil {
				return nil, http.StatusBadRequest, fmt.Errorf("invalid values field: %w", err)
			}
			continue
		}
		p := sc.Parameter(name)
		if p == nil || p.Type != params.TypeFileUpload {
			return nil, http.StatusBadRequest, fmt.Errorf("unexpected file for %q", name)
		}
		if _, dup := uploads[name]; dup {
			return nil, http.StatusBadRequest, fmt.Errorf("more than one file for %q", name)
		}
		path, err := saveUpload(part, uploadDir, len(uploads))
		if err != nil {
			return nil, uploadStatus(err), err
		}
		uploads[name] = path
	}
	if err := rejectFileValues(sc, raw); err != nil {
		return nil, http.StatusBadRequest, err
	}
	for k, v := range uploads {
		raw[k] = v
	}
	return raw, 0, nil
}

func rejectFileValues(sc *config.Script, raw map[string]any) error {
	for _, p := range sc.Parameters {
		if _, ok := raw[p.Name]; ok && p.Type == params.TypeFileUpload {
			return fmt.Errorf("parameter %q must be uploaded as a file", p.Name)
		}
	}
	return nil
}

func uploadStatus(err error) int {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// saveUpload stores one uploaded file under dir/<index>/<sanitized name>.
func saveUpload(part *multipart.Part, dir string, index int) (string, error) {
	base := unsafeChars.ReplaceAllString(filepath.Base(part.FileName()), "_")
	base = strings.TrimLeft(base, ".")
	if base == "" {
		base = "upload"
	}
	sub := filepath.Join(dir, strconv.Itoa(index))
	if err := os.MkdirAll(sub, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(sub, base)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // sanitized name inside our upload dir
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, part); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("upload failed: %w", err)
	}
	return path, f.Close()
}

func (s *Server) newUploadDir() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	dir := filepath.Join(s.opts.DataDir, "uploads", hex.EncodeToString(b))
	return dir, os.MkdirAll(dir, 0o750)
}

// checkDynamicValues verifies that values of parameters with script-provided lists are in the
// list computed for the submitted inputs.
func (s *Server) checkDynamicValues(r *http.Request, sc *config.Script, raw map[string]any, values map[string]params.Value) []string {
	var problems []string
	for _, p := range sc.Parameters {
		v := values[p.Name]
		if p.ValuesScript == "" || p.Constant || v.IsEmpty() {
			continue
		}
		allowed, err := s.dynamicValues(r.Context(), sc, p, func(name string) (any, bool) {
			rv, ok := raw[name]
			return rv, ok
		})
		if err != nil {
			slog.Warn("values script failed", "script", sc.Name, "parameter", p.Name, "err", err)
			problems = append(problems, fmt.Sprintf("parameter %q: could not load its allowed values", p.Name))
			continue
		}
		items := v.List
		if v.Kind == params.KindScalar {
			items = []string{v.Str}
		}
		for _, item := range items {
			if !slices.Contains(allowed, item) {
				problems = append(problems, (&params.ValidationError{Param: p.Name, Reason: "is not one of the allowed values"}).Error())
				break
			}
		}
	}
	return problems
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.hist.List(history.Filter{Script: r.URL.Query().Get("script"), Limit: limit})
	if err != nil {
		slog.Error("list runs", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if runs == nil {
		runs = []history.Meta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// runMeta returns a run's record, with the live status while the history is still catching up.
func (s *Server) runMeta(id string) (history.Meta, bool) {
	meta, err := s.hist.Get(id)
	if err != nil {
		return history.Meta{}, false
	}
	if run := s.exec.Get(id); run != nil {
		res := run.Result()
		meta.Status = string(res.Status)
		if res.Status != executor.StatusRunning {
			code, finished := res.ExitCode, res.Finished
			meta.ExitCode, meta.Finished = &code, &finished
		}
	}
	return meta, true
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	meta, ok := s.runMeta(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) runOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")

	if run := s.exec.Get(id); run != nil {
		data, next, done := run.Output(offset)
		w.Header().Set("X-Next-Offset", strconv.Itoa(next))
		w.Header().Set("X-Run-Finished", strconv.FormatBool(done))
		_, _ = w.Write(data) //nolint:gosec // served as text/plain with nosniff, never rendered as HTML
		return
	}
	f, err := s.hist.OpenOutput(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read output")
		return
	}
	offset = int(min(int64(offset), info.Size()))
	w.Header().Set("X-Next-Offset", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("X-Run-Finished", "true")
	_, _ = f.Seek(int64(offset), io.SeekStart)
	_, _ = io.Copy(w, f)
}

type inputBody struct {
	Line  *string `json:"line"`
	Close bool    `json:"close"`
}

func (s *Server) runInput(w http.ResponseWriter, r *http.Request) {
	run, ok := s.liveRun(w, r.PathValue("id"))
	if !ok {
		return
	}
	var body inputBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	switch {
	case body.Line != nil:
		err = run.WriteInput(*body.Line)
	case body.Close:
		err = run.CloseInput()
	default:
		writeError(w, http.StatusBadRequest, `send {"line": "..."} or {"close": true}`)
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "the run does not accept input")
		return
	}
	audit(r, "run.input", "run", run.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runSignal(w http.ResponseWriter, r *http.Request) {
	run, ok := s.liveRun(w, r.PathValue("id"))
	if !ok {
		return
	}
	action := "stop"
	if strings.HasSuffix(r.URL.Path, "/kill") {
		action = "kill"
		run.Kill()
	} else {
		run.Stop()
	}
	audit(r, "run."+action, "run", run.ID, "script", run.ScriptName)
	w.WriteHeader(http.StatusAccepted)
}

// liveRun finds a running run, answering 404 for unknown runs and 409 for finished ones.
func (s *Server) liveRun(w http.ResponseWriter, id string) (*executor.Run, bool) {
	run := s.exec.Get(id)
	if run != nil && run.Result().Status == executor.StatusRunning {
		return run, true
	}
	if _, err := s.hist.Get(id); err == nil || run != nil {
		writeError(w, http.StatusConflict, "the run has finished")
		return nil, false
	}
	writeError(w, http.StatusNotFound, "unknown run")
	return nil, false
}

func (s *Server) runFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	f, err := s.hist.OpenFile(r.PathValue("id"), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown file")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read file")
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
