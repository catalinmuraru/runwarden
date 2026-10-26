package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/policy"
)

// maxPolicyBytes bounds an imported policy.
const maxPolicyBytes = 1 << 20

type policyScript struct {
	Name                  string            `json:"name"`
	Group                 string            `json:"group,omitempty"`
	Tags                  []string          `json:"tags,omitempty"`
	EnvironmentParameters map[string]string `json:"environment_parameters,omitempty"`
}

type policyView struct {
	Policy           *policy.Policy `json:"policy"`
	Warnings         []string       `json:"warnings"`
	MaxExceptionDays int            `json:"max_exception_days"` // 0: no limit
	Scripts          []policyScript `json:"scripts"`
	Tags             []string       `json:"tags"`
}

func (s *Server) policyStore() *policy.Store { return s.opts.Access.Store }

// policyOnly is adminOnly for routes that need the policy store.
func (s *Server) policyOnly(fn http.HandlerFunc) http.HandlerFunc {
	return s.adminOnly(func(w http.ResponseWriter, r *http.Request) {
		if s.policyStore() == nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		fn(w, r)
	})
}

func (s *Server) policyView(p *policy.Policy) policyView {
	store := s.policyStore()
	v := policyView{Policy: p, Warnings: []string{}, Scripts: []policyScript{}, Tags: []string{},
		MaxExceptionDays: store.MaxExceptionDays()}
	if c := s.catalog.Load(); c != nil {
		if w := p.Warnings(c); w != nil {
			v.Warnings = w
		}
		for _, sc := range c.Scripts {
			v.Scripts = append(v.Scripts, policyScript{Name: sc.Name, Group: sc.Group, Tags: sc.Tags, EnvironmentParameters: sc.EnvironmentParameters})
			for _, t := range sc.Tags {
				if !slices.ContainsFunc(v.Tags, func(x string) bool { return strings.EqualFold(x, t) }) {
					v.Tags = append(v.Tags, t)
				}
			}
		}
	}
	slices.SortFunc(v.Tags, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return v
}

func (s *Server) getPolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.policyView(s.policyStore().Snapshot()))
}

var errNotFound = errors.New("not found")

// badRequest is a problem with the request itself (answered with 400).
type badRequest struct{ msg string }

func (e *badRequest) Error() string { return e.msg }

// changePolicy applies change based on revision, audits it and answers with the new policy.
func (s *Server) changePolicy(w http.ResponseWriter, r *http.Request, revision int, object, what string, change func(p *policy.Policy) error) {
	_, after, err := s.policyStore().Update(revision, change)
	var verr *policy.ValidationError
	var bad *badRequest
	switch {
	case err == nil:
	case errors.Is(err, policy.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, object+" not found")
		return
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, "the change is not valid", verr.Problems...)
		return
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.msg)
		return
	default:
		slog.Error("save policy", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save the access policy")
		return
	}
	s.record(r, audit.Event{Action: audit.PolicyChange, Category: "configuration", Type: "change", Success: true,
		PolicyObject: object, PolicyChange: what, Message: fmt.Sprintf("access policy: %s: %s", object, what)})
	writeJSON(w, http.StatusOK, s.policyView(after))
}

func revisionParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("revision"))
	return n
}

func list(items []string) string { return strings.Join(items, ", ") }

func (s *Server) putEnvironments(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision           int                  `json:"revision"`
		Environments       []policy.Environment `json:"environments"`
		UnknownEnvironment string               `json:"unknown_environment"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	names := make([]string, len(body.Environments))
	for i, e := range body.Environments {
		names[i] = e.Name
	}
	s.changePolicy(w, r, body.Revision, "environments", "set to "+list(names), func(p *policy.Policy) error {
		p.Environments, p.UnknownEnvironment = body.Environments, body.UnknownEnvironment
		return nil
	})
}

type roleBody struct {
	Revision    int    `json:"revision"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) postRole(w http.ResponseWriter, r *http.Request) {
	var body roleBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	s.changePolicy(w, r, body.Revision, "role "+name, "created", func(p *policy.Policy) error {
		if p.RoleIndex(name) >= 0 {
			return &badRequest{"a role named " + name + " already exists"}
		}
		p.Roles = append(p.Roles, policy.Role{Name: name, Description: body.Description})
		return nil
	})
}

func (s *Server) putRole(w http.ResponseWriter, r *http.Request) {
	var body roleBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	old, name := r.PathValue("name"), strings.TrimSpace(body.Name)
	what := "description changed"
	if !strings.EqualFold(old, name) {
		what = "renamed to " + name
	}
	s.changePolicy(w, r, body.Revision, "role "+old, what, func(p *policy.Policy) error {
		i := p.RoleIndex(old)
		if i < 0 {
			return errNotFound
		}
		if policy.IsBuiltIn(old) && !strings.EqualFold(old, name) {
			return &badRequest{"built-in roles cannot be renamed"}
		}
		if !strings.EqualFold(old, name) && p.RoleIndex(name) >= 0 {
			return &badRequest{"a role named " + name + " already exists"}
		}
		p.Roles[i] = policy.Role{Name: name, Description: body.Description}
		renameRole(p, old, name)
		return nil
	})
}

