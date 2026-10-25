//go:build unix

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
)

const testWait = 20 * time.Second

type testEnv struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	ex     *executor.Executor
	hist   *history.Store
	data   string
	user   string // sent as X-Test-User; empty means signed out
	groups string // comma-separated, sent as X-Test-Groups
}

// fakeAuth signs requests in from test headers, standing in for the session middleware.
func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.Header.Get("X-Test-User")
		if name == "" {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		u := auth.User{Name: name}
		if g := r.Header.Get("X-Test-Groups"); g != "" {
			u.Groups = strings.Split(g, ",")
		}
		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
	})
}

// as returns a copy of e that sends requests as another user.
func (e *testEnv) as(user, groups string) *testEnv {
	c := *e
	c.user, c.groups = user, groups
	return &c
}

func (e *testEnv) identify(h http.Header) {
	if e.user != "" {
		h.Set("X-Test-User", e.user)
	}
	if e.groups != "" {
		h.Set("X-Test-Groups", e.groups)
	}
}

func setup(t *testing.T, limits executor.Limits, opts Options) *testEnv {
	t.Helper()
	base, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := config.LoadDir("testdata/runners", config.Options{BaseDir: base})
	if err != nil || len(cat.Errors) > 0 {
		t.Fatalf("load runners: %v %v", err, cat.Errors)
	}
	cat.Script("Report").WorkingDirectory = t.TempDir()

	data := t.TempDir()
	hist, err := history.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	ex := executor.New(limits, executor.EnvPolicy{})
	opts.BaseDir, opts.DataDir = base, data
	if opts.MaxUploadBytes == 0 {
		opts.MaxUploadBytes = 1 << 20
	}
	opts.Authenticate = fakeAuth
	opts.Policy = authz.Policy{AdminUsers: []string{"root"}}
	srv := New(opts, ex, hist)
	srv.SetCatalog(cat)
	mux := http.NewServeMux()
	srv.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		_ = ex.Shutdown(ctx)
		hist.Wait()
	})
	return &testEnv{t: t, srv: srv, ts: ts, ex: ex, hist: hist, data: data, user: "alice", groups: "ops"}
}

func (e *testEnv) script(name string) *config.Script {
	e.t.Helper()
	sc := e.srv.script(name)
	if sc == nil {
		e.t.Fatalf("script %q not loaded", name)
	}
	return sc
}

