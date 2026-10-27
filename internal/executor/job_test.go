package executor

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/params"
)

func TestJobFromRequest(t *testing.T) {
	s := testScript(t, "args.sh",
		&params.Parameter{Name: "Name", Type: params.TypeText, Param: "--name"},
		&params.Parameter{Name: "Pass", Type: params.TypeText, Secure: true, Param: "--pass"})
	s.RedactPatterns = []*regexp.Regexp{regexp.MustCompile(`tok-[0-9]+`)}
	e := New(Limits{}, EnvPolicy{})
	job := e.Job(Request{Script: s, User: "alice", Timeout: time.Minute, Values: map[string]params.Value{
		"Name": params.Scalar("x"), "Pass": params.Scalar("hunter2"),
	}})
	if job.ID == "" || job.User != "alice" || job.Timeout != time.Minute || job.Dir != s.WorkingDirectory {
		t.Errorf("job = %+v", job)
	}
	if want := append(slices.Clone(s.Command), "--name", "x", "--pass", "hunter2"); !slices.Equal(job.Command, want) {
		t.Errorf("command %q, want %q", job.Command, want)
	}
	if !slices.Contains(job.MaskedArgs, params.Mask) || slices.Contains(job.MaskedArgs, "hunter2") {
		t.Errorf("masked args %q", job.MaskedArgs)
	}
	if !slices.Equal(job.Secure, []string{"hunter2"}) || !slices.Equal(job.Redact, []string{`tok-[0-9]+`}) {
		t.Errorf("secure %q, redact %q", job.Secure, job.Redact)
	}
	if job.Env["EXECUTION_ID"] != job.ID {
		t.Errorf("EXECUTION_ID = %q, want the run ID %q", job.Env["EXECUTION_ID"], job.ID)
	}
	r, err := e.StartJob(job)
	if err != nil {
		t.Fatal(err)
	}
	if waitDone(t, r); r.ID != job.ID || strings.Contains(outputOf(r), "hunter2") {
		t.Errorf("run %s, output:\n%s", r.ID, outputOf(r))
	}
}

// A job carries the server's default timeout, so an executor with other limits still applies it.
func TestJobResolvesTimeout(t *testing.T) {
	s := testScript(t, "args.sh")
	e := New(Limits{Timeout: 4 * time.Hour}, EnvPolicy{})
	if job := e.Job(Request{Script: s, User: "alice"}); job.Timeout != 4*time.Hour {
		t.Errorf("timeout %v, want the server default 4h", job.Timeout)
	}
}

func TestStartJobErrors(t *testing.T) {
	e := New(Limits{}, EnvPolicy{})
	if _, err := e.StartJob(Job{}); err == nil {
		t.Error("empty command accepted")
	}
	if _, err := e.StartJob(Job{Command: []string{"/bin/true"}, Redact: []string{"("}}); err == nil {
		t.Error("invalid redact pattern accepted")
	}
	if len(e.Running()) != 0 || len(e.byUser) != 0 {
		t.Error("failed starts left state behind")
	}
}

// fakeControl records what a remote run asks of its executor.
type fakeControl struct{ calls []string }

func (c *fakeControl) Input(line string, _ bool) error {
	c.calls = append(c.calls, "input:"+line)
	return nil
}
func (c *fakeControl) CloseInput() error { c.calls = append(c.calls, "eof"); return nil }
func (c *fakeControl) Stop()             { c.calls = append(c.calls, "stop") }
func (c *fakeControl) Kill()             { c.calls = append(c.calls, "kill") }
func (c *fakeControl) Resize(_, _ int) error {
	c.calls = append(c.calls, "resize")
	return nil
}

func TestRemoteRun(t *testing.T) {
	ctl := &fakeControl{}
	r, feed, err := NewRemote(Job{ID: "r1", Script: "s", User: "bob", Secure: []string{"s3cret"}, Redact: []string{`tok-\d+`}}, 64, ctl)
	if err != nil {
		t.Fatal(err)
	}
	feed.Write([]byte("pw=s3cret tok-42\n"))
	_ = r.WriteInput("typed", true)
	feed.Write([]byte("echo: typed\n"))
	_ = r.CloseInput()
	_ = r.Resize(10, 20)
	r.Stop()
	r.Kill()
	if want := []string{"input:typed", "eof", "resize", "stop", "kill"}; !slices.Equal(ctl.calls, want) {
		t.Errorf("control calls %q, want %q", ctl.calls, want)
	}
	if out := outputOf(r); strings.Contains(out, "s3cret") || strings.Contains(out, "tok-42") || strings.Contains(out, "typed") {
		t.Errorf("unmasked output %q", out)
	}
	feed.Write([]byte(strings.Repeat("x", 100)))
	feed.End(Result{Status: StatusKilled, ExitCode: -1})
	feed.End(Result{Status: StatusFinished}) // ignored
	res := r.Wait()
	if res.Status != StatusKilled || !res.OutputTruncated || res.Started != r.Started {
		t.Errorf("result %+v", res)
	}
	r.Kill() // finished: nothing is sent
	if len(ctl.calls) != 5 {
		t.Errorf("control called after the end: %q", ctl.calls)
	}
}

func TestAdopt(t *testing.T) {
	e := New(Limits{MaxPerUser: 1}, EnvPolicy{})
	var feed *Feed
	start := func(id string) func() (*Run, error) {
		return func() (*Run, error) {
			r, f, err := NewRemote(Job{ID: id, Script: "s", User: "Alice"}, 0, &fakeControl{})
			feed = f
			return r, err
		}
	}
	r, err := e.Adopt("Alice", start("r1"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Get("r1") != r || !slices.Equal(e.Running(), []string{"r1"}) || !slices.Equal(e.RunningFor("alice"), []string{"r1"}) {
		t.Errorf("adopted run not tracked: %v", e.Running())
	}
	if _, err := e.Adopt("alice", start("r2")); !errors.Is(err, ErrUserBusy) {
		t.Errorf("second run of alice = %v, want ErrUserBusy", err)
	}
	if _, err := e.Adopt("bob", func() (*Run, error) { return nil, ErrBusy }); !errors.Is(err, ErrBusy) {
		t.Errorf("failed start = %v", err)
	}
	feed.End(Result{Status: StatusFinished})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := e.Adopt("alice", start("r3")); err == nil {
			break // the slot was given back when r1 ended
		}
		if time.Now().After(deadline) {
			t.Fatal("alice's slot was not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Forget("r1")
	if e.Get("r1") != nil {
		t.Error("finished run not forgotten")
	}
}