func renameRole(p *policy.Policy, old, name string) {
	for i := range p.Rules {
		if strings.EqualFold(p.Rules[i].Role, old) {
			p.Rules[i].Role = name
		}
	}
	for i := range p.Users {
		for j, role := range p.Users[i].Roles {
			if strings.EqualFold(role, old) {
				p.Users[i].Roles[j] = name
			}
		}
	}
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.changePolicy(w, r, revisionParam(r), "role "+name, "deleted", func(p *policy.Policy) error {
		i := p.RoleIndex(name)
		switch {
		case i < 0:
			return errNotFound
		case policy.IsBuiltIn(name):
			return &badRequest{"built-in roles cannot be deleted"}
		case slices.ContainsFunc(p.Rules, func(rule policy.Rule) bool { return strings.EqualFold(rule.Role, name) }):
			return &badRequest{"the role still has rules; delete them first"}
		}
		p.Roles = slices.Delete(p.Roles, i, i+1)
		for j := range p.Users {
			p.Users[j].Roles = slices.DeleteFunc(p.Users[j].Roles, func(role string) bool { return strings.EqualFold(role, name) })
		}
		return nil
	})
}

func (s *Server) putUserRoles(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision int      `json:"revision"`
		Roles    []string `json:"roles"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	what := "roles set to " + list(body.Roles)
	if len(body.Roles) == 0 {
		what = "all roles removed (guest)"
	}
	s.changePolicy(w, r, body.Revision, "user "+name, what, func(p *policy.Policy) error {
		if name == "" || name == auth.LocalAdminName {
			return &badRequest{"this user cannot be given roles"}
		}
		p.Users = slices.DeleteFunc(p.Users, func(u policy.UserRoles) bool { return strings.EqualFold(u.Name, name) })
		p.Users = append(p.Users, policy.UserRoles{Name: name, Roles: body.Roles})
		slices.SortFunc(p.Users, func(a, b policy.UserRoles) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		return nil
	})
}

type ruleBody struct {
	Revision int `json:"revision"`
	policy.Rule
}

func describeTarget(scripts, envs []string, a policy.Access) string {
	return fmt.Sprintf("%s on %s in %s", a, list(scripts), list(envs))
}

func (s *Server) postRule(w http.ResponseWriter, r *http.Request) {
	var body ruleBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule := body.Rule
	rule.ID = policy.NewID()
	s.changePolicy(w, r, body.Revision, "rule "+rule.ID, fmt.Sprintf("created: role %s %s", rule.Role, describeTarget(rule.Scripts, rule.Environments, rule.Access)),
		func(p *policy.Policy) error {
			p.Rules = append(p.Rules, rule)
			return nil
		})
}

func (s *Server) putRule(w http.ResponseWriter, r *http.Request) {
	var body ruleBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule := body.Rule
	rule.ID = r.PathValue("id")
	s.changePolicy(w, r, body.Revision, "rule "+rule.ID, fmt.Sprintf("changed to: role %s %s", rule.Role, describeTarget(rule.Scripts, rule.Environments, rule.Access)),
		func(p *policy.Policy) error {
			i := slices.IndexFunc(p.Rules, func(x policy.Rule) bool { return x.ID == rule.ID })
			if i < 0 {
				return errNotFound
			}
			p.Rules[i] = rule
			return nil
		})
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cur, ok := s.policyStore().Snapshot().FindRule(id)
	what := "deleted"
	if ok {
		what = fmt.Sprintf("deleted: role %s %s", cur.Role, describeTarget(cur.Scripts, cur.Environments, cur.Access))
	}
	s.changePolicy(w, r, revisionParam(r), "rule "+id, what, func(p *policy.Policy) error {
		n := len(p.Rules)
		p.Rules = slices.DeleteFunc(p.Rules, func(x policy.Rule) bool { return x.ID == id })
		if len(p.Rules) == n {
			return errNotFound
		}
		return nil
	})
}

func (s *Server) postException(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision     int           `json:"revision"`
		User         string        `json:"user"`
		Scripts      []string      `json:"scripts"`
		Environments []string      `json:"environments"`
		Access       policy.Access `json:"access"`
		Reason       string        `json:"reason"`
		Expires      time.Time     `json:"expires"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	e := policy.Exception{ID: policy.NewID(), User: strings.TrimSpace(body.User), Scripts: body.Scripts, Environments: body.Environments,
		Access: body.Access, Reason: strings.TrimSpace(body.Reason), Expires: body.Expires.UTC(), CreatedBy: currentUser(r).Name, Created: now}
	if !e.Expires.After(now) {
		writeError(w, http.StatusBadRequest, "the expiry must be in the future")
		return
	}
	s.changePolicy(w, r, body.Revision, "exception "+e.ID,
		fmt.Sprintf("granted to %s: %s until %s; reason: %s", e.User, describeTarget(e.Scripts, e.Environments, e.Access), e.Expires.Format(time.RFC3339), e.Reason),
		func(p *policy.Policy) error {
			p.Exceptions = append(p.Exceptions, e)
			return nil
		})
}

