package remote

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/kube"
)

// LostNotice is appended to the output of a run whose executor connection was lost.
const LostNotice = "\n[executor connection lost: the run is marked failed]\n"

// ErrLost is returned for calls on a connection that has closed.
var ErrLost = errors.New("executor connection lost")

const (
	defaultHeartbeat = 15 * time.Second
	writeTimeout     = time.Minute
	helloTimeout     = 10 * time.Second
	serverReadLimit  = 4 << 20 // output chunks and helper output are far smaller
)

func logger() *slog.Logger { return slog.Default().With("component", "remote") }

// HubOptions configures the server side.
type HubOptions struct {
	// Tokens maps an environment to the hex SHA-256 of its executors' token (see HashToken).
	// Executors of other environments are refused.
	Tokens         map[string]string
	MaxOutputBytes int64         // stored per run; zero: the executor default
	Heartbeat      time.Duration // ping interval; zero: 15 seconds
}

// Hub accepts executor connections and keeps them per environment.
type Hub struct {
	opts   HubOptions
	tokens map[string][]byte

	mu    sync.Mutex
	conns map[string][]*Conn
}

// NewHub returns a Hub; it rejects malformed token hashes.
func NewHub(opts HubOptions) (*Hub, error) {
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = defaultHeartbeat
	}
	h := &Hub{opts: opts, tokens: map[string][]byte{}, conns: map[string][]*Conn{}}
	for env, hash := range opts.Tokens {
		b, err := hex.DecodeString(hash)
		if err != nil || len(b) != 32 {
			return nil, fmt.Errorf("executor token of %s: not a hex SHA-256", env)
		}
		h.tokens[env] = b
	}
	return h, nil
}

// authorized checks the bearer token of env in constant time.
func (h *Hub) authorized(env, header string) bool {
	want, ok := h.tokens[env]
	token, bearer := strings.CutPrefix(header, "Bearer ")
	if !ok || !bearer || token == "" {
		return false
	}
	got, _ := hex.DecodeString(HashToken(token))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ServeHTTP accepts one executor connection and serves it until it closes.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	env := r.Header.Get(EnvironmentHeader)
	if !h.authorized(env, r.Header.Get("Authorization")) {
		logger().Warn("executor refused: bad token or unknown environment", "environment", env, "remote", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(serverReadLimit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hctx, hcancel := context.WithTimeout(ctx, helloTimeout)
	var m Message
	err = wsjson.Read(hctx, ws, &m)
	hcancel()
	switch {
	case err != nil:
		_ = ws.Close(websocket.StatusPolicyViolation, "no hello")
		return
	case m.Type != typeHello || m.Hello == nil:
		_ = ws.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	case m.Hello.Protocol != Protocol:
		logger().Warn("executor refused: protocol mismatch", "environment", env, "pod", m.Hello.Pod, "protocol", m.Hello.Protocol, "want", Protocol)
		_ = ws.Close(websocket.StatusPolicyViolation, "protocol "+strconv.Itoa(Protocol)+" required")
		return
	}
	c := &Conn{
		hub: h, ws: ws, env: env, hello: *m.Hello, since: time.Now(),
		runs: map[string]*remoteRun{}, waits: map[string]chan Message{},
	}
	if err := c.send(ctx, Message{Type: typeWelcome}); err != nil {
		return
	}
	h.add(c)
	logger().Info("executor connected", "environment", env, "pod", c.hello.Pod, "version", c.hello.Version,
		"isolation", c.hello.Isolation.Active, "abi", c.hello.Isolation.ABI)
	go c.heartbeat(ctx)
	err = c.read(ctx)
	h.remove(c)
	c.lost()
	logger().Warn("executor disconnected", "environment", env, "pod", c.hello.Pod, "err", err)
	_ = ws.CloseNow()
}

func (h *Hub) add(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c.env] = append(h.conns[c.env], c)
}

func (h *Hub) remove(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.conns[c.env]
	for i, x := range list {
		if x == c {
			h.conns[c.env] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(h.conns[c.env]) == 0 {
		delete(h.conns, c.env)
	}
}

// Pick returns the least busy executor connection of env, or nil when none is connected.
func (h *Hub) Pick(env string) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	var best *Conn
	for _, c := range h.conns[env] {
		if best == nil || c.Busy() < best.Busy() {
			best = c
		}
	}
	return best
}

// Executors describes the connected executor pods of env.
func (h *Hub) Executors(env string) []Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Info, 0, len(h.conns[env]))
	for _, c := range h.conns[env] {
		out = append(out, c.Info())
	}
	return out
}

// Info describes one connected executor pod.
type Info struct {
	Environment string
	Connected   time.Time
	Busy        int // runs and helper calls in progress
	Hello
}

// Conn is the server side of one executor pod's connection.
type Conn struct {
	hub   *Hub
	ws    *websocket.Conn
	env   string
	hello Hello
	since time.Time

	mu     sync.Mutex
	runs   map[string]*remoteRun
	waits  map[string]chan Message // replies to start and capture, by ID
	seq    uint64
	closed bool
}

// remoteRun is a run in progress on the executor.
type remoteRun struct {
	run  *executor.Run
	feed *executor.Feed
}

// Info describes the connection.
func (c *Conn) Info() Info {
	return Info{Environment: c.env, Connected: c.since, Busy: c.Busy(), Hello: c.hello}
}

// Busy returns the runs and helper calls in progress.
func (c *Conn) Busy() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.runs) + len(c.waits)
}

