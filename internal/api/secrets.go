package api

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/kube"
	"github.com/catalinmuraru/runwarden/internal/policy"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

// splitUses returns the target and the source environments of uses.
func splitUses(uses []authz.EnvUse) (targets, sources []string) {
	for _, u := range uses {
		if u.Use == config.EnvSource {
			sources = append(sources, u.Environment)
		} else {
			targets = append(targets, u.Environment)
		}
	}
	return targets, sources
}

// resolveSecrets returns the declared secrets of sc for the environments in uses: targets
// unprefixed, sources as SOURCE_<NAME>.
func (s *Server) resolveSecrets(sc *config.Script, uses []authz.EnvUse) (secrets.Result, error) {
	targets, sources := splitUses(uses)
	return s.opts.Secrets.Resolve(sc.Secrets, targets, sources)
}

// helperUses returns the environments a helper of sc works on: those selected by raw values (or
// the parameters' defaults).
func (s *Server) helperUses(sc *config.Script, raw func(string) (any, bool)) []authz.EnvUse {
	return s.opts.Access.Uses(sc, func(name string) []string {
		p := sc.Parameter(name)
		if p == nil {
			return nil
		}
		v := p.Default
		if rv, ok := raw(name); ok {
			nv, err := p.Normalize(rv)
			if err != nil {
				return nil
			}
			v = nv
		}
		return valueItems(v)
	})
}

