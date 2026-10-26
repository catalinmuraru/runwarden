// Package config loads runner configs (one JSON file per script) into a Catalog.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// Script is one runnable script, parsed and validated from a runner config.
type Script struct {
	Name             string
	Command          []string // script path plus fixed arguments; executed without a shell
	WorkingDirectory string
	Description      string
	Group            string
	// Tags name the script's audience; access rules select scripts by name or by "tag:<tag>".
	Tags []string
	// EnvironmentParameters maps the parameters that select an environment to their use:
	// EnvTarget (needs run access there) or EnvSource (needs view access there).
	EnvironmentParameters map[string]string
	// Secrets are the names of the secrets a run receives from its environment (see package secrets).
	Secrets []string
	// Kubernetes runs get the kubeconfig of their environment (see package kube): choosing the
	// environment chooses the cluster.
	Kubernetes  bool
	OutputFiles []string // globs offered for download after a run
	// RedactPatterns are regexes whose matches are masked in the output, for secrets a script
	// prints that are not parameters (e.g. generated passwords).
	RedactPatterns   []*regexp.Regexp
	RequiresTerminal bool
	// Timeout overrides the global run timeout for this script; zero means the global one.
	Timeout    time.Duration
	Scheduling bool
	Parameters []*params.Parameter
	SourceFile string

	// IncludeTemplates are includes that depend on parameter values (contain "${").
	// They are resolved once values are known.
	IncludeTemplates []string
}

// Uses of an environment parameter.
const (
	EnvTarget = "target" // the run changes this environment
	EnvSource = "source" // the run only reads from it (e.g. the source of a copy)
)

// HasTag reports whether the script carries tag (case-insensitive).
func (s *Script) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// Parameter returns the parameter with the given name, or nil.
func (s *Script) Parameter(name string) *params.Parameter {
	for _, p := range s.Parameters {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Values normalizes and validates values received from a client (decoded JSON keyed by
// parameter name). Parameters missing from raw take their default. All problems are returned
// together; error messages never contain the values themselves.
func (s *Script) Values(raw map[string]any) (map[string]params.Value, error) {
	out := make(map[string]params.Value, len(s.Parameters))
	var errs []error
	for _, p := range s.Parameters {
		v := p.Default
		if r, ok := raw[p.Name]; ok {
			nv, err := p.Normalize(r)
			if err != nil {
				errs = append(errs, &params.ValidationError{Param: p.Name, Reason: "has an invalid value type"})
				continue
			}
			v = nv
		}
		if err := p.Validate(v); err != nil {
			errs = append(errs, err)
			continue
		}
		out[p.Name] = v
	}

	var unknown []string
	for name := range raw {
		if s.Parameter(name) == nil {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		errs = append(errs, fmt.Errorf("unknown parameter %q", name))
	}
	return out, errors.Join(errs...)
}
