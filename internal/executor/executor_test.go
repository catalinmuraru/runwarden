//go:build unix

package executor

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/params"
)

const testWait = 20 * time.Second

func testScript(t *testing.T, file string, ps ...*params.Parameter) *config.Script {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	return &config.Script{Name: file, Command: []string{path}, WorkingDirectory: t.TempDir(), Parameters: ps}
}

func mustStart(t *testing.T, e *Executor, req Request) *Run {
	t.Helper()
	r, err := e.Start(req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		r.Kill()
		<-r.Done()
	})
	return r
}

func waitDone(t *testing.T, r *Run) Result {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(testWait):
		t.Fatalf("run %s did not finish; output so far:\n%s", r.ID, outputOf(r))
	}
	return r.Result()
}

func outputOf(r *Run) string {
	data, _, _ := r.Output(0)
	return string(data)
}

// waitOutput blocks until the run's output contains text.
func waitOutput(t *testing.T, r *Run, text string) {
	t.Helper()
	deadline := time.After(testWait)
	for {
		changed := r.Changed()
		data, _, done := r.Output(0)
		if strings.Contains(string(data), text) {
			return
		}
		if done {
			t.Fatalf("run finished without printing %q; output:\n%s", text, data)
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("timed out waiting for %q; output:\n%s", text, data)
		}
	}
}