// do sends a request; a non-nil body that is not an io.Reader is sent as JSON.
func (e *testEnv) do(method, path string, body any, header ...string) (int, []byte) {
	e.t.Helper()
	var r io.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case io.Reader:
		r = b
	default:
		data, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	if len(header) == 2 {
		contentType = header[1]
	}
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.ts.URL+path, r)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	e.identify(req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (e *testEnv) start(script string, values map[string]any) string {
	e.t.Helper()
	code, body := e.do(http.MethodPost, "/api/scripts/"+script+"/runs", map[string]any{"values": values})
	if code != http.StatusCreated {
		e.t.Fatalf("start %s: %d %s", script, code, body)
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(body, &resp)
	return resp.ID
}

// finish waits until the run has ended and its history record is complete.
func (e *testEnv) finish(id string) history.Meta {
	e.t.Helper()
	if run := e.ex.Get(id); run != nil {
		select {
		case <-run.Done():
		case <-time.After(testWait):
			e.t.Fatalf("run %s did not finish", id)
		}
	}
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if m, err := e.hist.Get(id); err == nil && m.Finished != nil && e.ex.Get(id) == nil {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("history for %s not written", id)
	return history.Meta{}
}

func (e *testEnv) rawOutput(id string) string {
	e.t.Helper()
	_, body := e.do(http.MethodGet, "/api/runs/"+id+"/output", nil)
	return string(body)
}

func (e *testEnv) output(id string) string {
	e.t.Helper()
	_, body := e.do(http.MethodGet, "/api/runs/"+id+"/output", nil)
	return strings.ReplaceAll(string(body), "\r\n", "\n") // terminals end lines with CRLF
}

func (e *testEnv) waitOutput(id, text string) {
	e.t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if strings.Contains(e.output(id), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("output of %s never contained %q: %s", id, text, e.output(id))
}

func TestScripts(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})

	code, body := e.do(http.MethodGet, "/api/scripts", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"name":"Echo"`) {
		t.Fatalf("list: %d %s", code, body)
	}

	code, body = e.do(http.MethodGet, "/api/scripts/Echo", nil)
	if code != http.StatusOK {
		t.Fatalf("detail: %d %s", code, body)
	}
	if strings.Contains(string(body), "default-secret") {
		t.Errorf("secure default leaked: %s", body)
	}
	var view scriptView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	byName := map[string]paramView{}
	for _, p := range view.Parameters {
		byName[p.Name] = p
	}
	if p := byName["Token"]; !p.Secure || !p.HasDefault || p.Default != nil {
		t.Errorf("token = %+v", p)
	}
	if p := byName["Env"]; p.Default != "dev" || !slices.Equal(p.Values, []option{{"dev", "dev"}, {"prod", "Production"}}) {
		t.Errorf("env = %+v", p)
	}
	if p := byName["Target"]; !p.DynamicValues || !slices.Equal(p.DependsOn, []string{"Env"}) {
		t.Errorf("target = %+v", p)
	}
	if p := byName["Name"]; !p.Required || p.RegexDescription != "lowercase letters" {
		t.Errorf("name = %+v", p)
	}

	if code, _ := e.do(http.MethodGet, "/api/scripts/Nope", nil); code != http.StatusNotFound {
		t.Errorf("unknown script = %d", code)
	}
}

func TestRunWithJSON(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	id := e.start("Echo", map[string]any{"Name": "abc", "Env": "Production", "Target": "t-prod-2"})
	meta := e.finish(id)

	if meta.Status != "finished" || meta.ExitCode == nil || *meta.ExitCode != 0 || meta.User != "alice" {
		t.Errorf("meta = %+v", meta)
	}
	if out := e.output(id); !strings.Contains(out, "user=alice\n") {
		t.Errorf("RUNWARDEN_USER not passed to the script:\n%s", out)
	}
	if meta.Values["Token"] != "******" || meta.Values["Env"] != "prod" {
		t.Errorf("values = %v", meta.Values)
	}
	out := e.output(id)
	for _, want := range []string{"[--name]\n[abc]\n", "[--token]\n[******]\n", "[prod]\n", "[--target]\n[t-prod-2]\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "default-secret") || slices.Contains(meta.Args, "default-secret") {
		t.Error("secure default leaked")
	}

	code, body := e.do(http.MethodGet, "/api/runs/"+id, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"finished"`) {
		t.Errorf("get run: %d %s", code, body)
	}
	_, body = e.do(http.MethodGet, "/api/runs?script=Echo", nil)
	if !strings.Contains(string(body), id) {
		t.Errorf("history list misses the run: %s", body)
	}
	_, body = e.do(http.MethodGet, "/api/runs?script=Other", nil)
	if strings.Contains(string(body), id) {
		t.Errorf("filter ignored: %s", body)
	}
	if code, _ := e.do(http.MethodGet, "/api/runs/does-not-exist", nil); code != http.StatusNotFound {
		t.Errorf("unknown run = %d", code)
	}
}

func TestValidation(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	post := func(body any) (int, string) {
		code, b := e.do(http.MethodPost, "/api/scripts/Echo/runs", body)
		return code, string(b)
	}

	code, body := post(map[string]any{"values": map[string]any{"Name": "SECRETISH", "Unknown": 1}})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid values: %d %s", code, body)
	}
	for _, want := range []string{`\"Name\": must match lowercase letters`, `unknown parameter \"Unknown\"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "SECRETISH") {
		t.Errorf("response echoes a submitted value: %s", body)
	}

	if code, body := post(map[string]any{"values": map[string]any{}}); code != http.StatusBadRequest || !strings.Contains(body, "is required") {
		t.Errorf("missing required: %d %s", code, body)
	}
	if code, body := post(map[string]any{"values": map[string]any{"Name": "abc", "Upload": "/etc/passwd"}}); code != http.StatusBadRequest || !strings.Contains(body, "must be uploaded") {
		t.Errorf("file path as value: %d %s", code, body)
	}
	if code, body := post(map[string]any{"values": map[string]any{"Name": "abc", "Env": "dev", "Target": "t-prod-1"}}); code != http.StatusBadRequest || !strings.Contains(body, `\"Target\": is not one of the allowed values`) {
		t.Errorf("dynamic value outside its list: %d %s", code, body)
	}
	if code, _ := post(strings.NewReader("{")); code != http.StatusBadRequest {
		t.Errorf("broken JSON = %d", code)
	}
	if code, _ := e.do(http.MethodPost, "/api/scripts/Nope/runs", map[string]any{}); code != http.StatusNotFound {
		t.Errorf("unknown script = %d", code)
	}
}

func TestParameterValues(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	values := func(query string) (int, string) {
		code, body := e.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values"+query, nil)
		return code, string(body)
	}
	if code, body := values("?Env=prod"); code != http.StatusOK || !strings.Contains(body, `"t-prod-1"`) || !strings.Contains(body, `"t-prod-2"`) {
		t.Errorf("prod: %d %s", code, body)
	}
	if code, body := values(""); code != http.StatusOK || !strings.Contains(body, `"t-dev-1"`) {
		t.Errorf("default env: %d %s", code, body)
	}
	if code, body := values("?Env=bogus"); code != http.StatusBadRequest || !strings.Contains(body, "Env") {
		t.Errorf("invalid dependency: %d %s", code, body)
	}
	if code, body := e.do(http.MethodGet, "/api/scripts/Echo/parameters/Env/values", nil); code != http.StatusOK || !strings.Contains(string(body), `"label":"Production"`) {
		t.Errorf("static values: %d %s", code, body)
	}
	if code, _ := e.do(http.MethodGet, "/api/scripts/Echo/parameters/Nope/values", nil); code != http.StatusNotFound {
		t.Errorf("unknown parameter = %d", code)
	}
}

func multipartBody(t *testing.T, values map[string]any, field, filename string, content []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	v, _ := json.Marshal(values)
	_ = mw.WriteField("values", string(v))
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUpload(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})

	body, ct := multipartBody(t, map[string]any{"Name": "abc"}, "Upload", "../../evil name.txt", []byte("hello upload"))
	code, resp := e.do(http.MethodPost, "/api/scripts/Echo/runs", body, "Content-Type", ct)
	if code != http.StatusCreated {
		t.Fatalf("upload run: %d %s", code, resp)
	}
	var started struct{ ID string }
	_ = json.Unmarshal(resp, &started)
	e.finish(started.ID)
	out := e.output(started.ID)
	if !strings.Contains(out, "content=hello upload") || !strings.Contains(out, "evil_name.txt]") || strings.Contains(out, "..") {
		t.Errorf("upload not received safely:\n%s", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.data, "uploads")); len(entries) != 0 {
		t.Errorf("upload dir not cleaned up: %v", entries)
	}

	body, ct = multipartBody(t, map[string]any{"Name": "abc"}, "Name", "x.txt", []byte("x"))
	if code, resp := e.do(http.MethodPost, "/api/scripts/Echo/runs", body, "Content-Type", ct); code != http.StatusBadRequest {
		t.Errorf("file for a non-upload parameter: %d %s", code, resp)
	}
	body, ct = multipartBody(t, map[string]any{"Name": "abc"}, "Upload", "big.bin", make([]byte, 2<<20))
	if code, resp := e.do(http.MethodPost, "/api/scripts/Echo/runs", body, "Content-Type", ct); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload: %d %s", code, resp)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.data, "uploads")); len(entries) != 0 {
		t.Errorf("rejected uploads not cleaned up: %v", entries)
	}
}

