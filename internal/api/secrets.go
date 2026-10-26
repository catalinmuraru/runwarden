package api

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/policy"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

// resolveSecrets returns the declared secrets of sc for the environments in uses: targets
// unprefixed, sources as SOURCE_<NAME>.
func (s *Server) resolveSecrets(sc *config.Script, uses []authz.EnvUse) (secrets.Result, error) {
	var targets, sources []string
	for _, u := range uses {
		if u.Use == config.EnvSource {
			sources = append(sources, u.Environment)
		} else {
			targets = append(targets, u.Environment)
		}
	}
	return s.opts.Secrets.Resolve(sc.Secrets, targets, sources)
}

// helperSecrets returns the secrets a helper of sc gets: those of the environments selected by
// raw values (or the parameters' defaults).
func (s *Server) helperSecrets(sc *config.Script, raw func(string) (any, bool)) (map[string]string, error) {
	if len(sc.Secrets) == 0 {
		return nil, nil
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
		return nil, err
	}
	return res.Env, nil
}

// secretRefusal tells whether err is a deployment problem the user should see (missing secret,
// ambiguous environments) rather than an internal error.
func secretRefusal(err error) bool {
	var missing *secrets.MissingError
	return errors.As(err, &missing) || errors.Is(err, secrets.ErrSeveralTargets)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":   s.opts.Secrets.Dir != "",
		"environments": envs,
		"available":    inv,
		"unknown_dirs": nonNil(unknown),
		"scripts":      out,
	})
}
