package remote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/catalinmuraru/ward/internal/compat"
	"github.com/catalinmuraru/ward/internal/config"
	"github.com/catalinmuraru/ward/internal/executor"
	"github.com/catalinmuraru/ward/internal/params"
	"github.com/catalinmuraru/ward/internal/secrets"
)

const testToken = "executor-token-prod" //nolint:gosec // test value

// pair is a server with a Hub and one connected executor of "prod".
type pair struct {
	hub    *Hub
	agent  *Agent
	cat    *config.Catalog // the runner configs of server and executor
	cancel context.CancelFunc
	done   chan struct{} // closed when the agent has returned
}

// hash is the config hash the server sends for script name.
func (p *pair) hash(name string) string { return p.cat.Script(name).Hash() }

// start starts j as the server does, with the hash of the server's config of its script.
func (p *pair) start(j executor.Job, files ...File) (*executor.Run, error) {
	r, _, err := p.hub.Pick("prod").Start(context.Background(), j, StartOptions{ConfigHash: p.hash(j.Script), Files: files})
	return r, err
}

func newHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	hub, err := NewHub(HubOptions{Tokens: map[string]string{"prod": HashToken(testToken)}, Heartbeat: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(Path, hub)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return hub, ts
}

// testCatalog is the runner configs of the tests: the scripts in testdata.
func testCatalog(t *testing.T) *config.Catalog {
	t.Helper()
	sh := func(name string) []string {
		abs, err := filepath.Abs(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return []string{"/bin/bash", abs}
	}
	dir := os.TempDir()
	key := &params.Parameter{Name: "key", Param: "--key", Type: params.TypeText, Secure: true}
	return &config.Catalog{Scripts: []*config.Script{
		{Name: "echo", Command: sh("echo.sh"), WorkingDirectory: dir, Secrets: []string{"DB_PASSWORD"}, Parameters: []*params.Parameter{key}},
		{Name: "plain", Command: sh("echo.sh"), WorkingDirectory: dir},
		{Name: "wait", Command: sh("wait.sh"), WorkingDirectory: dir},
		{Name: "tty", Command: sh("tty.sh"), WorkingDirectory: dir, RequiresTerminal: true},
		{Name: "nosecret", Command: sh("wait.sh"), WorkingDirectory: dir, Secrets: []string{"MISSING"}, Parameters: []*params.Parameter{
			{Name: "x", Type: params.TypeList, ValuesScript: "/bin/echo x"},
		}},
		{Name: "helper", Command: sh("wait.sh"), WorkingDirectory: dir, Secrets: []string{"DB_PASSWORD"}, Parameters: []*params.Parameter{
			{Name: "Env", Type: params.TypeText},
			{Name: "choice", Type: params.TypeList, ValuesScript: strings.Join(sh("helper.sh"), " ") + " ${Env}", DefaultScript: "testdata/helper.sh"},
			{Name: "bad", Type: params.TypeList, ValuesScript: "${Env} list"},
		}},
	}}
}

// newAgent is an executor of "prod" connecting to url, with the configs cat and a secret
// DB_PASSWORD.
func newAgent(t *testing.T, url string, cat *config.Catalog, limits executor.Limits) *Agent {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("executor tests need Linux")
	}
	sec := t.TempDir()
	for _, env := range []string{"prod", secrets.Common} {
		if err := os.MkdirAll(filepath.Join(sec, env), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sec, "prod", "DB_PASSWORD"), []byte("prod-pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	return &Agent{
		URL: url, Token: testToken, Environment: "prod", Pod: "prod-0", Version: "test",
		Exec:      executor.New(limits, executor.EnvPolicy{TempRoot: t.TempDir(), PrivateHome: true}),
		Catalog:   func() *config.Catalog { return cat },
		BaseDir:   base,
		Secrets:   secrets.Store{Dir: sec},
		UploadDir: t.TempDir(),
		Heartbeat: time.Second,
	}
}

// runAgent runs a until the test ends.
func runAgent(t *testing.T, a *Agent) (context.CancelFunc, chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = a.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

func startPair(t *testing.T, limits executor.Limits) *pair {
	t.Helper()
	hub, ts := newHub(t)
	cat := testCatalog(t)
	a := newAgent(t, ts.URL, cat, limits)
	cancel, done := runAgent(t, a)
	p := &pair{hub: hub, agent: a, cat: cat, cancel: cancel, done: done}
	deadline := time.Now().Add(5 * time.Second)
	for hub.Pick("prod") == nil {
		if time.Now().After(deadline) {
			t.Fatal("executor did not register")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p
}

func job(id, script string) executor.Job {
	return executor.Job{ID: id, Script: script, User: "alice", Timeout: time.Minute}
}

func wait(t *testing.T, r *executor.Run) executor.Result {
	t.Helper()
	select {
	case <-r.Done():
		return r.Result()
	case <-time.After(10 * time.Second):
		t.Fatalf("run %s did not finish:\n%s", r.ID, output(r))
		return executor.Result{}
	}
}

func output(r *executor.Run) string {
	b, _, _ := r.Output(0)
	return string(b)
}

func waitFor(t *testing.T, r *executor.Run, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(output(r), text) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q in output:\n%s", text, output(r))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting: " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunWithSecretsFilesAndInput(t *testing.T) {
	p := startPair(t, executor.Limits{})
	conn := p.hub.Pick("prod")
	info := conn.Info()
	if info.Pod != "prod-0" || info.Capacity != 0 && info.Capacity < 1 || len(info.Secrets) != 1 || info.Secrets[0].Name != "DB_PASSWORD" {
		t.Errorf("hello = %+v", info)
	}
	serverPath := "/data/uploads/abc/0_report.txt"
	j := job("20261027-120000.000-0001", "echo")
	j.ParamArgs = []string{serverPath, "--key", "s3cret"}
	j.Env = map[string]string{"PARAM_KEY": "from-env", "EXECUTION_ID": j.ID}
	j.Secure = []string{"s3cret"}
	j.Stdin = []params.StdinInput{}
	r, err := p.start(j, File{Path: serverPath, Data: []byte("uploaded content")})
	if err != nil {
		t.Fatal(err)
	}
	if conn.Busy() != 1 {
		t.Errorf("busy = %d, want 1", conn.Busy())
	}
	waitFor(t, r, "file: ")
	if err := r.WriteInput("hello there", false); err != nil {
		t.Fatal(err)
	}
	res := wait(t, r)
	out := output(r)
	if res.Status != executor.StatusFinished || res.ExitCode != 0 {
		t.Errorf("result %+v:\n%s", res, out)
	}
	for _, want := range []string{"secret: ******", "env: from-env", "file: uploaded content", "input: hello there", "--key ******"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "prod-pw") || strings.Contains(out, "s3cret") || strings.Contains(out, serverPath) {
		t.Errorf("secret or server path in output:\n%s", out)
	}
	if conn.Busy() != 0 {
		t.Errorf("busy = %d after the run", conn.Busy())
	}
	eventually(t, "uploaded files removed on the executor", func() bool {
		entries, _ := os.ReadDir(p.agent.UploadDir)
		return len(entries) == 0
	})
}

func TestTerminalAndResize(t *testing.T) {
	p := startPair(t, executor.Limits{})
	j := job("tty-1", "tty") // the terminal comes from the executor's config
	j.Rows, j.Cols = 30, 100
	r, err := p.start(j)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, r, "30 100")
	if err := r.Resize(45, 132); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteInput("", false); err != nil {
		t.Fatal(err)
	}
	wait(t, r)
	if out := output(r); !strings.Contains(out, "/dev/pts/") || !strings.Contains(out, "45 132") {
		t.Errorf("terminal output:\n%s", out)
	}
}

func TestStopKillAndCloseInput(t *testing.T) {
	p := startPair(t, executor.Limits{})

	r, err := p.start(job("stop-1", "wait"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, r, "started")
	r.Stop()
	if res := wait(t, r); res.Status != executor.StatusStopped || !strings.Contains(output(r), "got TERM") {
		t.Errorf("stop: %+v\n%s", res, output(r))
	}

	r, err = p.start(job("kill-1", "wait"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, r, "started")
	r.Kill()
	if res := wait(t, r); res.Status != executor.StatusKilled {
		t.Errorf("kill: %+v", res)
	}
	if err := r.WriteInput("late", false); !errors.Is(err, executor.ErrInputClosed) {
		t.Errorf("input after the end: %v", err)
	}

	r, err = p.start(job("eof-1", "plain"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CloseInput(); err != nil {
		t.Fatal(err)
	}
	if res := wait(t, r); res.Status != executor.StatusFinished || !strings.Contains(output(r), "input: \n") {
		t.Errorf("eof: %+v\n%s", res, output(r))
	}
}

func TestDisconnectFailsRuns(t *testing.T) {
	p := startPair(t, executor.Limits{})
	r, err := p.start(job("lost-1", "wait"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, r, "started")
	p.cancel() // the executor pod goes away
	res := wait(t, r)
	if res.Status != executor.StatusFailed || res.ExitCode != -1 || !strings.Contains(output(r), "executor connection lost") {
		t.Errorf("result %+v:\n%s", res, output(r))
	}
	<-p.done
	eventually(t, "disconnected executor unlisted", func() bool { return p.hub.Pick("prod") == nil && len(p.hub.Executors("prod")) == 0 })
	eventually(t, "runs killed on the executor", func() bool { return len(p.agent.Exec.Running()) == 0 })
}

func TestShutdownStopsRunsGracefully(t *testing.T) {
	p := startPair(t, executor.Limits{})
	r, err := p.start(job("drain-1", "wait"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, r, "started")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.agent.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// The pod's SIGTERM reaches the server as a stopped run, not a lost connection.
	if res := wait(t, r); res.Status != executor.StatusStopped || !strings.Contains(output(r), "got TERM") {
		t.Errorf("result %+v:\n%s", res, output(r))
	}
	if _, err := p.start(job("drain-2", "wait")); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("start while shutting down: %v", err)
	}
}

func TestHelperCapture(t *testing.T) {
	p := startPair(t, executor.Limits{})
	conn := p.hub.Pick("prod")
	capture := func(param, kind string, values map[string]string) ([]byte, error) {
		return conn.Capture(context.Background(), Capture{Script: "helper", Param: param, Kind: kind, Values: values, ConfigHash: p.hash("helper")})
	}
	// The executor expands its own template: a value stays one argument, whatever it holds.
	out, err := capture("choice", config.HelperValues, map[string]string{"Env": "prod; rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "arg: prod; rm -rf /\none\ntwo\n******\n" {
		t.Errorf("helper output %q", got)
	}
	// A relative program resolves against the executor's BASE_DIR; secrets come from its config.
	if out, err := capture("choice", config.HelperDefault, nil); err != nil || string(out) != "one\ntwo\n******\n" {
		t.Errorf("default helper: %q, %v", out, err)
	}
	hash := p.hash("helper")
	for name, c := range map[string]Capture{
		"unknown script":        {Script: "nope", Param: "choice", Kind: config.HelperValues},
		"unknown parameter":     {Script: "helper", Param: "nope", Kind: config.HelperValues, ConfigHash: hash},
		"no such helper":        {Script: "helper", Param: "Env", Kind: config.HelperValues, ConfigHash: hash},
		"unknown kind":          {Script: "helper", Param: "choice", Kind: "command", ConfigHash: hash},
		"missing value":         {Script: "helper", Param: "choice", Kind: config.HelperValues, ConfigHash: hash},
		"program from a value":  {Script: "helper", Param: "bad", Kind: config.HelperValues, Values: map[string]string{"Env": "/bin/sh"}, ConfigHash: hash},
		"secret missing on pod": {Script: "nosecret", Param: "x", Kind: config.HelperValues, ConfigHash: p.hash("nosecret")},
	} {
		if _, err := conn.Capture(context.Background(), c); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	_, err = conn.Capture(context.Background(), Capture{Script: "helper", Param: "choice", Kind: config.HelperValues, Values: map[string]string{"Env": "x"}, ConfigHash: "other"})
	if err == nil || err.Error() != "runner config of helper differs between server and executor; roll the executors" {
		t.Errorf("config drift: %v", err)
	}
}

func TestBusyAndBadStarts(t *testing.T) {
	p := startPair(t, executor.Limits{MaxConcurrent: 1})
	conn := p.hub.Pick("prod")
	r, err := p.start(job("busy-1", "wait"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.start(job("busy-2", "wait")); !errors.Is(err, executor.ErrBusy) {
		t.Errorf("second start: %v, want ErrBusy", err)
	}
	r.Kill()
	wait(t, r)
	if _, err := p.start(job("../evil", "wait")); err == nil {
		t.Error("path-like run ID accepted")
	}
	if _, err := p.start(job("nosecret-1", "nosecret")); err == nil || !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("missing secret: %v", err)
	}
	if conn.Busy() != 0 {
		t.Errorf("busy = %d after refused starts", conn.Busy())
	}
}

func TestStartRefusals(t *testing.T) {
	p := startPair(t, executor.Limits{})
	conn := p.hub.Pick("prod")
	start := func(j executor.Job, hash string) error {
		_, _, err := conn.Start(context.Background(), j, StartOptions{ConfigHash: hash})
		return err
	}
	if err := start(job("unknown-1", "nope"), "x"); err == nil || !strings.Contains(err.Error(), "not in the runner configs") {
		t.Errorf("unknown script: %v", err)
	}
	err := start(job("drift-1", "wait"), "other")
	if err == nil || err.Error() != "runner config of wait differs between server and executor; roll the executors" {
		t.Errorf("config drift: %v", err)
	}
	j := job("env-1", "echo")
	j.Env = map[string]string{"LD_PRELOAD": "/tmp/evil.so"}
	if err := start(j, p.hash("echo")); err == nil || !strings.Contains(err.Error(), "LD_PRELOAD is not declared") {
		t.Errorf("undeclared variable: %v", err)
	}
	if conn.Busy() != 0 || len(p.agent.Exec.Running()) != 0 {
		t.Error("a refused start runs")
	}
}

// fakeServer stands in for the server (or someone posing as it): it registers one executor
// with the test configs and hands the connection to the test.
func fakeServer(t *testing.T) *websocket.Conn {
	t.Helper()
	conns := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		var m Message
		if err := wsjson.Read(r.Context(), ws, &m); err != nil || m.Type != typeHello {
			return
		}
		if err := wsjson.Write(r.Context(), ws, Message{Type: typeWelcome}); err != nil {
			return
		}
		conns <- ws
		<-done
	}))
	t.Cleanup(func() {
		close(done)
		srv.Close()
	})
	runAgent(t, newAgent(t, srv.URL, testCatalog(t), executor.Limits{}))
	select {
	case ws := <-conns:
		return ws
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not connect")
		return nil
	}
}

// exchange sends m (any JSON, so it may carry fields the protocol does not know) and collects
// the answers until the run or helper call ends.
func exchange(t *testing.T, ws *websocket.Conn, m any) (string, Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, ws, m); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for {
		var r Message
		if err := wsjson.Read(ctx, ws, &r); err != nil {
			t.Fatalf("%v; output so far:\n%s", err, out.String())
		}
		switch r.Type {
		case typeStarted:
			_ = wsjson.Write(ctx, ws, Message{Type: typeEOF, ID: r.ID})
		case typeOutput:
			out.Write(r.Data)
		case typeExit, typeFailed, typeCaptured:
			return out.String(), r
		}
	}
}

func TestExecutorRunsItsOwnConfig(t *testing.T) {
	ws := fakeServer(t)
	cat := testCatalog(t)
	// A start that names another program, directory and secrets, as protocol 1 carried them:
	// the executor runs its config of "plain" (no secrets) with the parameter arguments only.
	out, last := exchange(t, ws, map[string]any{
		"type": typeStart, "id": "fake-1",
		"start": map[string]any{
			"job": map[string]any{
				"id": "fake-1", "script": "plain", "user": "mallory",
				"command": []string{"/bin/sh", "-c", "echo pwned"},
				"program": "/bin/sh", "args": []string{"-c", "echo pwned"}, "dir": "/",
			},
			"param_args":  []string{"hello"},
			"secrets":     []string{"DB_PASSWORD"},
			"config_hash": cat.Script("plain").Hash(),
		},
	})
	if last.Type != typeExit || strings.Contains(out, "pwned") || !strings.Contains(out, "args: hello\n") || !strings.Contains(out, "secret: \n") {
		t.Errorf("%s %+v:\n%s", last.Type, last.Exit, out)
	}
	if strings.Contains(out, "prod-pw") {
		t.Errorf("secret value in output:\n%s", out)
	}
	// A helper call that names a shell command: the executor runs its own template.
	_, last = exchange(t, ws, map[string]any{
		"type": typeCapture, "id": "helper-1",
		"capture": map[string]any{
			"command": []string{"/bin/sh", "-c", "echo pwned"}, "dir": "/",
			"script": "helper", "param": "choice", "kind": config.HelperValues,
			"values": map[string]string{"Env": "$(echo pwned)"}, "config_hash": cat.Script("helper").Hash(),
		},
	})
	if last.Type != typeCaptured || string(last.Data) != "arg: $(echo pwned)\none\ntwo\n******\n" {
		t.Errorf("helper: %s %q %s", last.Type, last.Data, last.Error)
	}
	// A start without a config hash (protocol 1) is refused.
	_, last = exchange(t, ws, map[string]any{
		"type": typeStart, "id": "fake-2",
		"start": map[string]any{"job": map[string]any{"id": "fake-2", "script": "plain", "command": []string{"/bin/sh"}}},
	})
	if last.Type != typeFailed || !strings.Contains(last.Error, "differs between server and executor") {
		t.Errorf("start without hash: %+v", last)
	}
}

func TestProtocolMismatchRefused(t *testing.T) {
	hub, ts := newHub(t)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	header.Set(EnvironmentHeader, "prod")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, ts.URL+Path, &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	if err := wsjson.Write(ctx, ws, Message{Type: typeHello, Hello: &Hello{Protocol: Protocol - 1, Pod: "old-0"}}); err != nil {
		t.Fatal(err)
	}
	var m Message
	err = wsjson.Read(ctx, ws, &m)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Errorf("old protocol: %v (%+v)", err, m)
	}
	if len(hub.Executors("prod")) != 0 {
		t.Error("executor with another protocol registered")
	}
}

func TestBadTokenRefused(t *testing.T) {
	hub, ts := newHub(t)
	for name, a := range map[string]*Agent{
		"wrong token":       {URL: ts.URL, Token: "wrong", Environment: "prod"},
		"other environment": {URL: ts.URL, Token: testToken, Environment: "dev"},
		"no token":          {URL: ts.URL, Environment: "prod"},
	} {
		err := a.session(context.Background())
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(hub.Executors("prod")) != 0 || len(hub.Executors("dev")) != 0 {
		t.Error("refused executor registered")
	}
	if _, err := NewHub(HubOptions{Tokens: map[string]string{"prod": "not-hex"}}); err == nil {
		t.Error("malformed token hash accepted")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"report.txt": "report.txt", "evil name;rm.sh": "evil_name_rm.sh", "..": "file", "": "file"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// v0.5: executors that predate the rename send only the former header name.
func TestLegacyEnvironmentHeader(t *testing.T) {
	_, ts := newHub(t)
	for name, want := range map[string]bool{compat.LegacyEnvironmentHeader: true, "X-Other": false} {
		header := http.Header{}
		header.Set("Authorization", "Bearer "+testToken)
		header.Set(name, "prod")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ws, resp, err := websocket.Dial(ctx, ts.URL+Path, &websocket.DialOptions{HTTPHeader: header})
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if (err == nil) != want {
			t.Errorf("%s: dial err = %v, want accepted %v", name, err, want)
		}
		if ws != nil {
			_ = ws.CloseNow()
		}
	}
}
