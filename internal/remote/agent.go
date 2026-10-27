package remote

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/kube"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

const (
	minBackoff   = time.Second
	maxBackoff   = 30 * time.Second
	outputChunk  = 64 << 10
	stableAfter  = time.Minute // a connection that lasted this long resets the backoff
	defaultLimit = 256 << 20   // a start message carries the run's uploaded files
)

// Agent is the executor side: it keeps one connection to the server and runs what arrives on
// it with Exec, for one environment.
type Agent struct {
	URL         string // server base URL, e.g. http://runwarden:8080
	Token       string
	Environment string
	Pod         string
	Version     string
	Exec        *executor.Executor
	Capacity    int // concurrent runs Exec accepts, reported to the server
	Secrets     secrets.Store
	Kube        kube.Store
	Isolation   sandbox.Status
	UploadDir   string        // uploaded files of runs in progress go below it
	ReadLimit   int64         // largest message accepted; zero: 256 MiB
	Heartbeat   time.Duration // zero: 15 seconds
	HTTPClient  *http.Client

	connected atomic.Bool
}

// Connected reports whether the agent is registered with the server right now.
func (a *Agent) Connected() bool { return a.connected.Load() }

// Run connects and serves until ctx ends, reconnecting with backoff after every failure. Runs
// in progress when a connection drops are killed: the server has already marked them failed.
func (a *Agent) Run(ctx context.Context) error {
	backoff := minBackoff
	for {
		began := time.Now()
		err := a.session(ctx)
		a.connected.Store(false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(began) > stableAfter {
			backoff = minBackoff
		}
		wait := backoff/2 + rand.N(backoff/2+1) //nolint:gosec // jitter, not security
		logger().Warn("executor: connection to the server ended, reconnecting", "err", err, "in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (a *Agent) hello() Hello {
	h := Hello{Protocol: Protocol, Version: a.Version, Pod: a.Pod, Capacity: a.Capacity, Isolation: a.Isolation}
	var err error
	if h.Secrets, err = a.Secrets.Describe(a.Environment, secrets.Common); err != nil {
		logger().Warn("executor: list secrets", "err", err)
	}
	if a.Kube.Has(a.Environment) {
		inv, err := a.Kube.Inventory()
		if err != nil {
			logger().Warn("executor: list kubeconfigs", "err", err)
		}
		for _, st := range inv {
			if st.Environment == a.Environment {
				h.Cluster = &st
			}
		}
	}
	return h
}

// session is one connection: register, then serve messages until it breaks.
func (a *Agent) session(ctx context.Context) error {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+a.Token)
	header.Set(EnvironmentHeader, a.Environment)
	dctx, cancel := context.WithTimeout(ctx, helloTimeout)
	ws, resp, err := websocket.Dial(dctx, strings.TrimSuffix(a.URL, "/")+Path, &websocket.DialOptions{HTTPHeader: header, HTTPClient: a.HTTPClient})
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errors.New("the server refused the executor token for " + a.Environment)
		}
		return err
	}
	defer func() { _ = ws.CloseNow() }()
	limit := a.ReadLimit
	if limit <= 0 {
		limit = defaultLimit
	}
	ws.SetReadLimit(limit)

	s := &session{a: a, ws: ws, runs: map[string]*executor.Run{}}
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	defer s.killAll()
	hello := a.hello()
	if err := s.send(sctx, Message{Type: typeHello, Hello: &hello}); err != nil {
		return err
	}
	hctx, hcancel := context.WithTimeout(sctx, helloTimeout)
	var m Message
	err = wsjson.Read(hctx, ws, &m)
	hcancel()
	if err != nil {
		return fmt.Errorf("registration: %w", err)
	}
	if m.Type != typeWelcome {
		return fmt.Errorf("registration: unexpected %q", m.Type)
	}
	a.connected.Store(true)
	logger().Info("executor: registered with the server", "environment", a.Environment, "pod", a.Pod)
	go s.heartbeat(sctx)
	for {
		var m Message
		if err := wsjson.Read(sctx, ws, &m); err != nil {
			return err
		}
		s.handle(sctx, m)
	}
}

// session is the executor's state for one connection.
type session struct {
	a  *Agent
	ws *websocket.Conn

	mu   sync.Mutex
	runs map[string]*executor.Run
}

func (s *session) send(ctx context.Context, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, s.ws, m)
}

func (s *session) heartbeat(ctx context.Context) {
	every := s.a.Heartbeat
	if every <= 0 {
		every = defaultHeartbeat
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, every)
		err := s.ws.Ping(pctx)
		cancel()
		if err != nil {
			_ = s.ws.CloseNow()
			return
		}
	}
}

func (s *session) run(id string) *executor.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

// killAll kills the runs of a connection that ended: nobody receives their output any more.
func (s *session) killAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		r.Kill()
	}
}

func (s *session) handle(ctx context.Context, m Message) {
	switch m.Type {
	case typeStart:
		if m.Start == nil {
			return
		}
		s.start(ctx, m.ID, *m.Start)
	case typeCapture:
		if m.Capture != nil {
			go s.capture(ctx, m.ID, *m.Capture)
		}
	case typeInput, typeEOF, typeStop, typeKill, typeResize:
		r := s.run(m.ID)
		if r == nil {
			return
		}
		switch m.Type {
		case typeInput:
			_ = r.WriteInput(string(m.Data), m.Secret)
		case typeEOF:
			_ = r.CloseInput()
		case typeStop:
			r.Stop()
		case typeKill:
			r.Kill()
		case typeResize:
			_ = r.Resize(m.Rows, m.Cols)
		}
	default:
		logger().Warn("executor: unknown message from the server", "type", m.Type)
	}
}

