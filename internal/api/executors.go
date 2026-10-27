package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
	"github.com/catalinmuraru/runwarden/internal/kube"
	"github.com/catalinmuraru/runwarden/internal/remote"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

var (
	// errExecutorScope refuses a run that touches an executor environment and another one: the
	// executor holds only its own environment's credentials.
	errExecutorScope = errors.New("runs on an executor environment cannot use a second environment")
	// errNoExecutor means no executor of the run's environment is connected.
	errNoExecutor = errors.New("no executor is connected")
)

// startError is a start the executor refused (e.g. a missing secret); its message is for the user.
type startError struct{ err error }

func (e *startError) Error() string { return e.err.Error() }
func (e *startError) Unwrap() error { return e.err }

// executorEnv returns the environment of uses when it runs on executors. Such a run belongs to
// exactly one environment, as a target.
func (s *Server) executorEnv(uses []authz.EnvUse) (string, bool, error) {
	hub := s.opts.Executors
	if hub == nil {
		return "", false, nil
	}
	envs := authz.Environments(uses)
	for _, env := range envs {
		if !hub.Remote(env) {
			continue
		}
		if len(envs) > 1 || slices.ContainsFunc(uses, func(u authz.EnvUse) bool { return u.Use == config.EnvSource }) {
			return "", false, fmt.Errorf("%w (%s)", errExecutorScope, env)
		}
		return env, true, nil
	}
	return "", false, nil
}

// pick returns the least busy executor of env.
func (s *Server) pick(env string) (*remote.Conn, error) {
	if c := s.opts.Executors.Pick(env); c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("%w for environment %s", errNoExecutor, env)
}

// startRemote starts req on an executor of env and tracks it like a local run. The executor
// adds the environment's secrets and kubeconfig; the uploaded files travel with the start.
func (s *Server) startRemote(ctx context.Context, env string, req executor.Request) (*executor.Run, []history.Cluster, error) {
	files, err := readUploads(req.Uploads)
	if err != nil {
		return nil, nil, err
	}
	var clusters []kube.Cluster
	run, err := s.exec.Adopt(req.User, func() (*executor.Run, error) {
		conn, err := s.pick(env)
		if err != nil {
			return nil, err
		}
		run, cl, err := conn.Start(ctx, s.exec.Job(req), remote.StartOptions{
			Secrets: req.Script.Secrets, Kubernetes: req.Script.Kubernetes, Files: files})
		switch {
		case err == nil, errors.Is(err, executor.ErrBusy):
		case errors.Is(err, remote.ErrLost):
			err = fmt.Errorf("%w for environment %s", errNoExecutor, env)
		default:
			err = &startError{err}
		}
		clusters = cl
		return run, err
	})
	if err != nil {
		return nil, nil, err
	}
	out := make([]history.Cluster, len(clusters))
	for i, c := range clusters {
		out[i] = history.Cluster{Environment: c.Environment, Server: c.Server, Namespace: c.Namespace}
	}
	return run, out, nil
}

// readUploads reads the files below dir (a run's uploads), keyed by the paths the run refers to.
func readUploads(dir string) ([]remote.File, error) {
	var files []remote.File
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // our own upload dir
		if err != nil {
			return err
		}
		files = append(files, remote.File{Path: path, Data: data})
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return files, err
}

// runHelper runs a helper command (dropdown values, defaults) of sc for the environment the raw
// values select: on that environment's executor, or here with its secrets and kubeconfig.
func (s *Server) runHelper(ctx context.Context, sc *config.Script, raw func(string) (any, bool), args []string) ([]byte, error) {
	args = s.resolveCommand(args)
	uses := s.helperUses(sc, raw)
	env, onExecutor, err := s.executorEnv(uses)
	if err != nil {
		return nil, err
	}
	key := sc.WorkingDirectory + "\x00" + strings.Join(args, "\x00")
	if onExecutor {
		// The executor resolves the secrets itself: the environment keys the cache.
		key = "executor:" + env + "\x00" + key + "\x00" + strings.Join(sc.Secrets, ",") + fmt.Sprint(sc.Kubernetes)
		return s.capture(ctx, key, func(ctx context.Context) ([]byte, error) {
			conn, err := s.pick(env)
			if err != nil {
				return nil, err
			}
			out, err := conn.Capture(ctx, args, sc.WorkingDirectory, sc.Secrets, sc.Kubernetes)
			if errors.Is(err, remote.ErrLost) {
				err = fmt.Errorf("%w for environment %s", errNoExecutor, env)
			}
			return out, err
		})
	}
	sec, kc, err := s.helperEnv(sc, uses)
	if err != nil {
		return nil, err
	}
	key += "\x00" + fingerprint(sec) + "\x00" + kubeFingerprint(kc)
	return s.capture(ctx, key, func(ctx context.Context) ([]byte, error) {
		return s.exec.Capture(ctx, args, sc.WorkingDirectory, sec, kc)
	})
}

type executorPod struct {
	Pod       string         `json:"pod"`
	Version   string         `json:"version"`
	Connected time.Time      `json:"connected"`
	Busy      int            `json:"busy"`
	Capacity  int            `json:"capacity"`
	Isolation sandbox.Status `json:"isolation"`
}

type executorEnvironment struct {
	Environment string         `json:"environment"`
	Pods        []executorPod  `json:"pods"`
	Secrets     []secrets.Info `json:"secrets"` // names and modification times, as the first pod reports them
	cluster     *kube.Status
}

// executorOverview describes the environments that run on executors and their connected pods.
func (s *Server) executorOverview() []executorEnvironment {
	out := []executorEnvironment{}
	hub := s.opts.Executors
	if hub == nil {
		return out
	}
	for _, env := range hub.Environments() {
		row := executorEnvironment{Environment: env, Pods: []executorPod{}, Secrets: []secrets.Info{}}
		infos := hub.Executors(env)
		slices.SortFunc(infos, func(a, b remote.Info) int { return strings.Compare(a.Pod, b.Pod) })
		for i, in := range infos {
			row.Pods = append(row.Pods, executorPod{Pod: in.Pod, Version: in.Version, Connected: in.Connected,
				Busy: in.Busy, Capacity: in.Capacity, Isolation: in.Isolation})
			if i == 0 {
				row.Secrets = append(row.Secrets, in.Secrets...)
				row.cluster = in.Cluster
			}
		}
		out = append(out, row)
	}
	return out
}
