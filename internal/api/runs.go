package api

import (
	"context"
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

	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
	"github.com/catalinmuraru/runwarden/internal/params"
)

type startBody struct {
	Values map[string]any `json:"values"`
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	refused := func(reason string) {
		s.record(r, audit.Event{Action: audit.RunStart, Category: "process", Type: "start",
			Reason: reason, Script: name, Message: "run refused: " + reason})
	}
	sc := s.allowedScript(r, name)
	if sc == nil {
		if s.script(name) != nil {
			refused("not allowed to run this script")
		} else {
			refused("unknown script")
		}
		writeError(w, http.StatusNotFound, "unknown script")
		return
	}

	uploadDir, err := s.newUploadDir()
	if err != nil {
		slog.Error("create upload dir", "err", err)
		refused("internal error")
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
		refused("invalid request")
		writeError(w, status, err.Error())
		return
	}
	values, err := sc.Values(raw)
	if err != nil {
		details := errorList(err)
		refused("invalid parameters: " + strings.Join(details, "; "))
		writeError(w, http.StatusBadRequest, "invalid parameters", details...)
		return
	}
	user := currentUser(r)
	// Access depends on the environments the values select; helper scripts for dynamic values
	// only run once that is settled.
	uses := s.opts.Access.Uses(sc, func(name string) []string { return valueItems(values[name]) })
	envs := authz.Environments(uses)
	if err := s.opts.Access.CheckStart(user, sc, uses); err != nil {
		refused(err.Error())
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	// Only the secrets the script declares, from the environments it touches (checked above).
	sec, err := s.resolveSecrets(sc, uses)
	switch {
	case secretRefusal(err):
		refused(err.Error())
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		slog.Error("resolve secrets", "script", sc.Name, "err", err)
		refused("could not read secrets")
		writeError(w, http.StatusInternalServerError, "could not start the run")
		return
	}
	// The cluster of the environment, for scripts that use one; refused like a missing secret.
	kc, clusters, err := s.resolveKube(sc, uses)
	switch {
	case secretRefusal(err):
		refused(err.Error())
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		slog.Error("resolve kubeconfig", "script", sc.Name, "err", err)
		refused("could not read the kubeconfig")
		writeError(w, http.StatusInternalServerError, "could not start the run")
		return
	}
	if problems := s.checkDynamicValues(r, sc, raw, values); len(problems) > 0 {
		refused("invalid parameters: " + strings.Join(problems, "; "))
		writeError(w, http.StatusBadRequest, "invalid parameters", problems...)
		return
	}

	run, err := s.exec.Start(executor.Request{Script: sc, Values: values, User: user.Name, Timeout: sc.Timeout, Secrets: sec.Env, Kube: kc})
	switch {
	case errors.Is(err, executor.ErrUserBusy):
		refused("user already has a run in progress")
		writeError(w, http.StatusTooManyRequests, "you already have a run in progress; wait for it to finish or stop it", s.exec.RunningFor(user.Name)...)
		return
	case errors.Is(err, executor.ErrBusy):
		refused("too many runs in progress")
		writeError(w, http.StatusTooManyRequests, "too many runs in progress, try again later")
		return
	case err != nil:
		slog.Error("start run", "script", sc.Name, "err", err)
		refused("start failed")
		writeError(w, http.StatusInternalServerError, "could not start the run")
		return
	}
	started = true

	masked := params.MaskValues(sc.Parameters, values)
	// The end event is written when the run is over, possibly long after this request: capture
	// who started it now.
	end := audit.Event{Action: audit.RunEnd, Category: "process", Type: "end", RunID: run.ID, Script: sc.Name,
		User: user.Name, Roles: s.opts.Access.Roles(user), Groups: user.Groups, AuthMethod: user.Method,
		SourceIP: s.opts.ClientIP.IP(r), UserAgent: r.UserAgent(), Values: masked, Args: run.MaskedArgs, Environments: envs,
		Secrets: sec.Names()}
	meta := history.Meta{Script: sc.Name, User: user.Name, Args: run.MaskedArgs, Values: masked, Environments: envs,
		Secrets: sec.Names(), Clusters: clusters, Terminal: run.Terminal}
	files := history.OutputFiles{Patterns: sc.OutputFiles, Dir: sc.WorkingDirectory}
	err = s.hist.Track(run, meta, files, func() {
		res := run.Result()
		code := res.ExitCode
		end.Status, end.ExitCode, end.Duration = string(res.Status), &code, res.Finished.Sub(res.Started)
		end.Success = res.Status == executor.StatusFinished
		if !end.Success {
			end.Reason = string(res.Status)
		}
		end.Message = fmt.Sprintf("run %s of %q by %s: %s (exit code %d)", run.ID, sc.Name, user.Name, res.Status, code)
		audit.Log(context.Background(), end)
		s.exec.Forget(run.ID)
		_ = os.RemoveAll(uploadDir)
	})
	if err != nil {
		// A run that cannot be recorded must not continue unaudited.
		slog.Error("record run; killing it", "run", run.ID, "err", err)
		run.Kill()
		refused("could not record the run")
		writeError(w, http.StatusInternalServerError, "could not record the run")
		return
	}
	s.record(r, audit.Event{Action: audit.RunStart, Category: "process", Type: "start", Success: true,
		RunID: run.ID, Script: sc.Name, Values: masked, Args: run.MaskedArgs, Environments: envs, Secrets: sec.Names(),
		Message: fmt.Sprintf("run %s of %q started by %s", run.ID, sc.Name, user.Name)})

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

// listRuns lists the runs the user may see, newest first, filtered by script, user and status.
// Running runs come from the executor, so polling them does not read the whole history.
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := history.Filter{
		Script:  q.Get("script"),
		User:    q.Get("user"),
		Status:  q.Get("status"),
		Limit:   limit,
		Visible: func(m history.Meta) bool { return s.canSee(r, m) },
	}
	var runs []history.Meta
	var err error
	if f.Status == string(executor.StatusRunning) {
		runs = s.runningRuns(r, f)
	} else {
		runs, err = s.hist.List(f)
	}
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

// runMeta returns a run's record if the requesting user may see it, with the live status while
// the history is still catching up. Unknown and hidden runs look the same.
func (s *Server) runMeta(r *http.Request, id string) (history.Meta, bool) {
	meta, err := s.hist.Get(id)
	if err != nil || !s.canSee(r, meta) {
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
	meta, ok := s.runMeta(r, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		history.Meta
		CanControl bool `json:"can_control"` // may send input, stop and kill (owner, devops)
	}{meta, s.canControl(r, meta.User)})
}

// valueItems returns a parameter value as a list of strings (none for flags and empty values).
func valueItems(v params.Value) []string {
	switch v.Kind {
	case params.KindScalar:
		if v.Str != "" {
			return []string{v.Str}
		}
	case params.KindList:
		return v.List
	}
	return nil
}

func (s *Server) runOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)
	if _, ok := s.runMeta(r, id); !ok {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
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
	// Secret marks hidden input: it is masked wherever it would appear in the output.
	Secret bool `json:"secret"`
}

func (s *Server) runInput(w http.ResponseWriter, r *http.Request) {
	run, ok := s.liveRun(w, r)
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
		err = run.WriteInput(*body.Line, body.Secret)
	case body.Close:
		err = run.CloseInput()
	default:
		writeError(w, http.StatusBadRequest, `send {"line": "..."} or {"close": true}`)
		return
	}
	s.recordRunAction(r, audit.RunInput, run, err)
	if err != nil {
		writeError(w, http.StatusConflict, "the run does not accept input")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runSignal(w http.ResponseWriter, r *http.Request) {
	run, ok := s.liveRun(w, r)
	if !ok {
		return
	}
	if strings.HasSuffix(r.URL.Path, "/kill") {
		run.Kill()
		s.recordRunAction(r, audit.RunKill, run, nil)
	} else {
		run.Stop()
		s.recordRunAction(r, audit.RunStop, run, nil)
	}
	w.WriteHeader(http.StatusAccepted)
}

// liveRun finds a running run the user may control, answering 404 for unknown or hidden runs
// and 409 for finished ones.
func (s *Server) liveRun(w http.ResponseWriter, r *http.Request) (*executor.Run, bool) {
	id := r.PathValue("id")
	meta, ok := s.runMeta(r, id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown run")
		return nil, false
	}
	if !s.canControl(r, meta.User) {
		writeError(w, http.StatusForbidden, "you may only view this run")
		return nil, false
	}
	if run := s.exec.Get(id); run != nil && run.Result().Status == executor.StatusRunning {
		return run, true
	}
	writeError(w, http.StatusConflict, "the run has finished")
	return nil, false
}

func (s *Server) runFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if _, ok := s.runMeta(r, r.PathValue("id")); !ok {
		writeError(w, http.StatusNotFound, "unknown file")
		return
	}
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

// runningRuns returns the visible runs in progress that match f, newest first.
func (s *Server) runningRuns(r *http.Request, f history.Filter) []history.Meta {
	var out []history.Meta
	for _, id := range s.exec.Running() {
		m, ok := s.runMeta(r, id)
		if !ok || m.Status != string(executor.StatusRunning) ||
			(f.Script != "" && m.Script != f.Script) || (f.User != "" && !strings.EqualFold(f.User, m.User)) {
			continue
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b history.Meta) int { return b.Started.Compare(a.Started) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}
