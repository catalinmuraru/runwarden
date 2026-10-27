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

	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/params"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

const testToken = "executor-token-prod" //nolint:gosec // test value

// pair is a server with a Hub and one connected executor of "prod".
type pair struct {
	hub    *Hub
	agent  *Agent
	cancel context.CancelFunc
	done   chan struct{} // closed when the agent has returned
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

func startPair(t *testing.T, limits executor.Limits) *pair {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("executor tests need Linux")
	}
	hub, ts := newHub(t)
	sec := t.TempDir()
	for _, env := range []string{"prod", secrets.Common} {
		if err := os.MkdirAll(filepath.Join(sec, env), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sec, "prod", "DB_PASSWORD"), []byte("prod-pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		URL: ts.URL, Token: testToken, Environment: "prod", Pod: "prod-0", Version: "test",
		Exec:      executor.New(limits, executor.EnvPolicy{TempRoot: t.TempDir(), PrivateHome: true}),
		Secrets:   secrets.Store{Dir: sec},
		UploadDir: t.TempDir(),
		Heartbeat: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pair{hub: hub, agent: a, cancel: cancel, done: make(chan struct{})}
	go func() {
		_ = a.Run(ctx)
		close(p.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-p.done
	})
	deadline := time.Now().Add(5 * time.Second)
	for hub.Pick("prod") == nil {
		if time.Now().After(deadline) {
			t.Fatal("executor did not register")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p
}

func script(t *testing.T, name string, args ...string) []string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{"/bin/bash", abs}, args...)
}

func job(id string, command []string) executor.Job {
	return executor.Job{ID: id, Script: "test", User: "alice", Command: command, Dir: os.TempDir(), Timeout: time.Minute}
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
	j := job("20261027-120000.000-0001", script(t, "echo.sh", serverPath, "--key", "s3cret"))
	j.Secure = []string{"s3cret"}
	j.Stdin = []params.StdinInput{}
	r, _, err := conn.Start(context.Background(), j, StartOptions{
		Secrets: []string{"DB_PASSWORD"},
		Files:   []File{{Path: serverPath, Data: []byte("uploaded content")}},
	})
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
	for _, want := range []string{"secret: ******", "file: uploaded content", "input: hello there", "--key ******"} {
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
	j := job("tty-1", script(t, "tty.sh"))
	j.Terminal, j.Rows, j.Cols = true, 30, 100
	r, _, err := p.hub.Pick("prod").Start(context.Background(), j, StartOptions{})
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
	conn := p.hub.Pick("prod")

	r, _, err := conn.Start(context.Background(), job("stop-1", script(t, "wait.sh")), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, r, "started")
	r.Stop()
	if res := wait(t, r); res.Status != executor.StatusStopped || !strings.Contains(output(r), "got TERM") {
		t.Errorf("stop: %+v\n%s", res, output(r))
	}

	r, _, err = conn.Start(context.Background(), job("kill-1", script(t, "wait.sh")), StartOptions{})
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

	r, _, err = conn.Start(context.Background(), job("eof-1", script(t, "echo.sh")), StartOptions{})
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
	r, _, err := p.hub.Pick("prod").Start(context.Background(), job("lost-1", script(t, "wait.sh")), StartOptions{})
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

func TestHelperCapture(t *testing.T) {
	p := startPair(t, executor.Limits{})
	conn := p.hub.Pick("prod")
	out, err := conn.Capture(context.Background(), script(t, "helper.sh"), os.TempDir(), []string{"DB_PASSWORD"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "one\ntwo\n******\n" {
		t.Errorf("helper output %q", got)
	}
	if _, err := conn.Capture(context.Background(), script(t, "helper.sh"), os.TempDir(), []string{"MISSING"}, false); err == nil || !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("missing secret: %v", err)
	}
	if _, err := conn.Capture(context.Background(), []string{"/nonexistent/helper"}, os.TempDir(), nil, false); err == nil {
		t.Error("failing helper: no error")
	}
}

func TestBusyAndBadStarts(t *testing.T) {
	p := startPair(t, executor.Limits{MaxConcurrent: 1})
	conn := p.hub.Pick("prod")
	r, _, err := conn.Start(context.Background(), job("busy-1", script(t, "wait.sh")), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Start(context.Background(), job("busy-2", script(t, "wait.sh")), StartOptions{}); !errors.Is(err, executor.ErrBusy) {
		t.Errorf("second start: %v, want ErrBusy", err)
	}
	r.Kill()
	wait(t, r)
	if _, _, err := conn.Start(context.Background(), job("../evil", script(t, "wait.sh")), StartOptions{}); err == nil {
		t.Error("path-like run ID accepted")
	}
	if _, _, err := conn.Start(context.Background(), job("nosecret-1", script(t, "wait.sh")), StartOptions{Secrets: []string{"MISSING"}}); err == nil || !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("missing secret: %v", err)
	}
	if conn.Busy() != 0 {
		t.Errorf("busy = %d after refused starts", conn.Busy())
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
