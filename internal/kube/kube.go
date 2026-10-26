// Package kube pins a run to the Kubernetes cluster of its environment: one kubeconfig per
// environment (a mounted Kubernetes Secret, <dir>/<environment>/config) is read fresh at every
// start, reduced to its current context and renamed after the environment, so a run of
// "production" holds the context "production" and nothing else. The file is opaque otherwise:
// how it authenticates (exec plugins, tokens, certificates) is left to it and to the
// environment's secrets.
package kube

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName is the kubeconfig file inside an environment directory.
const FileName = "config"

// maxSize bounds one kubeconfig file.
const maxSize = 256 << 10

// Variables a run receives next to KUBECONFIG.
const (
	ContextVar       = "RUNWARDEN_KUBE_CONTEXT"
	SourceContextVar = "RUNWARDEN_KUBE_SOURCE_CONTEXT"
)

var envPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// Store reads kubeconfigs from Dir/<environment>/config.
type Store struct {
	Dir string // empty: no cluster for any environment
}

// MissingError means an environment has no kubeconfig.
type MissingError struct {
	Environment string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("no Kubernetes cluster is configured for %s", e.Environment)
}

var (
	// ErrSeveralTargets refuses a run that targets more than one environment with a cluster.
	ErrSeveralTargets = errors.New("this script uses a Kubernetes cluster, so it can run in one environment at a time")
	// ErrNoEnvironment refuses a run that needs a cluster but has no environment.
	ErrNoEnvironment = errors.New("this script uses a Kubernetes cluster but no environment was chosen")
)

// Cluster is what may be shown about an environment's kubeconfig: never credentials.
type Cluster struct {
	Environment string
	Context     string // the current context in the mounted file
	Server      string
	Namespace   string
}

// entry is one environment's pinned context with its cluster and user, still unrenamed.
type entry struct {
	info    Cluster
	context map[string]any
	cluster map[string]any
	user    map[string]any // nil: the context names no user
}

type named struct {
	Name    string         `yaml:"name"`
	Cluster map[string]any `yaml:"cluster"`
	Context map[string]any `yaml:"context"`
	User    map[string]any `yaml:"user"`
}

type file struct {
	Clusters       []named `yaml:"clusters"`
	Contexts       []named `yaml:"contexts"`
	Users          []named `yaml:"users"`
	CurrentContext string  `yaml:"current-context"`
}

// path returns the kubeconfig path of env, or an error for names that could leave Dir.
func (s Store) path(env string) (string, error) {
	if !envPattern.MatchString(env) {
		return "", fmt.Errorf("invalid environment name %q", env)
	}
	return filepath.Join(s.Dir, env, FileName), nil
}

// load reads and validates the kubeconfig of env.
func (s Store) load(env string) (*entry, error) {
	if s.Dir == "" || env == "" || env == "-" {
		return nil, &MissingError{Environment: env}
	}
	path, err := s.path(env)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path) // follows the Kubernetes symlinks
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &MissingError{Environment: env}
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("kubeconfig of %s is not a regular file", env)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("kubeconfig of %s is larger than %d bytes", env, maxSize)
	}
	data, err := os.ReadFile(path) //nolint:gosec // the environment is checked against a strict pattern
	if err != nil {
		return nil, err
	}
	e, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig of %s: %w", env, err)
	}
	e.info.Environment = env
	return e, nil
}

// parse validates a kubeconfig (YAML or JSON) and returns its current context.
func parse(data []byte) (*entry, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		// The parser's message may quote file content (credentials); keep only our own reason.
		return nil, errors.New("not a valid kubeconfig (YAML or JSON)")
	}
	if f.CurrentContext == "" {
		return nil, errors.New("current-context is not set")
	}
	find := func(list []named, kind, name string) (*named, error) {
		var found *named
		for i := range list {
			if list[i].Name != name {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("%s %q is defined twice", kind, name)
			}
			found = &list[i]
		}
		if found == nil {
			return nil, fmt.Errorf("%s %q does not exist", kind, name)
		}
		return found, nil
	}
	ctx, err := find(f.Contexts, "context", f.CurrentContext)
	if err != nil {
		return nil, err
	}
	if ctx.Context == nil {
		return nil, fmt.Errorf("context %q is empty", ctx.Name)
	}
	clusterName, _ := ctx.Context["cluster"].(string)
	if clusterName == "" {
		return nil, fmt.Errorf("context %q names no cluster", ctx.Name)
	}
	cl, err := find(f.Clusters, "cluster", clusterName)
	if err != nil {
		return nil, err
	}
	server, _ := cl.Cluster["server"].(string)
	if server == "" {
		return nil, fmt.Errorf("cluster %q has no server", cl.Name)
	}
	e := &entry{context: ctx.Context, cluster: cl.Cluster}
	e.info.Context = ctx.Name
	e.info.Server = server
	e.info.Namespace, _ = ctx.Context["namespace"].(string)
	if err := relativePaths(cl.Cluster, "cluster "+cl.Name, "certificate-authority"); err != nil {
		return nil, err
	}
	if userName, _ := ctx.Context["user"].(string); userName != "" {
		u, err := find(f.Users, "user", userName)
		if err != nil {
			return nil, err
		}
		if u.User == nil {
			u.User = map[string]any{}
		}
		if err := relativePaths(u.User, "user "+u.Name, "client-certificate", "client-key", "tokenFile"); err != nil {
			return nil, err
		}
		if exec, ok := u.User["exec"].(map[string]any); ok {
			// kubectl resolves a command with a path separator against the file's directory,
			// which is the run's HOME once copied.
			if cmd, _ := exec["command"].(string); strings.ContainsRune(cmd, '/') && !filepath.IsAbs(cmd) {
				return nil, fmt.Errorf("user %s: exec command must be absolute or looked up in PATH", u.Name)
			}
		}
		e.user = u.User
	}
	return e, nil
}