func TestBusyStopKill(t *testing.T) {
	e := setup(t, executor.Limits{MaxConcurrent: 1, KillGrace: 200 * time.Millisecond}, Options{})
	id := e.start("Sleep", nil)
	if code, body := e.do(http.MethodPost, "/api/scripts/Sleep/runs", map[string]any{}); code != http.StatusTooManyRequests {
		t.Errorf("second run = %d %s", code, body)
	}
	e.waitOutput(id, "started")
	if code, _ := e.do(http.MethodPost, "/api/runs/"+id+"/kill", nil); code != http.StatusAccepted {
		t.Errorf("kill = %d", code)
	}
	if meta := e.finish(id); meta.Status != "killed" {
		t.Errorf("status = %s", meta.Status)
	}
	if code, _ := e.do(http.MethodPost, "/api/runs/"+id+"/stop", nil); code != http.StatusConflict {
		t.Errorf("stop finished run = %d", code)
	}
	if code, _ := e.do(http.MethodPost, "/api/runs/nope/stop", nil); code != http.StatusNotFound {
		t.Errorf("stop unknown run = %d", code)
	}

	id = e.start("Sleep", nil)
	e.waitOutput(id, "started")
	if code, _ := e.do(http.MethodPost, "/api/runs/"+id+"/stop", nil); code != http.StatusAccepted {
		t.Errorf("stop = %d", code)
	}
	if meta := e.finish(id); meta.Status != "stopped" {
		t.Errorf("status = %s", meta.Status)
	}
}

