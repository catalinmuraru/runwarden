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

// helperEnv returns the secrets and the kubeconfig a helper of sc gets: those of the
// environments selected by raw values (or the parameters' defaults). A helper called before an
// environment is chosen (such as the one listing the environments) gets no kubeconfig.
func (s *Server) helperEnv(sc *config.Script, raw func(string) (any, bool)) (map[string]string, *executor.Kube, error) {
	if len(sc.Secrets) == 0 && !sc.Kubernetes {
		return nil, nil, nil
	}
	uses := s.opts.Access.Uses(sc, func(name string) []string {
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
	return errors.As(err, &missing) || errors.Is(err, secrets.ErrSeveralTargets) ||
		errors.As(err, &noCluster) || errors.Is(err, kube.ErrSeveralTargets) || errors.Is(err, kube.ErrNoEnvironment)
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
					if !slices.Contains(inv[env], name) && !slices.Contains(inv[secrets.Common], name) {
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
	if inv == nil {
		inv = map[string][]string{}
	}
	kc, err := s.kubeOverview(envs)
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
// and the environments where a "kubernetes" runner cannot start.
func (s *Server) kubeOverview(envs []string) (kubeOverview, error) {
	inv, err := s.opts.Kube.Inventory()
	if err != nil {
		return kubeOverview{}, err
	}
	out := kubeOverview{Configured: s.opts.Kube.Dir != "", Clusters: []kubeCluster{}, UnknownDirs: []string{}, Scripts: []kubeScript{}}
	usable := map[string]bool{}
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