func (c *Conn) send(ctx context.Context, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, c.ws, m)
}

func (c *Conn) heartbeat(ctx context.Context) {
	t := time.NewTicker(c.hub.opts.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, c.hub.opts.Heartbeat)
		err := c.ws.Ping(pctx)
		cancel()
		if err != nil {
			_ = c.ws.CloseNow() // ends read
			return
		}
	}
}

// read dispatches the executor's messages until the connection ends.
func (c *Conn) read(ctx context.Context) error {
	for {
		var m Message
		if err := wsjson.Read(ctx, c.ws, &m); err != nil {
			return err
		}
		switch m.Type {
		case typeOutput:
			if rr := c.run(m.ID); rr != nil {
				rr.feed.Write(m.Data)
			}
		case typeExit:
			c.mu.Lock()
			rr := c.runs[m.ID]
			delete(c.runs, m.ID)
			c.mu.Unlock()
			if rr != nil && m.Exit != nil {
				rr.feed.End(executor.Result{Status: m.Exit.Status, ExitCode: m.Exit.ExitCode, Finished: m.Exit.Finished, OutputTruncated: m.Exit.Truncated})
			}
		case typeStarted, typeFailed, typeCaptured:
			c.mu.Lock()
			ch := c.waits[m.ID]
			delete(c.waits, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		default:
			logger().Warn("executor sent an unknown message", "environment", c.env, "pod", c.hello.Pod, "type", m.Type)
		}
	}
}

func (c *Conn) run(id string) *remoteRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[id]
}

// lost fails every run in progress and wakes every waiting call.
func (c *Conn) lost() {
	c.mu.Lock()
	c.closed = true
	runs, waits := c.runs, c.waits
	c.runs, c.waits = map[string]*remoteRun{}, map[string]chan Message{}
	c.mu.Unlock()
	for _, rr := range runs {
		rr.feed.Write([]byte(LostNotice))
		rr.feed.End(executor.Result{Status: executor.StatusFailed, ExitCode: -1})
	}
	for _, ch := range waits {
		ch <- Message{Type: typeFailed, Error: ErrLost.Error()}
	}
}

