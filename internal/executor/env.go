package executor

import (
	"os"
	"slices"
	"strings"
)

// DefaultPath is the PATH scripts get when Ward's own environment has none.
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// EnvPolicy decides which variables a script receives. Scripts never inherit Ward's
// environment as a whole, so application secrets cannot reach them.
type EnvPolicy struct {
	// Allow lists variables copied from Ward's environment when set.
	// Nil means PATH, HOME, LANG and TZ.
	Allow []string
	// Prefix exposes Ward variables to scripts with the prefix stripped:
	// RUNNER_ENV_FOO=x reaches scripts as FOO=x. Empty means "RUNNER_ENV_".
	Prefix string
	// TempRoot, if set, gets a private directory per run (and per helper call), removed when it
	// ends. Its tmp/ is exported as TMPDIR.
	TempRoot string
	// PrivateHome gives every run its own HOME inside that directory, with KUBECONFIG and
	// AZURE_CONFIG_DIR in it, instead of Ward's HOME. Needs TempRoot.
	PrivateHome bool
	// HomeSkeleton, if set, is copied into every private HOME (e.g. .gitconfig, .ssh/known_hosts).
	HomeSkeleton string
	// Workspace, if set, holds the per-user workspaces: a run gets WARD_WORKSPACE=
	// <Workspace>/<user> (created 0700; with a Sandbox only that user's runs can reach it) and
	// WARD_SHARED=<Workspace>/shared (read-only with a Sandbox).
	Workspace string
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

// build returns the script environment: allowlisted and prefixed variables from Ward's
// environment, then parameter variables, then the run's secrets, then fixed variables set by
// Ward itself. Parameter variables that could change how the script or its interpreter
// runs, or spoof a secret or a fixed variable, are refused and returned by name.
func (p EnvPolicy) build(paramEnv, secrets, fixed map[string]string) (env []string, refused []string) {
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
		_, isFixed := fixed[name]
		_, isSecret := secrets[name]
		if isFixed || isSecret || deniedEnv(name) {
			refused = append(refused, name)
			continue
		}
		vars[name] = value
	}
	for name, value := range secrets {
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
	case "PATH", "HOME", "IFS", "ENV", "BASH_ENV", "SHELLOPTS", "BASHOPTS", "PS4", "PROMPT_COMMAND", "WARD_USER", "TMPDIR", "TERM",
		"KUBECONFIG", "AZURE_CONFIG_DIR", "WARD_WORKSPACE", "WARD_SHARED", "WARD_SANDBOX", "":
		return true
	}
	return strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "BASH_FUNC_")
}
