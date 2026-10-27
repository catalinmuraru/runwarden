package executor

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// Job is a run with its parameters already turned into argv, environment and stdin, so it can
// be started here or sent to an executor in another pod.
type Job struct {
	ID         string              `json:"id"`     // empty: StartJob assigns one
	Script     string              `json:"script"` // script name, for logs
	User       string              `json:"user"`
	Command    []string            `json:"command"`     // program and arguments
	MaskedArgs []string            `json:"masked_args"` // arguments after the program, secure values masked, for display and audit
	Env        map[string]string   `json:"env"`
	Stdin      []params.StdinInput `json:"stdin"`
	Secure     []string            `json:"secure"`   // secure parameter values, masked in the output
	Redact     []string            `json:"redact"`   // regexes whose matches are masked in the output
	Dir        string              `json:"dir"`      // working directory
	Terminal   bool                `json:"terminal"` // run on a pseudo-terminal (where supported)
	Rows       int                 `json:"rows"`     // initial terminal size; zero: the default
	Cols       int                 `json:"cols"`
	Timeout    time.Duration       `json:"timeout"`

	// Filled where the job runs, never sent between pods.
	Secrets map[string]string `json:"-"` // exported to the script and masked in its output
	Kube    *Kube             `json:"-"`
	Uploads string            `json:"-"` // directory with the run's uploaded files, readable by a sandboxed run
}

// Job prepares req for StartJob, with a new run ID.
func (e *Executor) Job(req Request) Job {
	s := req.Script
	id := e.nextID()
	inv := params.Build(s.Parameters, req.Values, id)
	redact := make([]string, len(s.RedactPatterns))
	for i, re := range s.RedactPatterns {
		redact[i] = re.String()
	}
	return Job{
		ID:         id,
		Script:     s.Name,
		User:       req.User,
		Command:    append(slices.Clone(s.Command), inv.Args...),
		MaskedArgs: append(slices.Clone(s.Command[1:]), inv.MaskedArgs...),
		Env:        inv.Env,
		Stdin:      inv.Stdin,
		Secure:     inv.SecureValues,
		Redact:     redact,
		Dir:        s.WorkingDirectory,
		Terminal:   s.RequiresTerminal,
		Timeout:    req.Timeout,
		Secrets:    req.Secrets,
		Kube:       req.Kube,
		Uploads:    req.Uploads,
	}
}

func compileAll(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("redact pattern %q: %w", p, err)
		}
		out[i] = re
	}
	return out, nil
}

// Resize sets the size of a run's terminal. Runs without one ignore it.
func (r *Run) Resize(rows, cols int) error {
	if r.ctl != nil {
		return r.ctl.Resize(rows, cols)
	}
	if r.pty == nil || rows <= 0 || cols <= 0 {
		return nil
	}
	r.stdinMu.Lock() // the terminal is closed under this lock when the run ends
	defer r.stdinMu.Unlock()
	if r.stdin == nil {
		return ErrInputClosed
	}
	return setSize(r.pty, rows, cols)
}

// Control drives a run whose process lives elsewhere, e.g. in an executor pod.
type Control interface {
	Input(line string, secret bool) error
	CloseInput() error
	Stop()
	Kill()
	Resize(rows, cols int) error
}

// Feed delivers the output and the result of a remote run.
type Feed struct {
	r *Run
}

// NewRemote returns a run of job whose process runs elsewhere and is driven through ctl. Its
// output and result arrive through the Feed. The job's secure values are masked here too.
func NewRemote(job Job, maxOutput int64, ctl Control) (*Run, *Feed, error) {
	redact, err := compileAll(job.Redact)
	if err != nil {
		return nil, nil, err
	}
	if maxOutput <= 0 {
		maxOutput = Limits{}.withDefaults().MaxOutputBytes
	}
	now := time.Now()
	r := &Run{
		ID:         job.ID,
		ScriptName: job.Script,
		User:       job.User,
		Started:    now,
		MaskedArgs: job.MaskedArgs,
		Terminal:   job.Terminal,
		out:        newOutput(maxOutput),
		done:       make(chan struct{}),
		mask:       newMasker(job.Secure, redact),
		ctl:        ctl,
		result:     Result{Status: StatusRunning, Started: now},
	}
	return r, &Feed{r: r}, nil
}

// Write stores a chunk of output, masked. The executor already masked it with the full set of
// secrets; chunks are not held back here, so a value split across two chunks is not caught.
func (f *Feed) Write(p []byte) {
	m := f.r.mask
	f.r.out.append(append(m.write(p), m.flush()...))
}

// End records the result and closes the run. Later calls do nothing.
func (f *Feed) End(res Result) {
	r := f.r
	r.mu.Lock()
	if r.result.Status != StatusRunning {
		r.mu.Unlock()
		return
	}
	if res.Status == "" || res.Status == StatusRunning {
		res.Status = StatusFailed
	}
	res.Started = r.Started
	if res.Finished.IsZero() {
		res.Finished = time.Now()
	}
	res.OutputTruncated = res.OutputTruncated || r.out.wasTruncated()
	r.result = res
	r.mu.Unlock()
	r.out.finish()
	close(r.done)
}