// deleteException ends an exception now. It stays listed as expired for a while.
func (s *Server) deleteException(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	now := time.Now().UTC()
	s.changePolicy(w, r, revisionParam(r), "exception "+id, "ended early", func(p *policy.Policy) error {
		i := slices.IndexFunc(p.Exceptions, func(e policy.Exception) bool { return e.ID == id })
		if i < 0 {
			return errNotFound
		}
		e := &p.Exceptions[i]
		if !e.Active(now) {
			return &badRequest{"the exception has already ended"}
		}
		// The end is audited here, not again by the expiry sweep.
		e.Expires, e.ExpiryAudited = now, true
		if !e.Expires.After(e.Created) {
			e.Created = now.Add(-time.Second)
		}
		return nil
	})
}

type effectiveScript struct {
	Name   string                  `json:"name"`
	Group  string                  `json:"group,omitempty"`
	Tags   []string                `json:"tags,omitempty"`
	Access map[string]policy.Grant `json:"access"` // per environment; missing: no access
}

// effectiveAccess shows the access of a user or a role: every script × environment.
func (s *Server) effectiveAccess(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var sub policy.Subject
	switch {
	case q.Get("user") != "":
		sub = s.opts.Access.Subject(authUser(activity.User{Name: q.Get("user")}))
	case q.Get("role") != "":
		sub = policy.Subject{User: "\x00role", Roles: []string{q.Get("role")}} // no user matches: rules only
	default:
		writeError(w, http.StatusBadRequest, "give user or role")
		return
	}
	p, now := s.policyStore().Snapshot(), time.Now()
	envs := append(p.EnvironmentNames(), policy.NoEnvironment)
	out := []effectiveScript{}
	if c := s.catalog.Load(); c != nil {
		for _, sc := range c.Scripts {
			es := effectiveScript{Name: sc.Name, Group: sc.Group, Tags: sc.Tags, Access: map[string]policy.Grant{}}
			scriptEnvs := []string{policy.NoEnvironment}
			if len(sc.EnvironmentParameters) > 0 {
				scriptEnvs = p.EnvironmentNames()
			}
			for _, env := range scriptEnvs {
				if g := p.Access(sub, sc, env, now); g.Access > policy.None {
					es.Access[env] = g
				}
			}
			out = append(out, es)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": nonNil(sub.Roles), "environments": envs, "scripts": out})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) exportPolicy(w http.ResponseWriter, r *http.Request) {
	data, err := policy.ExportYAML(s.policyStore().Snapshot())
	if err != nil {
		slog.Error("export policy", "err", err)
		writeError(w, http.StatusInternalServerError, "could not export the access policy")
		return
	}
	s.record(r, audit.Event{Action: audit.PolicyChange, Category: "configuration", Type: "info", Success: true,
		PolicyObject: "policy", PolicyChange: "exported", Message: "access policy exported"})
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="runwarden-policy.yaml"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// importPolicy replaces the whole policy with an exported one; with dry_run=true it only
// validates it and says what would change.
func (s *Server) importPolicy(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPolicyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "the policy file is too large")
		return
	}
	imported, err := policy.ImportYAML(data, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	store := s.policyStore()
	if err := imported.Validate(0); err != nil {
		var verr *policy.ValidationError
		if errors.As(err, &verr) {
			writeError(w, http.StatusBadRequest, "the imported policy is not valid", verr.Problems...)
			return
		}
	}
	sum := policy.Summarize(store.Snapshot(), imported)
	if r.URL.Query().Get("dry_run") == "true" {
		writeJSON(w, http.StatusOK, map[string]any{"summary": sum, "warnings": nonNil(imported.Warnings(s.catalog.Load()))})
		return
	}
	s.changePolicy(w, r, revisionParam(r), "policy",
		fmt.Sprintf("imported: %d environments, %d roles, %d users, %d rules, %d exceptions", sum.Environments, sum.Roles, sum.Users, sum.Rules, sum.Exceptions),
		func(p *policy.Policy) error {
			rev := p.Revision
			*p = *imported
			p.Revision = rev
			return nil
		})
}
