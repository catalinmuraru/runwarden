package executor

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// Status is the state of a run.
type Status string

// Run statuses. Every status except StatusRunning is final.
const (
	StatusRunning  Status = "running"
	StatusFinished Status = "finished"  // exit code 0
	StatusFailed   Status = "failed"    // non-zero exit code
	StatusStopped  Status = "stopped"   // stopped by a user
	StatusKilled   Status = "killed"    // killed by a user
	StatusTimedOut Status = "timed_out" // exceeded its time limit
)

// TruncatedNotice is appended once when a run's output exceeds the storage limit.
const TruncatedNotice = "\n[output truncated: limit reached]\n"

// ErrInputClosed is returned when writing to a run that no longer accepts input.
var ErrInputClosed = errors.New("input is closed")

// Result is the outcome of a finished run.
type Result struct {
	Status          Status
	ExitCode        int
	Started         time.Time
	Finished        time.Time
	OutputTruncated bool
}

// Run is one execution of a script.
type Run struct {
	ID         string
	ScriptName string
	Started    time.Time
	MaskedArgs []string // arguments with secure values masked, for display and audit
	Terminal   bool     // runs on a pseudo-terminal: input is echoed by the terminal

	pid    int
	grace  time.Duration
	out    *output
	done   chan struct{}
	mask   *masker
	tmpDir string // per-run TMPDIR, removed when the run ends

	stdinMu sync.Mutex
	stdin   io.WriteCloser // nil once closed
	pending []params.StdinInput
	window  []byte // recent raw output, for matching ExpectText

	mu       sync.Mutex
	reason   Status // why we signalled the process: stopped, killed or timed_out
	result   Result
	stopOnce sync.Once
}

// Done is closed when the run has finished and all output is stored.
func (r *Run) Done() <-chan struct{} { return r.done }

// Wait blocks until the run finishes and returns its result.
func (r *Run) Wait() Result {
	<-r.done
	return r.Result()
}

// Result returns the current result; Status is StatusRunning until the run finishes.
func (r *Run) Result() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

// Output returns the stored output from offset on, the offset to continue from, and whether
// the run has finished (no more output will follow).
func (r *Run) Output(offset int) (data []byte, next int, done bool) {
	return r.out.read(offset)
}

// Changed returns a channel that is closed when new output arrives or the run finishes.
// Fetch a fresh channel after each notification.
func (r *Run) Changed() <-chan struct{} { return r.out.notify() }

// WriteInput sends one line of input to the script. Secret input is masked wherever it would
// appear in the output, e.g. when a terminal echoes it.
func (r *Run) WriteInput(line string, secret bool) error {
	if secret && r.mask != nil {
		r.mask.addSecret(line)
	}
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	return r.writeLocked(line)
}

// CloseInput signals end of input: it closes the script's stdin, or on a terminal sends the
// end-of-file character (Ctrl-D), as a user at a terminal would.
func (r *Run) CloseInput() error {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if r.stdin == nil {
		return ErrInputClosed
	}
	if r.Terminal {
		if _, err := io.WriteString(r.stdin, "\x04"); err != nil {
			return ErrInputClosed
		}
		return nil
	}
	err := r.stdin.Close()
	r.stdin = nil
	return err
}

func (r *Run) writeLocked(line string) error {
	if r.stdin == nil {
		return ErrInputClosed
	}
	_, err := io.WriteString(r.stdin, line+"\n")
	if err != nil {
		return ErrInputClosed
	}
	return nil
}

// Stop asks the script to terminate (SIGTERM to its process group) and kills it if it is
// still running after the grace period.
func (r *Run) Stop() { r.stop(StatusStopped) }

// Kill kills the script and all of its children immediately.
func (r *Run) Kill() {
	r.setReason(StatusKilled, true)
	killGroup(r.pid)
}

func (r *Run) stop(reason Status) {
	if !r.setReason(reason, false) {
		return
	}
	terminateGroup(r.pid)
	r.stopOnce.Do(func() {
		go func() {
			select {
			case <-r.done:
			case <-time.After(r.grace):
				killGroup(r.pid)
			}
		}()
	})
}

// setReason records why the run is being ended. A kill overrides an earlier stop.
// It returns false if the run already finished.
func (r *Run) setReason(reason Status, override bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.Status != StatusRunning {
		return false
	}
	if r.reason == "" || override {
		r.reason = reason
	}
	return true
}

// handle stores a chunk of raw output and answers stdin prompts it completes.
func (r *Run) handle(raw []byte, m *masker) {
	r.out.append(m.write(raw))
	r.answerPrompts(raw)
}

