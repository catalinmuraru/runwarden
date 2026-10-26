// Package executor runs scripts: one process group per run, an allowlisted environment, live
// output with secure values masked, stdin, stop/kill and limits. Scripts are executed
// directly, never through a shell.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/params"
)

// Limits bound what runs may consume. Zero fields take the defaults.
type Limits struct {
	MaxConcurrent  int           // default 10
	Timeout        time.Duration // default 60 minutes
	KillGrace      time.Duration // default 10 seconds between SIGTERM and SIGKILL
	MaxOutputBytes int64         // default 10 MiB stored per run
}

func (l Limits) withDefaults() Limits {
	if l.MaxConcurrent <= 0 {
		l.MaxConcurrent = 10
	}
	if l.Timeout <= 0 {
		l.Timeout = 60 * time.Minute
	}
	if l.KillGrace <= 0 {
		l.KillGrace = 10 * time.Second
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = 10 << 20
	}
	return l
}

// Request describes a run to start.
type Request struct {
	Script  *config.Script
	Values  map[string]params.Value // validated values, see config.Script.Values
	Timeout time.Duration           // zero means Limits.Timeout
	User    string                  // signed-in user, exported to the script as RUNWARDEN_USER
}

// ErrBusy is returned when the concurrent run limit is reached.
var ErrBusy = errors.New("too many runs in progress")

// Executor starts runs and keeps track of them.
type Executor struct {
	limits Limits
	env    EnvPolicy
	sem    chan struct{}

	mu   sync.Mutex
	runs map[string]*Run
	seq  uint64
}

// New returns an Executor with the given limits and environment policy.
func New(limits Limits, env EnvPolicy) *Executor {
	limits = limits.withDefaults()
	return &Executor{
		limits: limits,
		env:    env.withDefaults(),
		sem:    make(chan struct{}, limits.MaxConcurrent),
		runs:   map[string]*Run{},
	}
}

func logger() *slog.Logger { return slog.Default().With("component", "executor") }

// Timeout returns the timeout a run gets when its script asks for d (zero: the default).
func (e *Executor) Timeout(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return e.limits.Timeout
}

