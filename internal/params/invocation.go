package params

import (
	"path/filepath"
	"strings"
)

// Mask replaces secure values wherever they would be shown or stored.
const Mask = "******"

// StdinInput is one value written to the script's stdin.
type StdinInput struct {
	Value      string
	ExpectText string // if set, write Value only after the script prints this text
}

// Invocation is everything a script receives for one run, derived from parameter values.
type Invocation struct {
	Args         []string          // command-line arguments after the script path
	MaskedArgs   []string          // Args with secure values replaced by Mask, for logs and audit
	Env          map[string]string // parameter env variables plus EXECUTION_ID
	Stdin        []StdinInput
	SecureValues []string // every secure value, for masking script output
}

// Build turns normalized, validated values (keyed by parameter name) into an Invocation.
// Parameters are processed in config order; missing values count as empty.
func Build(ps []*Parameter, values map[string]Value, executionID string) Invocation {
	inv := Invocation{Env: map[string]string{}}
	collided := map[string]bool{}

	for _, p := range ps {
		v := values[p.Name]
		if p.Constant {
			v = p.Default
		}
		v = p.scriptValue(v)

		if p.Secure {
			inv.SecureValues = append(inv.SecureValues, secureStrings(p, v)...)
		}
		if p.PassAs.asArgument() {
			inv.Args = append(inv.Args, p.args(v)...)
			inv.MaskedArgs = append(inv.MaskedArgs, p.args(p.masked(v))...)
		}
		if p.PassAs.asEnv() {
			p.addEnv(inv.Env, collided, v)
		}
		if p.PassAs == PassStdin && !v.IsEmpty() && v.Kind != KindFlag {
			inv.Stdin = append(inv.Stdin, StdinInput{Value: p.joined(v), ExpectText: p.StdinExpectedText})
		}
	}

	if _, ok := inv.Env["EXECUTION_ID"]; !ok {
		inv.Env["EXECUTION_ID"] = executionID
	}
	return inv
}

// MaskValues renders values for history and audit: scalars as strings, lists as string
// slices, flags as booleans, and secure values as Mask.
func MaskValues(ps []*Parameter, values map[string]Value) map[string]any {
	out := map[string]any{}
	for _, p := range ps {
		v, ok := values[p.Name]
		if !ok || v.Kind == KindNone {
			continue
		}
		switch {
		case v.Kind == KindFlag:
			out[p.Name] = v.Flag
		case p.Secure:
			out[p.Name] = Mask
		case v.Kind == KindList:
			out[p.Name] = append([]string{}, v.List...)
		default:
			out[p.Name] = v.Str
		}
	}
	return out
}

// scriptValue maps a server_file selection to the path the script receives.
func (p *Parameter) scriptValue(v Value) Value {
	if p.Type != TypeServerFile || v.IsEmpty() {
		return v
	}
	if p.FileRecursive {
		return Scalar(filepath.Join(append([]string{p.FileDir}, v.List...)...))
	}
	return Scalar(filepath.Join(p.FileDir, v.Str))
}

// args renders one parameter as command-line arguments.
func (p *Parameter) args(v Value) []string {
	switch v.Kind {
	case KindFlag:
		if v.Flag && p.Param != "" {
			return []string{p.Param}
		}
		return nil
	case KindScalar:
		if v.Str == "" {
			return nil
		}
		return p.withParam([]string{v.Str}, ArgPerValue)
	case KindList:
		if len(v.List) == 0 {
			return nil
		}
		if p.ArgMode == ArgSingle || p.ArgMode == "" {
			return p.withParam([]string{strings.Join(v.List, p.separator())}, ArgPerValue)
		}
		return p.withParam(v.List, p.ArgMode)
	}
	return nil
}

func (p *Parameter) withParam(items []string, mode ArgMode) []string {
	if p.Param == "" {
		return append([]string(nil), items...)
	}
	var out []string
	if mode == ArgRepeatParam {
		for _, it := range items {
			if p.SameArgParam {
				out = append(out, p.Param+it)
			} else {
				out = append(out, p.Param, it)
			}
		}
		return out
	}
	if p.SameArgParam {
		return append([]string{p.Param + items[0]}, items[1:]...)
	}
	return append([]string{p.Param}, items...)
}

func (p *Parameter) separator() string {
	if p.Separator == "" {
		return ","
	}
	return p.Separator
}

// joined renders v as a single string (stdin, env).
func (p *Parameter) joined(v Value) string {
	if v.Kind == KindList {
		return strings.Join(v.List, p.separator())
	}
	return v.Str
}

// masked returns v with secure content replaced by Mask, keeping its shape so the masked
// arguments line up with the real ones.
func (p *Parameter) masked(v Value) Value {
	if !p.Secure || v.IsEmpty() || v.Kind == KindFlag {
		return v
	}
	if v.Kind == KindList && p.ArgMode != ArgSingle && p.ArgMode != "" {
		items := make([]string, len(v.List))
		for i := range items {
			items[i] = Mask
		}
		return List(items...)
	}
	return Scalar(Mask)
}

func secureStrings(p *Parameter, v Value) []string {
	switch {
	case v.IsEmpty() || v.Kind == KindFlag:
		return nil
	case v.Kind == KindList:
		return append([]string{p.joined(v)}, v.List...)
	}
	return []string{v.Str}
}

// addEnv exports v under the parameter's env name. Lists are not exported; two parameters
// claiming the same name both lose it.
func (p *Parameter) addEnv(env map[string]string, collided map[string]bool, v Value) {
	if v.Kind == KindList || v.IsEmpty() {
		return
	}
	name := p.EnvVar
	if name == "" {
		name = EnvName(p.Name)
	}
	if name == "" || collided[name] {
		return
	}
	if _, taken := env[name]; taken {
		delete(env, name)
		collided[name] = true
		return
	}
	if v.Kind == KindFlag {
		env[name] = "true"
		return
	}
	env[name] = v.Str
}

// EnvName derives the default env variable for a parameter name: PARAM_ followed by the name
// upper-cased, with every run of characters outside [A-Za-z0-9_] replaced by one underscore.
// Names with no usable characters get no variable.
func EnvName(paramName string) string {
	var b strings.Builder
	prevUnderscore := false
	usable := false
	for _, r := range paramName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
			usable = true
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	if !usable {
		return ""
	}
	return "PARAM_" + strings.ToUpper(b.String())
}