// helperEnv returns the secrets and the kubeconfig a helper of sc gets for uses (see
// helperUses). A helper called before an environment is chosen (such as the one listing the
// environments) gets no kubeconfig.
func (s *Server) helperEnv(sc *config.Script, uses []authz.EnvUse) (map[string]string, *executor.Kube, error) {
	if len(sc.Secrets) == 0 && !sc.Kubernetes {
		return nil, nil, nil
	}
	res, err := s.resolveSecrets(sc, uses)
	if err != nil {
		return nil, nil, err
	}
	kc, _, err := s.resolveKube(sc, uses)
	if errors.Is(err, kube.ErrNoEnvironment) {
		return res.Env, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return res.Env, kc, nil
}

// secretRefusal tells whether err is a deployment problem the user should see (missing secret
// or cluster, ambiguous environments) rather than an internal error.
func secretRefusal(err error) bool {
	var missing *secrets.MissingError
	var noCluster *kube.MissingError
	var refused *startError
	return errors.As(err, &missing) || errors.Is(err, secrets.ErrSeveralTargets) ||
		errors.As(err, &noCluster) || errors.Is(err, kube.ErrSeveralTargets) || errors.Is(err, kube.ErrNoEnvironment) ||
		errors.Is(err, errExecutorScope) || errors.As(err, &refused)
}

type secretsScript struct {
	Name    string              `json:"name"`
	Secrets []string            `json:"secrets"`
	Missing map[string][]string `json:"missing"` // environment → declared names not available there
	Envless bool                `json:"envless"` // no environment parameter: only common applies
}

// adminSecrets lists secret names per environment and what each script declares and misses.
// Values are never sent.
func (s *Server) adminSecrets(w http.ResponseWriter, _ *http.Request) {
	inv, err := s.opts.Secrets.Inventory()
	if err != nil {
		slog.Error("secrets inventory", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read the secrets directory")
		return
	}
	if inv == nil {
		inv = map[string][]string{}
	}
	// Executor environments: the secrets their executors report (none mounted here). Without a
	// connected executor, what they hold is unknown, so nothing is reported missing there.
	execs := s.executorOverview()
	reported := map[string][]string{} // executor environment → its names, including its common ones
	for _, x := range execs {
		delete(inv, x.Environment)
		if len(x.Pods) == 0 {
			continue
		}
		reported[x.Environment] = []string{}
		for _, info := range x.Secrets {
			reported[x.Environment] = append(reported[x.Environment], info.Name)
			if info.Environment == x.Environment {
				inv[x.Environment] = append(inv[x.Environment], info.Name)
			}
		}
	}
	has := func(env, name string) bool {
		if s.opts.Executors != nil && s.opts.Executors.Remote(env) {
			names, ok := reported[env]
			return !ok || slices.Contains(names, name)
		}
		return slices.Contains(inv[env], name) || slices.Contains(inv[secrets.Common], name)
	}
	envs := s.opts.Access.Policy().EnvironmentNames()
	out := []secretsScript{}
	if c := s.catalog.Load(); c != nil {
		for _, sc := range c.Scripts {
			if len(sc.Secrets) == 0 {
				continue
			}
			row := secretsScript{Name: sc.Name, Secrets: sc.Secrets, Missing: map[string][]string{}, Envless: len(sc.EnvironmentParameters) == 0}
			check := []string{policy.NoEnvironment}
			if !row.Envless {
				check = envs
			}
			for _, env := range check {
				for _, name := range sc.Secrets {
					if !has(env, name) {
						row.Missing[env] = append(row.Missing[env], name)
					}
				}
			}
			out = append(out, row)
		}
	}
	// Directories that are no environment of the policy (typos, renamed environments).
	var unknown []string
	for dir := range inv {
		if dir != secrets.Common && !slices.Contains(envs, dir) {
			unknown = append(unknown, dir)
		}
	}
	slices.Sort(unknown)
	kc, err := s.kubeOverview(envs, execs)
	if err != nil {
		slog.Error("kubeconfig inventory", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read the kubeconfig directory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":   s.opts.Secrets.Dir != "",
		"environments": envs,
		"available":    inv,
		"unknown_dirs": nonNil(unknown),
		"scripts":      out,
		"kubernetes":   kc,
		"isolation":    s.opts.Isolation,
		"executors":    execs,
	})
}

type kubeCluster struct {
	Environment string `json:"environment"`
	Context     string `json:"context,omitempty"`
	Server      string `json:"server,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Error       string `json:"error,omitempty"` // why the kubeconfig cannot be used
}

type kubeScript struct {
	Name    string   `json:"name"`
	Missing []string `json:"missing"` // environments without a usable kubeconfig
}

type kubeOverview struct {
	Configured  bool          `json:"configured"` // KUBE_DIR set
	Clusters    []kubeCluster `json:"clusters"`
	UnknownDirs []string      `json:"unknown_dirs"` // directories that are no environment of the policy
	Scripts     []kubeScript  `json:"scripts"`      // runners with "kubernetes": true
}

// kubeOverview lists the cluster of each environment's kubeconfig (server, never credentials)
// and the environments where a "kubernetes" runner cannot start. Executor environments show the
// cluster their first executor reports; without a connected executor they are not listed.
func (s *Server) kubeOverview(envs []string, execs []executorEnvironment) (kubeOverview, error) {
	inv, err := s.opts.Kube.Inventory()
	if err != nil {
		return kubeOverview{}, err
	}
	out := kubeOverview{Configured: s.opts.Kube.Dir != "", Clusters: []kubeCluster{}, UnknownDirs: []string{}, Scripts: []kubeScript{}}
	usable := map[string]bool{}
	for _, x := range execs {
		if len(x.Pods) == 0 {
			usable[x.Environment] = true // unknown: not reported missing
		} else if x.cluster != nil {
			inv = append(inv, *x.cluster)
		}
	}
	for _, st := range inv {
		out.Clusters = append(out.Clusters, kubeCluster{Environment: st.Environment, Context: st.Context,
			Server: st.Server, Namespace: st.Namespace, Error: st.Error})
		usable[st.Environment] = st.Error == ""
		if !slices.Contains(envs, st.Environment) {
			out.UnknownDirs = append(out.UnknownDirs, st.Environment)
		}
	}
	if c := s.catalog.Load(); c != nil {
		for _, sc := range c.Scripts {
			if !sc.Kubernetes {
				continue
			}
			row := kubeScript{Name: sc.Name, Missing: []string{}}
			for _, env := range envs {
				if !usable[env] {
					row.Missing = append(row.Missing, env)
				}
			}
			out.Scripts = append(out.Scripts, row)
		}
	}
	return out, nil
}