// relativePaths refuses file references that would no longer resolve once the kubeconfig is
// copied into the run's HOME.
func relativePaths(m map[string]any, where string, keys ...string) error {
	for _, k := range keys {
		if p, _ := m[k].(string); p != "" && !filepath.IsAbs(p) {
			return fmt.Errorf("%s: %s must be an absolute path", where, k)
		}
	}
	return nil
}

// Result is what a run receives: the kubeconfig to write into its private HOME and the
// variables naming its contexts.
type Result struct {
	Kubeconfig []byte
	Env        map[string]string
	Clusters   []Cluster // target first, then source (if any and different)
}

// Resolve builds the kubeconfig of a run: the target environment's cluster as the current
// context, plus the source environment's cluster for two-environment runs. Contexts, clusters
// and users are renamed to the environment, so two environments on one cluster (different
// namespaces) and same-named entries from different files do not collide.
func (s Store) Resolve(targets, sources []string) (Result, error) {
	targets, sources = compact(targets), compact(sources)
	if len(targets) > 1 || len(sources) > 1 {
		return Result{}, ErrSeveralTargets
	}
	if len(targets) == 0 {
		return Result{}, ErrNoEnvironment
	}
	envs := targets
	if len(sources) == 1 && sources[0] != targets[0] {
		envs = append(envs, sources[0])
	}
	out := map[string]any{
		"apiVersion":      "v1",
		"kind":            "Config",
		"current-context": targets[0],
		"preferences":     map[string]any{},
	}
	var clusters, contexts, users []any
	res := Result{Env: map[string]string{ContextVar: targets[0]}}
	for _, env := range envs {
		e, err := s.load(env)
		if err != nil {
			return Result{}, err
		}
		ctx := map[string]any{}
		for k, v := range e.context {
			ctx[k] = v
		}
		ctx["cluster"] = env
		clusters = append(clusters, map[string]any{"name": env, "cluster": e.cluster})
		if e.user != nil {
			ctx["user"] = env
			users = append(users, map[string]any{"name": env, "user": e.user})
		}
		contexts = append(contexts, map[string]any{"name": env, "context": ctx})
		res.Clusters = append(res.Clusters, e.info)
	}
	if len(sources) == 1 {
		res.Env[SourceContextVar] = sources[0]
	}
	out["clusters"], out["contexts"], out["users"] = clusters, contexts, users
	if users == nil {
		out["users"] = []any{}
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		return Result{}, err
	}
	res.Kubeconfig = data
	return res, nil
}

func compact(envs []string) []string {
	var out []string
	for _, e := range envs {
		if e != "" && e != "-" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// Status is one environment directory as the Admin area shows it.
type Status struct {
	Cluster
	Error string // why the kubeconfig cannot be used; empty when it can
}

// Inventory lists the environments that have a kubeconfig directory, with their cluster
// server or the reason the file is unusable. It never returns credentials.
func (s Store) Inventory() ([]Status, error) {
	if s.Dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Status
	for _, d := range entries {
		if !d.IsDir() && d.Type()&fs.ModeSymlink == 0 || !envPattern.MatchString(d.Name()) {
			continue // ..data and other Kubernetes bookkeeping
		}
		st := Status{Cluster: Cluster{Environment: d.Name()}}
		e, err := s.load(d.Name())
		switch {
		case err != nil:
			st.Error = err.Error()
		default:
			st.Cluster = e.info
		}
		out = append(out, st)
	}
	return out, nil
}

// Has reports whether env has a kubeconfig file (without validating it).
func (s Store) Has(env string) bool {
	if s.Dir == "" {
		return false
	}
	path, err := s.path(env)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