// answerPrompts writes queued stdin values: leading values without ExpectText at once, then
// each value with ExpectText once its text has appeared in the output.
func (r *Run) answerPrompts(raw []byte) {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if len(r.pending) == 0 {
		return
	}
	r.window = append(r.window, raw...)
	for len(r.pending) > 0 {
		in := r.pending[0]
		if in.ExpectText != "" {
			i := bytes.Index(r.window, []byte(in.ExpectText))
			if i < 0 {
				break
			}
			r.window = r.window[i+len(in.ExpectText):]
		}
		if err := r.writeLocked(in.Value); err != nil {
			r.pending = nil
			break
		}
		r.pending = r.pending[1:]
	}
	if len(r.pending) == 0 {
		r.window = nil
		return
	}
	// Keep only what a future match could still use.
	if keep := len(r.pending[0].ExpectText) - 1; len(r.window) > keep {
		r.window = append([]byte(nil), r.window[len(r.window)-keep:]...)
	}
}

// idleFlush is how long output may be quiet before held-back bytes (kept for masking) are
// released, so prompts without a newline still reach the user.
const idleFlush = 200 * time.Millisecond

// pump reads the script's output until the stream closes.
func (r *Run) pump(outR *os.File, m *masker, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 32<<10)
	for {
		if m.pendingLen() > 0 {
			_ = outR.SetReadDeadline(time.Now().Add(idleFlush))
		} else {
			_ = outR.SetReadDeadline(time.Time{})
		}
		n, err := outR.Read(buf)
		if n > 0 {
			r.handle(buf[:n], m)
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			r.out.append(m.flush())
			continue
		}
		if err != nil {
			return // EOF, or EIO on a terminal once every process holding it has exited
		}
	}
}

// supervise waits for the process, cleans up leftover children, drains output and records
// the result.
func (r *Run) supervise(cmd *exec.Cmd, outR *os.File, pumpDone <-chan struct{}, m *masker, timer *time.Timer) {
	waitErr := cmd.Wait()
	timer.Stop()

	// The run ends with its main process. Background children that are still alive (and keep
	// the output pipe open) are terminated, then killed.
	terminateGroup(r.pid)
	select {
	case <-pumpDone:
	case <-time.After(r.grace):
		killGroup(r.pid)
		select {
		case <-pumpDone:
		case <-time.After(time.Second):
			_ = outR.Close()
			<-pumpDone
		}
	}
	r.out.append(m.flush())
	_ = outR.Close() // for a terminal this is the master side, which also carried stdin

	r.stdinMu.Lock()
	r.stdin = nil // exec closes a stdin pipe after Wait
	r.pending = nil
	r.stdinMu.Unlock()
	if r.tmpDir != "" {
		_ = os.RemoveAll(r.tmpDir)
	}

	r.mu.Lock()
	code := -1
	if cmd.ProcessState != nil {
		code = exitCode(cmd.ProcessState)
	}
	status := r.reason
	switch {
	case status != "":
	case waitErr == nil && code == 0:
		status = StatusFinished
	default:
		status = StatusFailed
	}
	r.result = Result{
		Status:          status,
		ExitCode:        code,
		Started:         r.Started,
		Finished:        time.Now(),
		OutputTruncated: r.out.wasTruncated(),
	}
	r.mu.Unlock()

	r.out.finish()
	close(r.done)
}

// output is a run's stored output, read by any number of readers at their own pace.
type output struct {
	mu        sync.Mutex
	buf       []byte
	max       int64
	truncated bool
	finished  bool
	changed   chan struct{}
}

func newOutput(maxBytes int64) *output {
	return &output{max: maxBytes, changed: make(chan struct{})}
}

func (o *output) append(p []byte) {
	if len(p) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.truncated {
		return
	}
	if room := o.max - int64(len(o.buf)); int64(len(p)) > room {
		o.buf = append(o.buf, p[:max(room, 0)]...)
		o.buf = append(o.buf, TruncatedNotice...)
		o.truncated = true
	} else {
		o.buf = append(o.buf, p...)
	}
	o.signal()
}

func (o *output) finish() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = true
	o.signal()
}

// signal wakes all waiting readers. Callers hold o.mu.
func (o *output) signal() {
	close(o.changed)
	o.changed = make(chan struct{})
}

func (o *output) notify() <-chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return o.changed
}

func (o *output) read(offset int) ([]byte, int, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	offset = min(max(offset, 0), len(o.buf))
	return bytes.Clone(o.buf[offset:]), len(o.buf), o.finished
}

func (o *output) wasTruncated() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.truncated
}
