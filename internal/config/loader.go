package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// Options control how runner configs are loaded.
type Options struct {
	// BaseDir resolves relative script, working directory, include and file_dir paths.
	// Empty means the process working directory.
	BaseDir string
}

// Issue is a problem found in one config file.
type Issue struct {
	File string
	Msg  string
}

func (i Issue) String() string { return i.File + ": " + i.Msg }

// Catalog is the result of loading a runners directory. A file with errors is left out;
// the other files still load.
type Catalog struct {
	Scripts  []*Script // sorted by name
	Errors   []Issue
	Warnings []Issue
}

// Script returns the script with the given name, or nil.
func (c *Catalog) Script(name string) *Script {
	for _, s := range c.Scripts {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// LoadDir loads every *.json file under dir, recursively. Configs marked hidden are only
// used as include targets. The error is non-nil only when dir itself cannot be read.
func LoadDir(dir string, opts Options) (*Catalog, error) {
	base := opts.BaseDir
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		base = wd
	}
	info, err := os.Stat(dir) //nolint:gosec // dir is chosen by the operator, not by users
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	l := &loader{base: base, cat: &Catalog{}, sources: map[string]string{}}
	//nolint:gosec // dir is chosen by the operator, not by users
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			l.errorf(path, "%v", err)
			return nil
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".json") {
			l.loadFile(path)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	slices.SortFunc(l.cat.Scripts, func(a, b *Script) int { return strings.Compare(a.Name, b.Name) })
	return l.cat, nil
}

type loader struct {
	base    string
	cat     *Catalog
	sources map[string]string // script name -> file that defined it
}

func (l *loader) errorf(file, format string, args ...any) {
	l.cat.Errors = append(l.cat.Errors, Issue{File: file, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) warnf(file, format string, args ...any) {
	l.cat.Warnings = append(l.cat.Warnings, Issue{File: file, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(l.base, p)
}

func (l *loader) loadFile(file string) {
	data, err := os.ReadFile(file) //nolint:gosec // file comes from walking the operator's runners dir
	if err != nil {
		l.errorf(file, "%v", err)
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		l.errorf(file, "invalid JSON: %v", err)
		return
	}
	if raw, ok := obj["hidden"]; ok {
		var hidden flexBool
		if err := json.Unmarshal(raw, &hidden); err != nil {
			l.errorf(file, "hidden: %v", err)
			return
		}
		if hidden {
			return
		}
	}

	s, err := l.build(file, obj)
	if err != nil {
		l.errorf(file, "%v", err)
		return
	}
	if first, dup := l.sources[s.Name]; dup {
		l.errorf(file, "duplicate script name %q, already defined in %s", s.Name, first)
		return
	}
	l.sources[s.Name] = file
	l.cat.Scripts = append(l.cat.Scripts, s)
}

func (l *loader) build(file string, obj map[string]json.RawMessage) (*Script, error) {
	var includes stringOrList
	if raw, ok := obj["include"]; ok {
		if err := json.Unmarshal(raw, &includes); err != nil {
			return nil, fmt.Errorf("include: %w", err)
		}
	}
	var templates []string
	for _, inc := range includes {
		if strings.Contains(inc, "${") {
			templates = append(templates, inc)
			l.warnf(file, "include %q depends on parameter values and is not applied yet", inc)
			continue
		}
		if err := l.mergeInclude(file, obj, inc); err != nil {
			l.warnf(file, "include %q: %v", inc, err)
		}
	}

	merged, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var raw rawScript
	if err := json.Unmarshal(merged, &raw); err != nil {
		return nil, err
	}

	if strings.TrimSpace(raw.Name) == "" {
		return nil, errors.New("name is required")
	}
	if strings.TrimSpace(raw.ScriptPath) == "" {
		return nil, errors.New("script_path is required")
	}
	cmd, err := splitCommand(raw.ScriptPath)
	if err != nil {
		return nil, fmt.Errorf("script_path: %w", err)
	}
	if strings.ContainsAny(cmd[0], `/\`) {
		cmd[0] = l.resolve(cmd[0]) // bare names like "bash" are looked up in PATH
	}
	wd := l.resolve(raw.WorkingDirectory)
	if wd == "" {
		wd = l.base
	}

	s := &Script{
		Name:             raw.Name,
		Command:          cmd,
		WorkingDirectory: wd,
		Description:      raw.Description,
		Group:            raw.Group,
		AllowedUsers:     raw.AllowedUsers,
		AdminUsers:       raw.AdminUsers,
		OutputFiles:      raw.OutputFiles,
		RequiresTerminal: raw.RequiresTerminal == nil || bool(*raw.RequiresTerminal),
		Scheduling:       raw.Scheduling != nil && bool(raw.Scheduling.Enabled),
		SourceFile:       file,
		IncludeTemplates: templates,
	}
	for _, pattern := range raw.RedactPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("redact_patterns: %w", err)
		}
		s.RedactPatterns = append(s.RedactPatterns, re)
	}
	seen := map[string]bool{}
	for _, data := range raw.Parameters {
		p, err := l.buildParam(file, data)
		if err != nil {
			return nil, err
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("parameter %q is defined twice", p.Name)
		}
		seen[p.Name] = true
		s.Parameters = append(s.Parameters, p)
	}
	return s, nil
}

// mergeInclude adds top-level keys and parameters from an included file. Keys and parameters
// already present win; "include" and "hidden" are never inherited.
func (l *loader) mergeInclude(file string, obj map[string]json.RawMessage, inc string) error {
	data, err := os.ReadFile(l.resolve(inc))
	if err != nil {
		return err
	}
	var incObj map[string]json.RawMessage
	if err := json.Unmarshal(data, &incObj); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	for k, v := range incObj {
		switch k {
		case "include", "hidden", "parameters":
			continue
		}
		if _, exists := obj[k]; !exists {
			obj[k] = v
		}
	}

	var own, extra []json.RawMessage
	if raw, ok := obj["parameters"]; ok {
		if err := json.Unmarshal(raw, &own); err != nil {
			return fmt.Errorf("parameters: %w", err)
		}
	}
	if raw, ok := incObj["parameters"]; ok {
		if err := json.Unmarshal(raw, &extra); err != nil {
			return fmt.Errorf("parameters: %w", err)
		}
	}
	names := map[string]bool{}
	for _, p := range own {
		names[paramName(p)] = true
	}
	for _, p := range extra {
		name := paramName(p)
		if names[name] {
			l.warnf(file, "parameter %q is also defined in include %q; the include's definition is ignored", name, inc)
			continue
		}
		names[name] = true
		own = append(own, p)
	}
	merged, err := json.Marshal(own)
	if err != nil {
		return err
	}
	obj["parameters"] = merged
	return nil
}

func paramName(data json.RawMessage) string {
	var head struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &head)
	return head.Name
}

func (l *loader) buildParam(file string, data json.RawMessage) (*params.Parameter, error) {
	name := paramName(data)
	fail := func(format string, args ...any) error {
		label := name
		if label == "" {
			label = "(unnamed)"
		}
		return fmt.Errorf("parameter %q: %s", label, fmt.Sprintf(format, args...))
	}

	var rp rawParam
	if err := json.Unmarshal(data, &rp); err != nil {
		return nil, fail("%v", err)
	}
	if strings.TrimSpace(rp.Name) == "" {
		return nil, fail("name is required")
	}

	p := &params.Parameter{
		Name:              rp.Name,
		Param:             rp.Param,
		Type:              params.Type(rp.Type),
		Description:       rp.Description,
		Required:          bool(rp.Required),
		Constant:          bool(rp.Constant),
		Secure:            bool(rp.Secure),
		NoValue:           bool(rp.NoValue),
		SameArgParam:      bool(rp.SameArgParam),
		Separator:         ",",
		EnvVar:            rp.EnvVar,
		ValuesUIMapping:   rp.ValuesUIMapping,
		FileDir:           l.resolve(rp.FileDir),
		FileRecursive:     bool(rp.FileRecursive),
		FileExtensions:    rp.FileExtensions,
		ExcludedFiles:     rp.ExcludedFiles,
		StdinExpectedText: rp.StdinExpectedText,
	}
	switch lower := strings.ToLower(rp.Type); lower {
	case "":
		p.Type = params.TypeText
	case "ip", "ip4", "ip6", "ipv4", "ipv6":
		p.Type = params.Type(strings.ReplaceAll(lower, "v", ""))
	}
	if !params.KnownType(p.Type) {
		// Existing configs rely on unknown types behaving as free text.
		l.warnf(file, "parameter %q: unknown type %q, treated as text", p.Name, rp.Type)
		p.Type = params.TypeText
	}
	if rp.Separator != nil {
		p.Separator = *rp.Separator
	}

	switch mode := params.ArgMode(rp.MultiselectArgumentType); mode {
	case "":
		p.ArgMode = params.ArgSingle
		if rp.MultipleArguments != nil && bool(*rp.MultipleArguments) {
			p.ArgMode = params.ArgPerValue
		}
	case params.ArgSingle, params.ArgPerValue, params.ArgRepeatParam:
		p.ArgMode = mode
	default:
		return nil, fail("unknown multiselect_argument_type %q", rp.MultiselectArgumentType)
	}

	switch pass := params.PassAs(strings.ToLower(strings.TrimSpace(rp.PassAs))); pass {
	case params.PassDefault, params.PassArgument, params.PassEnv, params.PassStdin:
		p.PassAs = pass
	default:
		l.warnf(file, "parameter %q: unknown pass_as %q, passing as argument and env variable", p.Name, rp.PassAs)
	}

	if rp.Min != nil {
		v := int64(*rp.Min)
		p.Min = &v
	}
	if rp.Max != nil {
		v := int64(*rp.Max)
		p.Max = &v
	}
	if rp.MaxLength != nil {
		v := int(*rp.MaxLength)
		p.MaxLength = &v
	}
	if rp.Regex != nil && rp.Regex.Pattern != "" {
		re, err := regexp.Compile(`^(?:` + rp.Regex.Pattern + `)$`)
		if err != nil {
			return nil, fail("regex: %v", err)
		}
		p.Regex = re
		p.RegexDescription = rp.Regex.Description
	}

	values, err := decodeAny(rp.Values)
	if err != nil {
		return nil, fail("values: %v", err)
	}
	switch v := values.(type) {
	case nil:
	case []any:
		p.Values = make([]string, 0, len(v))
		for _, e := range v {
			switch t := e.(type) {
			case string:
				p.Values = append(p.Values, t)
			case json.Number:
				p.Values = append(p.Values, t.String())
			default:
				return nil, fail("values must contain strings or numbers")
			}
		}
	default:
		cmd, ok := scriptRef(v)
		if !ok {
			return nil, fail(`values must be a list or {"script": "..."}`)
		}
		p.ValuesScript = cmd
		l.checkScript(file, p.Name, "values", cmd)
	}

	switch ft := params.FileType(strings.ToLower(rp.FileType)); ft {
	case "":
		if len(p.FileExtensions) > 0 {
			p.FileType = params.FileTypeFile
		}
	case params.FileTypeFile, params.FileTypeDir:
		p.FileType = ft
	default:
		return nil, fail("unknown file_type %q", rp.FileType)
	}
	if p.Type == params.TypeServerFile && p.FileDir == "" {
		return nil, fail("file_dir is required for server_file")
	}

	def, err := decodeAny(rp.Default)
	if err != nil {
		return nil, fail("default: %v", err)
	}
	if cmd, ok := scriptRef(def); ok {
		p.DefaultScript = cmd
		l.checkScript(file, p.Name, "default", cmd)
	} else if def != nil {
		if p.Default, err = p.ParseDefault(def); err != nil {
			return nil, fail("default: %v", err)
		}
	}
	if p.Constant && p.Default.IsEmpty() && p.DefaultScript == "" {
		return nil, fail("constant parameters need a default")
	}
	return p, nil
}

// checkScript warns about value/default commands that need a shell: scripts never run
// through one, so pipelines and redirects must move into a script file.
func (l *loader) checkScript(file, param, what, cmd string) {
	if usesShellSyntax(cmd) {
		l.warnf(file, "parameter %q: %s script uses shell syntax, which is not supported; move it into a script file", param, what)
		return
	}
	if _, err := splitCommand(cmd); err != nil {
		l.warnf(file, "parameter %q: %s script: %v", param, what, err)
	}
}
