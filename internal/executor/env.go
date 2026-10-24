package executor

import (
	"os"
	"slices"
	"strings"
)

// DefaultPath is the PATH scripts get when Runwarden's own environment has none.
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// EnvPolicy decides which variables a script receives. Scripts never inherit Runwarden's
// environment as a whole, so application secrets cannot reach them.
type EnvPolicy struct {
	// Allow lists variables copied from Runwarden's environment when set.
	// Nil means PATH, HOME, LANG and TZ.
	Allow []string
	// Prefix exposes Runwarden variables to scripts with the prefix stripped:
	// RUNNER_ENV_FOO=x reaches scripts as FOO=x. Empty means "RUNNER_ENV_".
	Prefix string
}

func (p EnvPolicy) withDefaults() EnvPolicy {
	if p.Allow == nil {
		p.Allow = []string{"PATH", "HOME", "LANG", "TZ"}
	}
	if p.Prefix == "" {
		p.Prefix = "RUNNER_ENV_"
	}
	return p
}

// build returns the script environment: allowlisted and prefixed variables from Runwarden's
// environment, then parameter variables, then fixed variables set by Runwarden itself.
// Parameter variables that could change how the script or its interpreter runs, or spoof a
// fixed variable, are refused and returned by name.
func (p EnvPolicy) build(paramEnv, fixed map[string]string) (env []string, refused []string) {
	vars := map[string]string{}
	for _, name := range p.Allow {
		if v, ok := os.LookupEnv(name); ok {
			vars[name] = v
		}
	}
	if vars["PATH"] == "" {
		vars["PATH"] = DefaultPath
	}
	if vars["LANG"] == "" {
		vars["LANG"] = "C.UTF-8"
	}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if rest, ok := strings.CutPrefix(name, p.Prefix); ok && rest != "" {
			vars[rest] = value
		}
	}
	for name, value := range paramEnv {
		if _, isFixed := fixed[name]; isFixed || deniedEnv(name) {
			refused = append(refused, name)
			continue
		}
		vars[name] = value
	}
	for name, value := range fixed {
		vars[name] = value
	}

	for name, value := range vars {
		env = append(env, name+"="+value)
	}
	slices.Sort(env)
	slices.Sort(refused)
	return env, refused
}

// deniedEnv reports variables a parameter must never set: they control the dynamic loader,
// the shell or the search path, so a user-supplied value could run arbitrary code.
func deniedEnv(name string) bool {
	switch name {
	case "PATH", "HOME", "IFS", "ENV", "BASH_ENV", "SHELLOPTS", "BASHOPTS", "PS4", "PROMPT_COMMAND", "RUNWARDEN_USER", "":
		return true
	}
	return strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "BASH_FUNC_")
}
