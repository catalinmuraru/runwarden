package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/kube"
	"github.com/catalinmuraru/runwarden/internal/params"
)

// checkConfig implements `runwarden check-config <runners-dir>`: it loads every runner config,
// reports scripts, warnings and errors, and returns the process exit code.
func checkConfig(args []string, out io.Writer) int {
	return checkConfigWith(args, out, os.Getenv)
}

func checkConfigWith(args []string, out io.Writer, getenv func(string) string) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(out, "usage: runwarden check-config <runners-dir>")
		return 2
	}
	cat, err := config.LoadDir(args[0], config.Options{})
	if err != nil {
		_, _ = fmt.Fprintln(out, "error:", err)
		return 1
	}
	// Only a KUBE_DIR set on purpose is checked: the default rarely exists where check-config runs.
	kubes := kube.Store{Dir: getenv("KUBE_DIR")}
	var kubeWarnings []string
	for _, s := range cat.Scripts {
		details := []string{fmt.Sprintf("%d parameters", len(s.Parameters))}
		if s.Timeout > 0 {
			details = append(details, "timeout "+s.Timeout.String())
		}
		if len(s.Tags) > 0 {
			details = append(details, "tags "+strings.Join(s.Tags, ", "))
		}
		if len(s.Secrets) > 0 {
			details = append(details, "secrets "+strings.Join(s.Secrets, ", "))
		}
		if s.Kubernetes {
			details = append(details, "kubernetes")
			if kubes.Dir != "" {
				kubeWarnings = append(kubeWarnings, missingClusters(s, kubes)...)
			}
		}
		kubeWarnings = append(kubeWarnings, secureArguments(s)...)
		for _, name := range slices.Sorted(maps.Keys(s.EnvironmentParameters)) {
			details = append(details, fmt.Sprintf("environment from %q (%s)", name, s.EnvironmentParameters[name]))
		}
		_, _ = fmt.Fprintf(out, "ok       %s (%s)\n", s.Name, strings.Join(details, "; "))
	}
	for _, w := range cat.Warnings {
		_, _ = fmt.Fprintf(out, "warning  %s\n", w)
	}
	if strings.EqualFold(getenv("RUN_HOME"), config.RunHomeShared) && slices.ContainsFunc(cat.Scripts, func(s *config.Script) bool { return s.Kubernetes }) {
		kubeWarnings = append(kubeWarnings, `RUN_HOME=shared: runners with "kubernetes": true cannot start (they need RUN_HOME=per-run)`)
	}
	for _, w := range kubeWarnings {
		_, _ = fmt.Fprintf(out, "warning  %s\n", w)
	}
	for _, e := range cat.Errors {
		_, _ = fmt.Fprintf(out, "error    %s\n", e)
	}
	_, _ = fmt.Fprintf(out, "\n%d scripts, %d warnings, %d errors\n", len(cat.Scripts), len(cat.Warnings)+len(kubeWarnings), len(cat.Errors))
	if len(cat.Errors) > 0 {
		return 1
	}
	return 0
}

// secureArguments warns about secure parameters passed as command-line arguments: every
// concurrent run can read a process's arguments (/proc/<pid>/cmdline), even when sandboxed.
func secureArguments(s *config.Script) []string {
	var out []string
	for _, p := range s.Parameters {
		if p.Secure && (p.PassAs == params.PassDefault || p.PassAs == params.PassArgument) && !p.NoValue {
			out = append(out, fmt.Sprintf("%s: secure parameter %q is passed as an argument, which other runs can read; use \"pass_as\": \"env_variable\"", s.Name, p.Name))
		}
	}
	return out
}

// missingClusters lists the environments a script can run in (static values of its environment
// parameters) that have no kubeconfig. Values computed by a dropdown helper are not known here.
func missingClusters(s *config.Script, kubes kube.Store) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(s.EnvironmentParameters)) {
		p := s.Parameter(name)
		for _, env := range p.Values {
			if env != "" && env != "-" && !kubes.Has(env) {
				out = append(out, fmt.Sprintf("%s: environment %q (parameter %q) has no kubeconfig in %s", s.Name, env, name, kubes.Dir))
			}
		}
	}
	return out
}