func TestInput(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	id := e.start("Prompt", nil)
	e.waitOutput(id, "Continue? ")
	if code, body := e.do(http.MethodPost, "/api/runs/"+id+"/input", map[string]any{"line": "yes"}); code != http.StatusNoContent {
		t.Fatalf("input = %d %s", code, body)
	}
	e.finish(id)
	if out := e.output(id); !strings.Contains(out, "answer=yes") {
		t.Errorf("output:\n%s", out)
	}
	if code, _ := e.do(http.MethodPost, "/api/runs/"+id+"/input", map[string]any{"line": "late"}); code != http.StatusConflict {
		t.Errorf("input to finished run = %d", code)
	}
}

// dial opens a WebSocket to a run and returns the handshake status code.
func (e *testEnv) dial(id, query string, header http.Header) (*websocket.Conn, int, error) {
	url := "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/api/runs/" + id + "/ws" + query
	if header == nil {
		header = http.Header{}
	}
	e.identify(header)
	c, resp, err := websocket.Dial(e.t.Context(), url, &websocket.DialOptions{HTTPHeader: header})
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	if err == nil {
		c.SetReadLimit(1 << 20)
		e.t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, status, err
}

// readUntilResult collects output frames until the result frame.
func readUntilResult(t *testing.T, c *websocket.Conn, onOutput func(all string)) (string, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()
	var all strings.Builder
	for {
		var msg map[string]any
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			t.Fatalf("read: %v (output so far %q)", err, all.String())
		}
		switch msg["type"] {
		case "output":
			all.WriteString(msg["data"].(string))
			if int(msg["offset"].(float64)) == 0 {
				t.Error("offset missing")
			}
			if onOutput != nil {
				onOutput(all.String())
			}
		case "result":
			return all.String(), msg
		default:
			t.Fatalf("unexpected message %v", msg)
		}
	}
}

func TestWebSocketLive(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	id := e.start("Prompt", nil)
	c, _, err := e.dial(id, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	answered := false
	out, result := readUntilResult(t, c, func(all string) {
		if !answered && strings.Contains(all, "Continue? ") {
			answered = true
			if err := wsjson.Write(t.Context(), c, wsIn{Type: "input", Line: "yes"}); err != nil {
				t.Errorf("send input: %v", err)
			}
		}
	})
	if result["status"] != "finished" || !strings.Contains(out, "answer=yes") {
		t.Errorf("result %v, output %q", result, out)
	}
}

func TestWebSocketStoredWithOffset(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	id := e.start("Echo", map[string]any{"Name": "abc"})
	e.finish(id)
	full := e.rawOutput(id) // offsets count raw bytes, terminal CRLF included

	c, _, err := e.dial(id, "?offset=5", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, result := readUntilResult(t, c, nil)
	if out != full[5:] || result["status"] != "finished" {
		t.Errorf("got %q (result %v), want %q", out, result, full[5:])
	}
}

func TestWebSocketOrigin(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{AllowedOrigins: []string{"trusted.example"}})
	id := e.start("Echo", map[string]any{"Name": "abc"})
	e.finish(id)

	_, status, err := e.dial(id, "", http.Header{"Origin": {"https://evil.example"}})
	if err == nil {
		t.Fatal("cross-origin WebSocket accepted")
	}
	if status != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403", status)
	}
	if _, _, err := e.dial(id, "", http.Header{"Origin": {"https://trusted.example"}}); err != nil {
		t.Errorf("allowed origin rejected: %v", err)
	}
}