// call sends m and waits for the reply with the same ID. register runs under the lock that
// registers the wait, so a run is known before any of its output can arrive.
func (c *Conn) call(ctx context.Context, m Message, register func()) (Message, error) {
	ch := make(chan Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Message{}, ErrLost
	}
	c.waits[m.ID] = ch
	if register != nil {
		register()
	}
	c.mu.Unlock()
	if err := c.send(ctx, m); err != nil {
		c.forget(m.ID)
		return Message{}, fmt.Errorf("%w: %w", ErrLost, err)
	}
	select {
	case reply := <-ch:
		return reply, nil
	case <-ctx.Done():
		c.forget(m.ID)
		return Message{}, ctx.Err()
	}
}

func (c *Conn) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.waits, id)
}

// StartOptions is what the executor needs besides the job.
type StartOptions struct {
	Secrets    []string // secret names the script declares
	Kubernetes bool     // pin the run to the environment's cluster
	Files      []File   // uploaded files
}

// Start runs job on the executor. The run's output and result arrive over the connection; if
// the connection is lost, the run ends as failed. It returns executor.ErrBusy when the
// executor runs as many runs as it accepts, and the clusters the run was pinned to.
func (c *Conn) Start(ctx context.Context, job executor.Job, opts StartOptions) (*executor.Run, []kube.Cluster, error) {
	if !validID.MatchString(job.ID) {
		return nil, nil, fmt.Errorf("remote: invalid run ID %q", job.ID)
	}
	run, feed, err := executor.NewRemote(job, c.hub.opts.MaxOutputBytes, &control{c: c, id: job.ID})
	if err != nil {
		return nil, nil, err
	}
	m := Message{Type: typeStart, ID: job.ID, Start: &Start{Job: job, Secrets: opts.Secrets, Kubernetes: opts.Kubernetes, Files: opts.Files}}
	reply, err := c.call(ctx, m, func() { c.runs[job.ID] = &remoteRun{run: run, feed: feed} })
	if err == nil && reply.Type != typeStarted {
		err = replyError(reply)
	}
	if err != nil {
		c.mu.Lock()
		delete(c.runs, job.ID)
		c.mu.Unlock()
		if reply.Type == "" { // no answer (canceled): the run may have started anyway
			_ = c.send(context.Background(), Message{Type: typeKill, ID: job.ID})
		}
		return nil, nil, err
	}
	var clusters []kube.Cluster
	if reply.Started != nil {
		clusters = reply.Started.Clusters
	}
	return run, clusters, nil
}

func replyError(m Message) error {
	switch m.Error {
	case errBusy:
		return executor.ErrBusy
	case ErrLost.Error():
		return ErrLost
	}
	return errors.New(m.Error)
}

// Capture runs a dropdown helper on the executor and returns its output.
func (c *Conn) Capture(ctx context.Context, command []string, dir string, secretNames []string, kubernetes bool) ([]byte, error) {
	c.mu.Lock()
	c.seq++
	id := "helper-" + strconv.FormatUint(c.seq, 10)
	c.mu.Unlock()
	reply, err := c.call(ctx, Message{Type: typeCapture, ID: id, Capture: &Capture{Command: command, Dir: dir, Secrets: secretNames, Kubernetes: kubernetes}}, nil)
	if err != nil {
		return nil, err
	}
	if reply.Type != typeCaptured {
		return nil, replyError(reply)
	}
	return reply.Data, nil
}

// control forwards a remote run's input and signals to its executor.
type control struct {
	c  *Conn
	id string
}

func (k *control) post(m Message) error {
	m.ID = k.id
	if k.c.run(k.id) == nil {
		return executor.ErrInputClosed
	}
	return k.c.send(context.Background(), m)
}

func (k *control) Input(line string, secret bool) error {
	return k.post(Message{Type: typeInput, Data: []byte(line), Secret: secret})
}
func (k *control) CloseInput() error { return k.post(Message{Type: typeEOF}) }
func (k *control) Stop()             { _ = k.post(Message{Type: typeStop}) }
func (k *control) Kill()             { _ = k.post(Message{Type: typeKill}) }
func (k *control) Resize(rows, cols int) error {
	return k.post(Message{Type: typeResize, Rows: rows, Cols: cols})
}
