package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// Hash identifies the parts of s that shape a run: command, working directory, parameters
// (with their helper commands and variables), secrets, kubeconfig, redact patterns and
// terminal. Two pods that load the same runner config get the same hash, so an executor can
// tell whether it runs what the server validated.
func (s *Script) Hash() string {
	type param struct {
		Name, Param, Type, Default, DefaultScript string
		Constant, Secure, NoValue, SameArgParam   bool
		ArgMode, Separator, PassAs, EnvVar        string
		ValuesScript, FileDir, FileType           string
		FileRecursive                             bool
		StdinExpectedText                         string
	}
	type shape struct {
		Command          []string
		WorkingDirectory string
		Parameters       []param
		Secrets          []string
		Kubernetes       bool
		Redact           []string
		Terminal         bool
	}
	v := shape{
		Command: s.Command, WorkingDirectory: s.WorkingDirectory, Secrets: s.Secrets,
		Kubernetes: s.Kubernetes, Terminal: s.RequiresTerminal,
	}
	for _, re := range s.RedactPatterns {
		v.Redact = append(v.Redact, re.String())
	}
	for _, p := range s.Parameters {
		def, _ := json.Marshal(p.Default)
		v.Parameters = append(v.Parameters, param{
			Name: p.Name, Param: p.Param, Type: string(p.Type), Default: string(def), DefaultScript: p.DefaultScript,
			Constant: p.Constant, Secure: p.Secure, NoValue: p.NoValue, SameArgParam: p.SameArgParam,
			ArgMode: string(p.ArgMode), Separator: p.Separator, PassAs: string(p.PassAs), EnvVar: p.EnvVar,
			ValuesScript: p.ValuesScript, FileDir: p.FileDir, FileType: string(p.FileType),
			FileRecursive: p.FileRecursive, StdinExpectedText: p.StdinExpectedText,
		})
	}
	b, _ := json.Marshal(v) // plain strings, bools and slices: cannot fail
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Helper kinds: which command of a parameter produces its dropdown values or its default.
const (
	HelperValues  = "values"
	HelperDefault = "default"
)

// Helper returns the command template of p for kind (HelperValues or HelperDefault), or ""
// when p has none.
func Helper(p *params.Parameter, kind string) string {
	switch kind {
	case HelperValues:
		return p.ValuesScript
	case HelperDefault:
		return p.DefaultScript
	}
	return ""
}

// ExpandProgram expands a helper command template as ExpandCommand does and returns the
// program apart from its arguments. A template whose program contains a placeholder is
// refused: the program always comes from the config itself.
func ExpandProgram(cmd string, lookup func(name string) (string, bool)) (string, []string, error) {
	args, err := splitCommand(cmd)
	if err != nil {
		return "", nil, err
	}
	if len(args) == 0 {
		return "", nil, errors.New("empty command")
	}
	if placeholder.MatchString(args[0]) {
		return "", nil, errors.New("the program of a helper command cannot depend on parameter values")
	}
	expanded, err := ExpandCommand(cmd, lookup)
	if err != nil {
		return "", nil, err
	}
	return args[0], expanded[1:], nil
}

// ResolveProgram makes a relative program path absolute against baseDir, as the loader does
// for script paths. Bare names are left to the PATH lookup.
func ResolveProgram(baseDir, program string) string {
	if !filepath.IsAbs(program) && strings.ContainsAny(program, `/\`) {
		return filepath.Join(baseDir, program)
	}
	return program
}
