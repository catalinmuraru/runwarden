// Package config loads runner configs (one JSON file per script) into a Catalog.
package config

import (
	"errors"
	"fmt"
	"slices"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// Script is one runnable script, parsed and validated from a runner config.
type Script struct {
	Name             string
	Command          []string // script path plus fixed arguments; executed without a shell
	WorkingDirectory string
	Description      string
	Group            string
	AllowedUsers     []string // user names, "@group" or "*"
	AdminUsers       []string
	OutputFiles      []string // globs offered for download after a run
	RequiresTerminal bool
	Scheduling       bool
	Parameters       []*params.Parameter
	SourceFile       string

	// IncludeTemplates are includes that depend on parameter values (contain "${").
	// They are resolved once values are known.
	IncludeTemplates []string
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
