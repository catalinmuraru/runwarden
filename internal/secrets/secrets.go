// Package secrets gives runs the secrets of their environment: one directory per environment
// (a mounted Kubernetes Secret, one file per secret) plus a "common" one. A run receives only the
// secrets its runner config declares, read fresh at every start so rotated values apply at once.
package secrets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Common is the directory of secrets that do not depend on the environment.
const Common = "common"

// maxSize bounds one secret file.
const maxSize = 64 << 10

var (
	// NamePattern is what a secret (an environment variable name) must look like.
	NamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	envPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
)

// SourcePrefix is prepended to the secrets of a run's source environment.
const SourcePrefix = "SOURCE_"

// Store reads secrets from Dir/<environment>/<NAME>.
type Store struct {
	Dir string // empty: no secrets at all
}

// MissingError means a declared secret exists neither in the environment nor in common.
type MissingError struct {
	Name, Environment string
}

func (e *MissingError) Error() string {
	where := e.Environment
	if where == "" || where == "-" {
		where = "the common secrets"
	}
	return fmt.Sprintf("secret %s is not available in %s", e.Name, where)
}

// ErrSeveralTargets refuses giving secrets to a run that targets more than one environment.
var ErrSeveralTargets = errors.New("this script uses secrets, so it can run in one environment at a time")

// read returns the secrets of one environment directory (none if it does not exist).
func (s Store) read(env string) (map[string]string, error) {
	out := map[string]string{}
	if s.Dir == "" || env == "" || env == "-" {
		return out, nil
	}
	if !envPattern.MatchString(env) {
		return nil, fmt.Errorf("invalid environment name %q", env)
	}
	dir := filepath.Join(s.Dir, env)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if !NamePattern.MatchString(name) {
			continue // ..data, ..2026_10_26... and other Kubernetes bookkeeping
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path) // follows the Kubernetes symlinks
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > maxSize {
			return nil, fmt.Errorf("secret %s in %s is larger than %d bytes", name, env, maxSize)
		}
		data, err := os.ReadFile(path) //nolint:gosec // name and environment are checked against strict patterns
		if err != nil {
			return nil, err
		}
		// One trailing newline is a file-editing artefact, not part of the value.
		v := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		out[name] = v
	}
	return out, nil
}

// Result is the environment a run receives.
type Result struct {
	Env map[string]string // variable name → value
}

// Names returns the variables given, sorted.
func (r Result) Names() []string {
	out := make([]string, 0, len(r.Env))
	for k := range r.Env {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Values returns the secret values, for masking.
func (r Result) Values() []string {
	out := make([]string, 0, len(r.Env))
	for _, v := range r.Env {
		out = append(out, v)
	}
	return out
}

// Resolve returns the declared secrets for a run: from its target environment (or common), and
// the source environment's own values as SOURCE_<NAME> (common values do not depend on the
// environment, so they are not repeated). targets and sources are environment names; "-" or
// none means the script has no environment, so only common applies.
func (s Store) Resolve(names, targets, sources []string) (Result, error) {
	res := Result{Env: map[string]string{}}
	if len(names) == 0 {
		return res, nil
	}
	targets, sources = compact(targets), compact(sources)
	if len(targets) > 1 || len(sources) > 1 {
		return res, ErrSeveralTargets
	}
	common, err := s.read(Common)
	if err != nil {
		return res, err
	}
	get := func(env, prefix string) error {
		vals, err := s.read(env)
		if err != nil {
			return err
		}
		for _, n := range names {
			v, ok := vals[n]
			if !ok {
				if _, shared := common[n]; shared && prefix != "" {
					continue // already given unprefixed
				}
				v, ok = common[n]
			}
			if !ok {
				return &MissingError{Name: n, Environment: env}
			}
			res.Env[prefix+n] = v
		}
		return nil
	}
	target := ""
	if len(targets) == 1 {
		target = targets[0]
	}
	if err := get(target, ""); err != nil {
		return Result{}, err
	}
	if len(sources) == 1 {
		if err := get(sources[0], SourcePrefix); err != nil {
			return Result{}, err
		}
	}
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

// Inventory lists the secret names per directory (environments and common), never values.
func (s Store) Inventory() (map[string][]string, error) {
	out := map[string][]string{}
	if s.Dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 || !envPattern.MatchString(e.Name()) {
			continue
		}
		vals, err := s.read(e.Name())
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(vals))
		for n := range vals {
			names = append(names, n)
		}
		slices.Sort(names)
		out[e.Name()] = names
	}
	return out, nil
}

// Info describes one secret without its value.
type Info struct {
	Name        string    `json:"name"`
	Environment string    `json:"environment"` // the directory: an environment or Common
	Modified    time.Time `json:"modified"`
}

// Describe lists the secrets of the given environment directories by name and modification
// time, never reading a value. Missing directories list nothing.
func (s Store) Describe(envs ...string) ([]Info, error) {
	var out []Info
	for _, env := range envs {
		if s.Dir == "" || env == "" || env == "-" {
			continue
		}
		if !envPattern.MatchString(env) {
			return nil, fmt.Errorf("invalid environment name %q", env)
		}
		dir := filepath.Join(s.Dir, env)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !NamePattern.MatchString(e.Name()) {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, e.Name())) // follows the Kubernetes symlinks
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			out = append(out, Info{Name: e.Name(), Environment: env, Modified: info.ModTime().UTC()})
		}
	}
	return out, nil
}