func pidFrom(t *testing.T, out, key string) int {
	t.Helper()
	m := regexp.MustCompile(key + `=(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no %s= in output:\n%s", key, out)
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}

// waitGone waits until pid no longer exists (orphans are reaped by init).
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive", pid)
}

func TestArgsEnvAndWorkingDir(t *testing.T) {
	t.Setenv("APP_SECRET", "must-not-leak")
	t.Setenv("RUNNER_ENV_GREETING", "hello")
	s := testScript(t, "args.sh",
		&params.Parameter{Name: "name", Param: "--name"},
		&params.Parameter{Name: "pattern"},
		&params.Parameter{Name: "hijack", EnvVar: "LD_PRELOAD", PassAs: params.PassEnv},
	)
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: s, Values: map[string]params.Value{
		"name":    params.Scalar("two words; echo injected"),
		"pattern": params.Scalar("*"),
		"hijack":  params.Scalar("/tmp/evil.so"),
	}})
	res := waitDone(t, r)
	out := outputOf(r)

	if res.Status != StatusFinished || res.ExitCode != 0 {
		t.Fatalf("result = %+v\n%s", res, out)
	}
	for _, want := range []string{
		"[--name]\n[two words; echo injected]\n[*]\n",
		"pwd=" + s.WorkingDirectory + "\n",
		"env:GREETING=hello\n",
		"env:EXECUTION_ID=" + r.ID + "\n",
		"env:PARAM_NAME=two words; echo injected\n",
		"env:PATH=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{"APP_SECRET", "must-not-leak", "RUNNER_ENV_", "LD_PRELOAD", "evil.so", "\ninjected"} {
		if strings.Contains(out, leak) {
			t.Errorf("output contains %q:\n%s", leak, out)
		}
	}
}

func TestOutputOrder(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: testScript(t, "order.sh")})
	waitDone(t, r)
	if got, want := outputOf(r), "out1\nerr1\nout2\nerr2\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestInteractiveInput(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: testScript(t, "prompt.sh")})
	waitOutput(t, r, "Continue? ")
	if err := r.WriteInput("yes"); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, r, "Name? ")
	if err := r.WriteInput("Ada"); err != nil {
		t.Fatal(err)
	}
	res := waitDone(t, r)
	out := outputOf(r)
	if res.Status != StatusFinished || !strings.Contains(out, "first=yes") || !strings.Contains(out, "second=Ada") {
		t.Errorf("result %+v, output:\n%s", res, out)
	}
	if err := r.WriteInput("late"); !errors.Is(err, ErrInputClosed) {
		t.Errorf("WriteInput after exit = %v, want ErrInputClosed", err)
	}
}

func TestStdinFromParameters(t *testing.T) {
	s := testScript(t, "prompt.sh",
		&params.Parameter{Name: "first", PassAs: params.PassStdin},
		&params.Parameter{Name: "second", PassAs: params.PassStdin, StdinExpectedText: "Name? "},
	)
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: s, Values: map[string]params.Value{
		"first":  params.Scalar("ok"),
		"second": params.Scalar("Grace"),
	}})
	res := waitDone(t, r)
	out := outputOf(r)
	if res.Status != StatusFinished || !strings.Contains(out, "first=ok") || !strings.Contains(out, "second=Grace") {
		t.Errorf("result %+v, output:\n%s", res, out)
	}
}

func TestCloseInput(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: testScript(t, "prompt.sh")})
	waitOutput(t, r, "Continue? ")
	if err := r.CloseInput(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, r) // reads see end of input, the script runs to its end
	if err := r.WriteInput("x"); !errors.Is(err, ErrInputClosed) {
		t.Errorf("WriteInput after CloseInput = %v", err)
	}
}

func TestStopKillsProcessGroup(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: testScript(t, "group.sh")})
	waitOutput(t, r, "self=")
	out := outputOf(r)
	child, self := pidFrom(t, out, "child"), pidFrom(t, out, "self")

	r.Stop()
	if res := waitDone(t, r); res.Status != StatusStopped {
		t.Errorf("status = %s, want %s", res.Status, StatusStopped)
	}
	waitGone(t, self)
	waitGone(t, child)
}

func TestStopEscalatesToKill(t *testing.T) {
	grace := 300 * time.Millisecond
	r := mustStart(t, New(Limits{KillGrace: grace}, EnvPolicy{}), Request{Script: testScript(t, "ignore_term.sh")})
	waitOutput(t, r, "ready")
	stopped := time.Now()
	r.Stop()
	res := waitDone(t, r)
	if res.Status != StatusStopped || res.ExitCode != 128+int(syscall.SIGKILL) {
		t.Errorf("result = %+v, want stopped by SIGKILL", res)
	}
	if elapsed := time.Since(stopped); elapsed < grace {
		t.Errorf("killed after %v, before the %v grace period", elapsed, grace)
	}
}

func TestKill(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: testScript(t, "sleep.sh")})
	waitOutput(t, r, "started")
	r.Kill()
	if res := waitDone(t, r); res.Status != StatusKilled || res.ExitCode != 137 {
		t.Errorf("result = %+v", res)
	}
}

func TestTimeout(t *testing.T) {
	e := New(Limits{KillGrace: 200 * time.Millisecond}, EnvPolicy{})
	r := mustStart(t, e, Request{Script: testScript(t, "sleep.sh"), Timeout: 300 * time.Millisecond})
	if res := waitDone(t, r); res.Status != StatusTimedOut {
		t.Errorf("status = %s, want %s", res.Status, StatusTimedOut)
	}
}

func TestExitCode(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: testScript(t, "exit3.sh")})
	res := waitDone(t, r)
	if res.Status != StatusFailed || res.ExitCode != 3 || outputOf(r) != "bye\n" {
		t.Errorf("result = %+v, output %q", res, outputOf(r))
	}
	if res.Finished.Before(res.Started) {
		t.Errorf("finished %v before started %v", res.Finished, res.Started)
	}
}

func TestLeftoverChildrenTerminated(t *testing.T) {
	r := mustStart(t, New(Limits{KillGrace: 500 * time.Millisecond}, EnvPolicy{}), Request{Script: testScript(t, "leftover.sh")})
	res := waitDone(t, r)
	if res.Status != StatusFinished {
		t.Errorf("status = %s, want %s (main process exited 0)", res.Status, StatusFinished)
	}
	waitGone(t, pidFrom(t, outputOf(r), "child"))
}

func TestStartErrors(t *testing.T) {
	e := New(Limits{}, EnvPolicy{})
	if _, err := e.Start(Request{}); err == nil {
		t.Error("Start without a script succeeded")
	}
	if _, err := e.Start(Request{Script: testScript(t, "does_not_exist.sh")}); err == nil {
		t.Error("Start of a missing script succeeded")
	}
	// A failed start must release its concurrency slot.
	e = New(Limits{MaxConcurrent: 1}, EnvPolicy{})
	_, _ = e.Start(Request{Script: testScript(t, "does_not_exist.sh")})
	r := mustStart(t, e, Request{Script: testScript(t, "exit3.sh")})
	waitDone(t, r)
}

func TestOutputLimit(t *testing.T) {
	const limit = 10 << 10
	r := mustStart(t, New(Limits{MaxOutputBytes: limit}, EnvPolicy{}), Request{Script: testScript(t, "flood.sh")})
	res := waitDone(t, r)
	out := outputOf(r)
	if res.Status != StatusFinished || !res.OutputTruncated {
		t.Errorf("result = %+v", res)
	}
	if len(out) != limit+len(TruncatedNotice) || !strings.HasSuffix(out, TruncatedNotice) {
		t.Errorf("stored %d bytes, want %d ending with the notice", len(out), limit+len(TruncatedNotice))
	}
}

func TestSecureValuesMasked(t *testing.T) {
	s := testScript(t, "args.sh", &params.Parameter{Name: "token", Secure: true})
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: s, Values: map[string]params.Value{
		"token": params.Scalar("s3cr3t-value"),
	}})
	waitDone(t, r)
	out := outputOf(r)
	if strings.Contains(out, "s3cr3t") {
		t.Fatalf("secret leaked into output:\n%s", out)
	}
	if !strings.Contains(out, "[******]") || !strings.Contains(out, "env:PARAM_TOKEN=******") {
		t.Errorf("masked values missing:\n%s", out)
	}
	if !slices.Equal(r.MaskedArgs, []string{params.Mask}) {
		t.Errorf("MaskedArgs = %q", r.MaskedArgs)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	e := New(Limits{MaxConcurrent: 1}, EnvPolicy{})
	first := mustStart(t, e, Request{Script: testScript(t, "sleep.sh")})
	if _, err := e.Start(Request{Script: testScript(t, "exit3.sh")}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start = %v, want ErrBusy", err)
	}
	first.Kill()
	waitDone(t, first)

	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := e.Start(Request{Script: testScript(t, "exit3.sh")})
		if err == nil {
			waitDone(t, r)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not released: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConcurrentReaders(t *testing.T) {
	r := mustStart(t, New(Limits{}, EnvPolicy{}), Request{Script: testScript(t, "flood.sh")})
	results := make(chan string, 4)
	for range 4 {
		go func() {
			var got []byte
			offset := 0
			for {
				changed := r.Changed()
				data, next, done := r.Output(offset)
				got = append(got, data...)
				offset = next
				if done {
					results <- string(got)
					return
				}
				<-changed
			}
		}()
	}
	waitDone(t, r)
	want := outputOf(r)
	for range 4 {
		select {
		case got := <-results:
			if got != want {
				t.Errorf("reader got %d bytes, want %d", len(got), len(want))
			}
		case <-time.After(testWait):
			t.Fatal("reader did not finish")
		}
	}
}

func TestGetForgetAndShutdown(t *testing.T) {
	e := New(Limits{KillGrace: 200 * time.Millisecond}, EnvPolicy{})
	a := mustStart(t, e, Request{Script: testScript(t, "sleep.sh")})
	b := mustStart(t, e, Request{Script: testScript(t, "ignore_term.sh")})
	if e.Get(a.ID) != a || e.Get(b.ID) != b || a.ID == b.ID {
		t.Fatal("runs not registered under unique IDs")
	}
	e.Forget(a.ID) // still running: kept
	if e.Get(a.ID) == nil {
		t.Error("running run forgotten")
	}

	waitOutput(t, b, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Run{a, b} {
		if res := r.Result(); res.Status != StatusStopped {
			t.Errorf("%s: status = %s, want stopped", r.ScriptName, res.Status)
		}
	}
	e.Forget(a.ID)
	if e.Get(a.ID) != nil {
		t.Error("finished run not forgotten")
	}
}

// TestExampleRunners runs the example scripts end to end with values from the example configs.
func TestExampleRunners(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := config.LoadDir(filepath.Join(root, "examples", "runners"), config.Options{BaseDir: root})
	if err != nil || len(cat.Errors) > 0 {
		t.Fatalf("load examples: %v %v", err, cat.Errors)
	}
	e := New(Limits{}, EnvPolicy{})

	backup := cat.Script("Backup Database")
	values, err := backup.Values(map[string]any{"Database": "orders", "Encryption key": "s3cr3t", "Storage token": "tok3n"})
	if err != nil {
		t.Fatal(err)
	}
	r := mustStart(t, e, Request{Script: backup, Values: values})
	waitDone(t, r)
	out := outputOf(r)
	for _, want := range []string{"[--database]\n  [orders]", "[--key]\n  [******]", "STORAGE_TOKEN=******", "PARAM_DATABASE=orders"} {
		if !strings.Contains(out, want) {
			t.Errorf("backup output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cr3t") || strings.Contains(out, "tok3n") {
		t.Errorf("secret leaked:\n%s", out)
	}

	rotate := cat.Script("🗂️ Rotate Logs")
	values, err = rotate.Values(map[string]any{"Service": "api"})
	if err != nil {
		t.Fatal(err)
	}
	r = mustStart(t, e, Request{Script: rotate, Values: values})
	waitDone(t, r)
	if out := outputOf(r); !strings.Contains(out, "Stdin:\n  14") {
		t.Errorf("stdin value not received:\n%s", out)
	}
}