func TestOutputFiles(t *testing.T) {
	e := setup(t, executor.Limits{}, Options{})
	id := e.start("Report", nil)
	meta := e.finish(id)
	if !slices.Equal(meta.Files, []string{"report.txt"}) {
		t.Fatalf("files = %q", meta.Files)
	}
	code, body := e.do(http.MethodGet, "/api/runs/"+id+"/files/report.txt", nil)
	if code != http.StatusOK || string(body) != "report body\n" {
		t.Errorf("download: %d %q", code, body)
	}
	for _, bad := range []string{"..%2Fmeta.json", "meta.json", "nope.txt"} {
		if code, _ := e.do(http.MethodGet, "/api/runs/"+id+"/files/"+bad, nil); code != http.StatusNotFound {
			t.Errorf("file %q = %d, want 404", bad, code)
		}
	}
}

func TestAuthorization(t *testing.T) {
	alice := setup(t, executor.Limits{}, Options{}) // in group ops
	bob := alice.as("bob", "")
	carol := alice.as("carol", "") // admin of Restricted
	root := alice.as("ROOT", "")   // global admin, matched case-insensitively
	nobody := alice.as("", "")

	if code, _ := nobody.do(http.MethodGet, "/api/scripts", nil); code != http.StatusUnauthorized {
		t.Errorf("signed out = %d, want 401", code)
	}

	_, body := bob.do(http.MethodGet, "/api/scripts", nil)
	if strings.Contains(string(body), "Restricted") || !strings.Contains(string(body), "Echo") {
		t.Errorf("bob's script list: %s", body)
	}
	if code, _ := bob.do(http.MethodGet, "/api/scripts/Restricted", nil); code != http.StatusNotFound {
		t.Errorf("bob reads Restricted = %d, want 404", code)
	}
	if code, _ := bob.do(http.MethodPost, "/api/scripts/Restricted/runs", map[string]any{}); code != http.StatusNotFound {
		t.Errorf("bob runs Restricted = %d, want 404", code)
	}

	id := alice.start("Restricted", nil)
	alice.finish(id)
	if out := alice.output(id); !strings.Contains(out, "user=alice") {
		t.Errorf("alice's output: %s", out)
	}

	for _, path := range []string{"", "/output", "/files/x.txt"} {
		if code, _ := bob.do(http.MethodGet, "/api/runs/"+id+path, nil); code != http.StatusNotFound {
			t.Errorf("bob GET run%s = %d, want 404", path, code)
		}
	}
	if code, _ := bob.do(http.MethodPost, "/api/runs/"+id+"/stop", nil); code != http.StatusNotFound {
		t.Errorf("bob stops alice's run = %d, want 404", code)
	}
	if _, status, err := bob.dial(id, "", nil); err == nil || status != http.StatusNotFound {
		t.Errorf("bob's WebSocket: status %d, err %v", status, err)
	}
	if _, body := bob.do(http.MethodGet, "/api/runs", nil); strings.Contains(string(body), id) {
		t.Errorf("bob's history lists alice's run: %s", body)
	}

	for _, viewer := range []*testEnv{alice, carol, root} {
		if code, _ := viewer.do(http.MethodGet, "/api/runs/"+id, nil); code != http.StatusOK {
			t.Errorf("%s GET run = %d, want 200", viewer.user, code)
		}
		if _, body := viewer.do(http.MethodGet, "/api/runs", nil); !strings.Contains(string(body), id) {
			t.Errorf("%s's history misses the run", viewer.user)
		}
	}
	// carol administers Restricted only: alice's Echo runs stay private.
	echo := alice.start("Echo", map[string]any{"Name": "abc"})
	alice.finish(echo)
	if code, _ := carol.do(http.MethodGet, "/api/runs/"+echo, nil); code != http.StatusNotFound {
		t.Errorf("carol GET alice's Echo run = %d, want 404", code)
	}
}

func TestCompleteUTF8(t *testing.T) {
	s := []byte("aé€😀")
	for n := 0; n <= len(s); n++ {
		cut := completeUTF8(s[:n])
		if !utf8Valid(s[:cut]) || cut < n-3 {
			t.Errorf("completeUTF8(%q) = %d", s[:n], cut)
		}
	}
	if completeUTF8(s) != len(s) {
		t.Error("complete input shortened")
	}
}

func utf8Valid(b []byte) bool { return strings.ToValidUTF8(string(b), "\uFFFD") == string(b) }