// start runs a job: the environment's secrets and kubeconfig are added here, uploaded files
// are written to this pod's disk. Its output is streamed until it ends.
func (s *session) start(ctx context.Context, id string, st Start) {
	fail := func(err error) {
		msg := err.Error()
		if errors.Is(err, executor.ErrBusy) || errors.Is(err, executor.ErrUserBusy) {
			msg = errBusy
		}
		logger().Warn("executor: run not started", "run", id, "script", st.Job.Script, "err", err)
		_ = s.send(ctx, Message{Type: typeFailed, ID: id, Error: msg})
	}
	job := st.Job
	if !validID.MatchString(id) || job.ID != id || s.run(id) != nil {
		fail(fmt.Errorf("invalid run ID %q", id))
		return
	}
	a := s.a
	env := []string{a.Environment}
	if len(st.Secrets) > 0 {
		res, err := a.Secrets.Resolve(st.Secrets, env, nil)
		if err != nil {
			fail(err)
			return
		}
		job.Secrets = res.Env
	}
	var started Started
	if st.Kubernetes {
		res, err := a.Kube.Resolve(env, nil)
		if err != nil {
			fail(err)
			return
		}
		job.Kube = &executor.Kube{Config: res.Kubeconfig, Env: res.Env}
		started.Clusters = res.Clusters
	}
	uploads, err := a.writeFiles(id, st.Files, &job)
	if err != nil {
		fail(err)
		return
	}
	cleanup := func() {
		if uploads != "" {
			_ = os.RemoveAll(uploads)
		}
	}
	r, err := a.Exec.StartJob(job)
	if err != nil {
		cleanup()
		fail(err)
		return
	}
	s.mu.Lock()
	s.runs[id] = r
	s.mu.Unlock()
	// Sent before any output: the server knows the run once this arrives.
	_ = s.send(ctx, Message{Type: typeStarted, ID: id, Started: &started})
	go func() {
		s.stream(ctx, r)
		<-r.Done()
		s.mu.Lock()
		delete(s.runs, id)
		s.mu.Unlock()
		a.Exec.Forget(id)
		cleanup()
	}()
}

// stream sends r's output in chunks, then its result.
func (s *session) stream(ctx context.Context, r *executor.Run) {
	offset := 0
	for {
		changed := r.Changed()
		data, next, done := r.Output(offset)
		offset = next
		for len(data) > 0 {
			n := min(len(data), outputChunk)
			if err := s.send(ctx, Message{Type: typeOutput, ID: r.ID, Data: data[:n]}); err != nil {
				return
			}
			data = data[n:]
		}
		if done {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return
		}
	}
	res := r.Result()
	_ = s.send(ctx, Message{Type: typeExit, ID: r.ID, Exit: &Exit{Status: res.Status, ExitCode: res.ExitCode, Finished: res.Finished, Truncated: res.OutputTruncated}})
}

// writeFiles stores a run's uploaded files below UploadDir/<id> and points the job's
// arguments, variables and stdin values at them instead of the server's paths.
func (a *Agent) writeFiles(id string, files []File, job *executor.Job) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	if a.UploadDir == "" {
		return "", errors.New("this executor accepts no uploaded files (no upload directory)")
	}
	dir := filepath.Join(a.UploadDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var pairs []string
	for i, f := range files {
		if f.Path == "" {
			continue
		}
		local := filepath.Join(dir, strconv.Itoa(i)+"_"+safeName(filepath.Base(f.Path)))
		if err := os.WriteFile(local, f.Data, 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		pairs = append(pairs, f.Path, local)
	}
	rep := strings.NewReplacer(pairs...)
	for i := 1; i < len(job.Command); i++ {
		job.Command[i] = rep.Replace(job.Command[i])
	}
	for k, v := range job.Env {
		job.Env[k] = rep.Replace(v)
	}
	for i := range job.Stdin {
		job.Stdin[i].Value = rep.Replace(job.Stdin[i].Value)
	}
	job.Uploads = dir
	return dir, nil
}

// safeName keeps a file name to letters, digits, dot, dash and underscore.
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
	if strings.Trim(name, ".") == "" {
		return "file"
	}
	return name
}

// capture runs a dropdown helper with the environment's secrets and kubeconfig.
func (s *session) capture(ctx context.Context, id string, c Capture) {
	reply := func(data []byte, err error) {
		m := Message{Type: typeCaptured, ID: id, Data: data}
		if err != nil {
			m = Message{Type: typeFailed, ID: id, Error: err.Error()}
		}
		_ = s.send(ctx, m)
	}
	if len(c.Command) == 0 {
		reply(nil, errors.New("empty command"))
		return
	}
	a := s.a
	env := []string{a.Environment}
	var values map[string]string
	if len(c.Secrets) > 0 {
		res, err := a.Secrets.Resolve(c.Secrets, env, nil)
		if err != nil {
			reply(nil, err)
			return
		}
		values = res.Env
	}
	var kc *executor.Kube
	if c.Kubernetes {
		res, err := a.Kube.Resolve(env, nil)
		if err != nil {
			reply(nil, err)
			return
		}
		kc = &executor.Kube{Config: res.Kubeconfig, Env: res.Env}
	}
	reply(a.Exec.Capture(ctx, c.Command, c.Dir, values, kc))
}
