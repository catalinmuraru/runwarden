package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/params"
)

type scriptSummary struct {
	Name        string `json:"name"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
}

type option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type paramView struct {
	Name             string   `json:"name"`
	Type             string   `json:"type"` // parameter type, or "flag"
	Description      string   `json:"description,omitempty"`
	Required         bool     `json:"required"`
	Default          any      `json:"default,omitempty"`
	HasDefault       bool     `json:"has_default,omitempty"` // set for secure defaults, which are not sent
	Constant         bool     `json:"constant,omitempty"`
	Secure           bool     `json:"secure,omitempty"`
	Values           []option `json:"values,omitempty"`
	DynamicValues    bool     `json:"dynamic_values,omitempty"`
	DependsOn        []string `json:"depends_on,omitempty"`
	Min              *int64   `json:"min,omitempty"`
	Max              *int64   `json:"max,omitempty"`
	MaxLength        *int     `json:"max_length,omitempty"`
	RegexDescription string   `json:"regex_description,omitempty"`
	FileRecursive    bool     `json:"file_recursive,omitempty"`
	FileType         string   `json:"file_type,omitempty"`
	FileExtensions   []string `json:"file_extensions,omitempty"`
}

type scriptView struct {
	scriptSummary
	RequiresTerminal bool        `json:"requires_terminal"`
	TimeoutSeconds   int64       `json:"timeout_seconds"` // the run is stopped after this long
	Parameters       []paramView `json:"parameters"`
}

func (s *Server) listScripts(w http.ResponseWriter, r *http.Request) {
	out := []scriptSummary{}
	user := currentUser(r)
	if c := s.catalog.Load(); c != nil {
		for _, sc := range c.Scripts {
			if !s.opts.Policy.CanRun(user, sc) {
				continue
			}
			out = append(out, scriptSummary{Name: sc.Name, Group: sc.Group, Description: sc.Description})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": out})
}

func (s *Server) getScript(w http.ResponseWriter, r *http.Request) {
	sc := s.allowedScript(r, r.PathValue("name"))
	if sc == nil {
		writeError(w, http.StatusNotFound, "unknown script")
		return
	}
	view := scriptView{
		scriptSummary:    scriptSummary{Name: sc.Name, Group: sc.Group, Description: sc.Description},
		RequiresTerminal: sc.RequiresTerminal,
		TimeoutSeconds:   int64(s.exec.Timeout(sc.Timeout).Seconds()),
		Parameters:       []paramView{},
	}
	for _, p := range sc.Parameters {
		view.Parameters = append(view.Parameters, s.paramView(r.Context(), sc, p))
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) paramView(ctx context.Context, sc *config.Script, p *params.Parameter) paramView {
	v := paramView{
		Name:             p.Name,
		Type:             string(p.Type),
		Description:      p.Description,
		Required:         p.Required,
		Constant:         p.Constant,
		Secure:           p.Secure,
		DynamicValues:    p.ValuesScript != "",
		DependsOn:        config.Placeholders(p.ValuesScript),
		Min:              p.Min,
		Max:              p.Max,
		MaxLength:        p.MaxLength,
		RegexDescription: p.RegexDescription,
		FileRecursive:    p.FileRecursive,
		FileType:         string(p.FileType),
		FileExtensions:   p.FileExtensions,
	}
	if p.NoValue {
		v.Type = "flag"
	}
	for _, val := range p.Values {
		v.Values = append(v.Values, option{Value: val, Label: label(p, val)})
	}

	def := p.Default
	if def.Kind == params.KindNone && p.DefaultScript != "" && !p.Secure {
		def = s.scriptDefault(ctx, sc, p)
	}
	switch {
	case def.Kind == params.KindNone:
	case p.Secure:
		v.HasDefault = true
	case def.Kind == params.KindFlag:
		v.Default = def.Flag
	case def.Kind == params.KindList:
		v.Default = def.List
	default:
		v.Default = def.Str
	}
	return v
}

// scriptDefault evaluates a default produced by a command. Defaults that depend on other
// parameters are left to the user.
func (s *Server) scriptDefault(ctx context.Context, sc *config.Script, p *params.Parameter) params.Value {
	if len(config.Placeholders(p.DefaultScript)) > 0 {
		return params.Value{}
	}
	args, err := config.ExpandCommand(p.DefaultScript, func(string) (string, bool) { return "", false })
	if err != nil {
		return params.Value{}
	}
	out, err := s.capture(ctx, s.resolveCommand(args), sc.WorkingDirectory)
	if err != nil {
		slog.Warn("default script failed", "script", sc.Name, "parameter", p.Name, "err", err)
		return params.Value{}
	}
	lines := splitLines(out)
	if p.IsList() {
		return params.List(lines...)
	}
	return params.Scalar(strings.TrimSpace(string(out)))
}

func label(p *params.Parameter, value string) string {
	if l, ok := p.ValuesUIMapping[value]; ok {
		return l
	}
	return value
}

func (s *Server) parameterValues(w http.ResponseWriter, r *http.Request) {
	sc := s.allowedScript(r, r.PathValue("name"))
	if sc == nil {
		writeError(w, http.StatusNotFound, "unknown script")
		return
	}
	p := sc.Parameter(r.PathValue("param"))
	if p == nil {
		writeError(w, http.StatusNotFound, "unknown parameter")
		return
	}
	var values []string
	if p.ValuesScript == "" {
		values = p.Values
	} else {
		q := r.URL.Query()
		var err error
		values, err = s.dynamicValues(r.Context(), sc, p, func(name string) (any, bool) {
			if !q.Has(name) {
				return nil, false
			}
			if vs := q[name]; len(vs) > 1 {
				list := make([]any, len(vs))
				for i, v := range vs {
					list[i] = v
				}
				return list, true
			}
			return q.Get(name), true
		})
		if err != nil {
			writeValuesError(w, err)
			return
		}
	}
	opts := []option{}
	for _, v := range values {
		opts = append(opts, option{Value: v, Label: label(p, v)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": opts})
}

// dependencyError means the values can't be computed until other parameters are valid.
type dependencyError struct{ problems []string }

func (e *dependencyError) Error() string { return strings.Join(e.problems, "; ") }

func writeValuesError(w http.ResponseWriter, err error) {
	if errors.Is(err, errHelpersBusy) {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	var dep *dependencyError
	if errors.As(err, &dep) {
		writeError(w, http.StatusBadRequest, "parameters this list depends on are missing or invalid", dep.problems...)
		return
	}
	slog.Warn("values script failed", "err", err)
	writeError(w, http.StatusBadGateway, "could not load values")
}

// dynamicValues runs a parameter's values script. ${Other} references are filled from raw
// (client input) or the other parameter's default, after validating them with their own rules.
func (s *Server) dynamicValues(ctx context.Context, sc *config.Script, p *params.Parameter, raw func(string) (any, bool)) ([]string, error) {
	var problems []string
	lookup := func(name string) (string, bool) {
		dep := sc.Parameter(name)
		if dep == nil {
			problems = append(problems, fmt.Sprintf("unknown parameter %q", name))
			return "", true
		}
		v := dep.Default
		if rv, ok := raw(name); ok {
			nv, err := dep.Normalize(rv)
			if err != nil {
				problems = append(problems, fmt.Sprintf("parameter %q has an invalid value type", name))
				return "", true
			}
			v = nv
		}
		if err := dep.Validate(v); err != nil {
			problems = append(problems, err.Error())
			return "", true
		}
		if v.IsEmpty() && v.Kind != params.KindFlag {
			problems = append(problems, fmt.Sprintf("parameter %q needs a value first", name))
			return "", true
		}
		return argString(dep, v), true
	}
	args, err := config.ExpandCommand(p.ValuesScript, lookup)
	if len(problems) > 0 {
		return nil, &dependencyError{problems: problems}
	}
	if err != nil {
		return nil, err
	}
	out, err := s.capture(ctx, s.resolveCommand(args), sc.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// resolveCommand makes a relative helper path absolute against the base dir, as the config
// loader does for script paths. Bare names are looked up in PATH.
func (s *Server) resolveCommand(args []string) []string {
	if len(args) > 0 && !filepath.IsAbs(args[0]) && strings.ContainsAny(args[0], `/\`) {
		args[0] = filepath.Join(s.opts.BaseDir, args[0])
	}
	return args
}

func argString(p *params.Parameter, v params.Value) string {
	switch v.Kind {
	case params.KindFlag:
		return strconv.FormatBool(v.Flag)
	case params.KindList:
		sep := p.Separator
		if sep == "" {
			sep = ","
		}
		return strings.Join(v.List, sep)
	}
	return v.Str
}

func splitLines(out []byte) []string {
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