// Start launches a run. It returns ErrBusy when too many runs are in progress.
func (e *Executor) Start(req Request) (*Run, error) {
	if req.Script == nil || len(req.Script.Command) == 0 {
		return nil, errors.New("executor: no script to run")
	}
	select {
	case e.sem <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	r, err := e.start(req)
	if err != nil {
		<-e.sem
		return nil, err
	}
	go func() {
		<-r.done
		<-e.sem
		res := r.Result()
		logger().Info("run finished", "run", r.ID, "script", r.ScriptName,
			"status", res.Status, "exit_code", res.ExitCode, "duration", res.Finished.Sub(res.Started).Round(time.Millisecond))
	}()
	return r, nil
}

func (e *Executor) start(req Request) (*Run, error) {
	id := e.nextID()
	s := req.Script
	inv := params.Build(s.Parameters, req.Values, id)
	terminal := s.RequiresTerminal && ptySupported
	fixed := map[string]string{}
	if req.User != "" {
		fixed["RUNWARDEN_USER"] = req.User
	}
	if terminal {
		fixed["TERM"] = "xterm-256color"
	}
	tmpDir := ""
	if e.env.TempRoot != "" {
		// A private TMPDIR per run, so concurrent runs don't collide on temporary files.
		tmpDir = filepath.Join(e.env.TempRoot, id)
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			return nil, fmt.Errorf("run temp dir: %w", err)
		}
		fixed["TMPDIR"] = tmpDir
	}
	fail := func(err error) (*Run, error) {
		if tmpDir != "" {
			_ = os.RemoveAll(tmpDir)
		}
		return nil, err
	}
	env, refused := e.env.build(inv.Env, fixed)
	for _, name := range refused {
		logger().Warn("env variable refused for parameter", "script", s.Name, "variable", name)
	}

	args := append(append([]string(nil), s.Command[1:]...), inv.Args...)
	// No context: a canceled context would kill only the main process, while runs end through
	// Stop/Kill/timeout, which signal the whole process group.
	cmd := exec.Command(s.Command[0], args...) //nolint:gosec,noctx // argv from a validated config, no shell
	cmd.Dir = s.WorkingDirectory
	cmd.Env = env

	// outR is what we read output from; child is the side the child gets and we close after start.
	var (
		outR, child *os.File
		stdin       io.WriteCloser
		err         error
	)
	if terminal {
		// One terminal for stdin, stdout and stderr, as when a user runs the script by hand.
		var tty *os.File
		outR, tty, err = openPTY()
		if err != nil {
			return fail(err)
		}
		preparePTY(cmd)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
		stdin, child = outR, tty
	} else {
		if err := prepare(cmd); err != nil {
			return fail(err)
		}
		var outW *os.File
		if outR, outW, err = os.Pipe(); err != nil {
			return fail(err)
		}
		cmd.Stdout = outW
		cmd.Stderr = outW // one pipe keeps stdout and stderr in order
		if stdin, err = cmd.StdinPipe(); err != nil {
			_ = outR.Close()
			_ = outW.Close()
			return fail(err)
		}
		child = outW
	}

	timeout := e.Timeout(req.Timeout)
	m := newMasker(inv.SecureValues, s.RedactPatterns)
	r := &Run{
		ID:         id,
		ScriptName: s.Name,
		MaskedArgs: append(append([]string(nil), s.Command[1:]...), inv.MaskedArgs...),
		Terminal:   terminal,
		grace:      e.limits.KillGrace,
		out:        newOutput(e.limits.MaxOutputBytes),
		done:       make(chan struct{}),
		mask:       m,
		tmpDir:     tmpDir,
		stdin:      stdin,
		pending:    inv.Stdin,
		result:     Result{Status: StatusRunning},
	}

	if err := cmd.Start(); err != nil {
		_ = outR.Close()
		_ = child.Close()
		return fail(fmt.Errorf("start %s: %w", s.Name, err))
	}
	_ = child.Close() // the child holds its own copy
	r.pid = cmd.Process.Pid
	r.Started = time.Now()
	r.result.Started = r.Started
	logger().Info("run started", "run", id, "script", s.Name, "args", r.MaskedArgs, "timeout", timeout, "terminal", terminal)

	pumpDone := make(chan struct{})
	r.answerPrompts(nil) // stdin values that don't wait for a prompt
	go r.pump(outR, m, pumpDone)
	timer := time.AfterFunc(timeout, func() { r.stop(StatusTimedOut) })
	go r.supervise(cmd, outR, pumpDone, m, timer)

	e.mu.Lock()
	e.runs[id] = r
	e.mu.Unlock()
	return r, nil
}

func (e *Executor) nextID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	// Sortable by start time; milliseconds keep IDs unique across restarts.
	return fmt.Sprintf("%s-%04d", time.Now().UTC().Format("20060102-150405.000"), e.seq%10000)
}

// Get returns a known run, or nil.
func (e *Executor) Get(id string) *Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs[id]
}

// Running returns the IDs of runs still in progress.
func (e *Executor) Running() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for id, r := range e.runs {
		if r.Result().Status == StatusRunning {
			ids = append(ids, id)
		}
	}
	return ids
}

// Forget drops a finished run from memory. Running runs are kept.
func (e *Executor) Forget(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.runs[id]; ok && r.Result().Status != StatusRunning {
		delete(e.runs, id)
	}
}

// Shutdown stops all running scripts and waits for them, killing whatever is left when ctx
// ends.
func (e *Executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	var running []*Run
	for _, r := range e.runs {
		if r.Result().Status == StatusRunning {
			running = append(running, r)
		}
	}
	e.mu.Unlock()

	for _, r := range running {
		r.Stop()
	}
	for _, r := range running {
		select {
		case <-r.done:
		case <-ctx.Done():
			for _, r := range running {
				r.Kill()
			}
			return ctx.Err()
		}
	}
	return nil
}
