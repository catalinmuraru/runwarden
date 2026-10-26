//go:build unix

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/executor"
)

func TestEnvironmentAccess(t *testing.T) {
	alice := setup(t, executor.Limits{}, Options{})
	bob, carol, erin, dave := alice.as("bob", ""), alice.as("carol", ""), alice.as("erin", ""), alice.as("dave", "")

	// The script page tells each user where they may run and view.
	_, body := bob.do(http.MethodGet, "/api/scripts/Echo", nil)
	var view scriptView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Access["development"] != "run" || view.Access["production"] != "view" {
		t.Errorf("bob's access = %v", view.Access)
	}
	_, body = erin.do(http.MethodGet, "/api/scripts/Echo", nil)
	if strings.Contains(string(body), `"prod"`) || !strings.Contains(string(body), `"environment":"development","access":"run"`) {
		t.Errorf("erin sees: %s", body)
	}

	// Guests see nothing.
	if _, body := dave.do(http.MethodGet, "/api/scripts", nil); string(body) != `{"scripts":[]}`+"\n" {
		t.Errorf("guest's scripts: %s", body)
	}
	if code, _ := dave.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values?Env=dev", nil); code != http.StatusNotFound {
		t.Errorf("guest values = %d", code)
	}
	if _, body := carol.do(http.MethodGet, "/api/scripts", nil); !strings.Contains(string(body), `"tags":["ops"]`) || strings.Contains(string(body), "Echo") {
		t.Errorf("carol's scripts: %s", body)
	}

	// Dropdowns computed from an environment need view access there.
	if code, body := erin.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values?Env=prod", nil); code != http.StatusForbidden || !strings.Contains(string(body), "production") {
		t.Errorf("erin's production targets = %d %s", code, body)
	}
	if code, body := bob.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values?Env=prod", nil); code != http.StatusOK || !strings.Contains(string(body), "t-prod-1") {
		t.Errorf("bob's production targets = %d %s", code, body)
	}
	if code, body := erin.do(http.MethodGet, "/api/scripts/Echo/parameters/Env/values", nil); code != http.StatusOK || strings.Contains(string(body), "prod") {
		t.Errorf("erin's environments = %d %s", code, body)
	}

	// Starting needs run access to the selected environment.
	start := func(e *testEnv, env string) (int, string) {
		code, body := e.do(http.MethodPost, "/api/scripts/Echo/runs", map[string]any{"values": map[string]any{"Name": "abc", "Env": env}})
		return code, string(body)
	}
	if code, body := start(bob, "prod"); code != http.StatusForbidden || !strings.Contains(body, "view-only access to environment production") {
		t.Errorf("bob on production = %d %s", code, body)
	}
	if code, body := start(erin, "prod"); code != http.StatusForbidden || !strings.Contains(body, "no access to environment production") {
		t.Errorf("erin on production = %d %s", code, body)
	}
	if code, _ := start(bob, "dev"); code != http.StatusCreated {
		t.Errorf("bob on development = %d", code)
	}

	// alice's production run: bob may view it (not control it), erin may not see it.
	prod := alice.start("Echo", map[string]any{"Name": "abc", "Env": "prod"})
	meta := alice.finish(prod)
	if len(meta.Environments) != 1 || meta.Environments[0] != "production" {
		t.Errorf("environments = %v", meta.Environments)
	}
	code, body := bob.do(http.MethodGet, "/api/runs/"+prod, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"can_control":false`) {
		t.Errorf("bob GET production run = %d %s", code, body)
	}
	if _, body := alice.do(http.MethodGet, "/api/runs/"+prod, nil); !strings.Contains(string(body), `"can_control":true`) {
		t.Errorf("alice GET own run: %s", body)
	}
	if code, _ := erin.do(http.MethodGet, "/api/runs/"+prod, nil); code != http.StatusNotFound {
		t.Errorf("erin GET production run = %d", code)
	}

	// A run recorded before environments existed is placed from its values.
	old := alice.start("Echo", map[string]any{"Name": "abc", "Env": "prod"})
	alice.finish(old)
	oldMeta, err := alice.hist.Get(old)
	if err != nil {
		t.Fatal(err)
	}
	oldMeta.Environments = nil
	if envs := alice.srv.runEnvironments(oldMeta, alice.script("Echo")); len(envs) != 1 || envs[0] != "production" {
		t.Errorf("derived environments = %v", envs)
	}
}

func TestViewerCannotStartOrControl(t *testing.T) {
	alice := setup(t, executor.Limits{KillGrace: 200 * time.Millisecond}, Options{})
	carol := alice.as("carol", "") // auditor: views tag:ops scripts

	if code, body := carol.do(http.MethodPost, "/api/scripts/Restricted/runs", map[string]any{}); code != http.StatusForbidden || !strings.Contains(string(body), "view-only access to this script") {
		t.Errorf("carol starts Restricted = %d %s", code, body)
	}
	// carol views only tag:ops scripts: other runs stay hidden.
	sleep := alice.start("Sleep", nil)
	if code, _ := carol.do(http.MethodGet, "/api/runs/"+sleep, nil); code != http.StatusNotFound {
		t.Errorf("carol sees a Sleep run = %d", code)
	}
	alice.do(http.MethodPost, "/api/runs/"+sleep+"/kill", nil)
	alice.finish(sleep)
}

// policyEnv is a test server with the activity store, which the Admin routes need.
func policyEnv(t *testing.T) *testEnv {
	t.Helper()
	acts, err := activity.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return setup(t, executor.Limits{}, Options{Activity: acts})
}

func (e *testEnv) policy() policyView {
	e.t.Helper()
	code, body := e.do(http.MethodGet, "/api/admin/policy", nil)
	if code != http.StatusOK {
		e.t.Fatalf("GET policy = %d %s", code, body)
	}
	var v policyView
	if err := json.Unmarshal(body, &v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

func TestPolicyAdmin(t *testing.T) {
	var (
		mu     sync.Mutex
		events []audit.Record
	)
	audit.SetSink(func(r audit.Record) { mu.Lock(); events = append(events, r); mu.Unlock() })
	t.Cleanup(func() { audit.SetSink(nil) })

	alice := policyEnv(t)
	root, dave := alice.as("root", ""), alice.as("dave", "")

	if code, _ := alice.do(http.MethodGet, "/api/admin/policy", nil); code != http.StatusNotFound {
		t.Errorf("non-admin GET policy = %d", code)
	}
	v := root.policy()
	if len(v.Policy.Environments) != 2 || v.MaxExceptionDays != 30 || len(v.Tags) != 1 || v.Tags[0] != "ops" {
		t.Errorf("policy view = %+v", v)
	}
	rev := v.Policy.Revision

	// dave is a guest until given a role.
	code, body := root.do(http.MethodPut, "/api/admin/policy/users/dave", map[string]any{"revision": rev, "roles": []string{"intern"}})
	if code != http.StatusOK {
		t.Fatalf("set roles = %d %s", code, body)
	}
	if _, body := dave.do(http.MethodGet, "/api/scripts", nil); !strings.Contains(string(body), "Echo") {
		t.Errorf("dave after the role: %s", body)
	}
	if code, _ := root.do(http.MethodPut, "/api/admin/policy/users/dave", map[string]any{"revision": rev, "roles": []string{}}); code != http.StatusConflict {
		t.Errorf("stale revision = %d, want 409", code)
	}
	rev++

	// Invalid rules are refused with the problems.
	code, body = root.do(http.MethodPost, "/api/admin/policy/rules", map[string]any{"revision": rev, "role": "intern", "scripts": []string{"Echo"}, "environments": []string{"staging"}, "access": "run"})
	if code != http.StatusBadRequest || !strings.Contains(string(body), `unknown environment \"staging\"`) {
		t.Errorf("invalid rule = %d %s", code, body)
	}
	code, body = root.do(http.MethodPost, "/api/admin/policy/rules", map[string]any{"revision": rev, "role": "intern", "scripts": []string{"Typo"}, "environments": []string{"production"}, "access": "view"})
	if code != http.StatusOK || !strings.Contains(string(body), `\"Typo\" matches no script`) {
		t.Errorf("rule with warning = %d %s", code, body)
	}
	rev++

	// An exception gives dave run on production for a while; ending it takes it away.
	expires := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	code, body = root.do(http.MethodPost, "/api/admin/policy/exceptions", map[string]any{"revision": rev, "user": "dave",
		"scripts": []string{"Echo"}, "environments": []string{"production"}, "access": "run", "reason": "incident 42", "expires": expires})
	if code != http.StatusOK {
		t.Fatalf("exception = %d %s", code, body)
	}
	rev++
	var after policyView
	_ = json.Unmarshal(body, &after)
	exc := after.Policy.Exceptions[0]
	if exc.CreatedBy != "root" || exc.Reason != "incident 42" {
		t.Errorf("exception = %+v", exc)
	}
	code, body = root.do(http.MethodGet, "/api/admin/policy/effective?user=dave", nil)
	if code != http.StatusOK || !strings.Contains(string(body), "exception "+exc.ID) {
		t.Errorf("effective = %d %s", code, body)
	}
	if code, _ := dave.do(http.MethodPost, "/api/scripts/Echo/runs", map[string]any{"values": map[string]any{"Name": "abc", "Env": "prod"}}); code != http.StatusCreated {
		t.Errorf("dave on production with the exception = %d", code)
	}
	tooLong := time.Now().Add(40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if code, _ := root.do(http.MethodPost, "/api/admin/policy/exceptions", map[string]any{"revision": rev, "user": "dave",
		"scripts": []string{"Echo"}, "environments": []string{"production"}, "access": "run", "reason": "x", "expires": tooLong}); code != http.StatusBadRequest {
		t.Errorf("too long exception = %d", code)
	}
	if code, body := root.do(http.MethodDelete, "/api/admin/policy/exceptions/"+exc.ID+"?revision="+strconv.Itoa(rev), nil); code != http.StatusOK {
		t.Fatalf("end exception = %d %s", code, body)
	}
	rev++
	if code, _ := dave.do(http.MethodPost, "/api/scripts/Echo/runs", map[string]any{"values": map[string]any{"Name": "abc", "Env": "prod"}}); code != http.StatusForbidden {
		t.Errorf("dave on production after the end = %d", code)
	}

	// Roles: built-ins stay; a role with rules cannot be deleted.
	if code, _ := root.do(http.MethodDelete, "/api/admin/policy/roles/developer?revision="+strconv.Itoa(rev), nil); code != http.StatusBadRequest {
		t.Errorf("delete built-in = %d", code)
	}
	if code, _ := root.do(http.MethodDelete, "/api/admin/policy/roles/intern?revision="+strconv.Itoa(rev), nil); code != http.StatusBadRequest {
		t.Errorf("delete role with rules = %d", code)
	}
	if code, body := root.do(http.MethodPut, "/api/admin/policy/roles/intern", map[string]any{"revision": rev, "name": "trainee"}); code != http.StatusOK ||
		!strings.Contains(string(body), `"role":"trainee"`) || !strings.Contains(string(body), `"roles":["trainee"]`) {
		t.Errorf("rename role = %d %s", code, body)
	}
	rev++

	// Export, then import it back: a dry run first reports what changes.
	code, yaml := root.do(http.MethodGet, "/api/admin/policy/export", nil)
	if code != http.StatusOK || !bytes.Contains(yaml, []byte("role: trainee")) {
		t.Fatalf("export = %d %s", code, yaml)
	}
	code, body = root.do(http.MethodPost, "/api/admin/policy/import?dry_run=true", bytes.NewReader([]byte("environments: []\n")), "Content-Type", "application/yaml")
	if code != http.StatusOK || !strings.Contains(string(body), `"removed_rules":6`) {
		t.Errorf("dry run = %d %s", code, body)
	}
	if code, body := root.do(http.MethodPost, "/api/admin/policy/import?revision="+strconv.Itoa(rev), bytes.NewReader(yaml), "Content-Type", "application/yaml"); code != http.StatusOK {
		t.Errorf("import = %d %s", code, body)
	}
	if code, _ := root.do(http.MethodPost, "/api/admin/policy/import?dry_run=true", bytes.NewReader([]byte("rulez: 1\n")), "Content-Type", "application/yaml"); code != http.StatusBadRequest {
		t.Errorf("bad import = %d", code)
	}

	mu.Lock()
	seen := append([]audit.Record(nil), events...)
	mu.Unlock()
	changes := 0
	for _, e := range seen {
		if e.Action == audit.PolicyChange && e.User == "root" {
			changes++
		}
	}
	if changes < 7 {
		t.Errorf("policy-change events = %d", changes)
	}

	// The users page lists pre-assigned users and roles.
	_, body = root.do(http.MethodGet, "/api/admin/users", nil)
	var users struct{ Users []adminUser }
	_ = json.Unmarshal(body, &users)
	found := map[string]adminUser{}
	for _, u := range users.Users {
		found[u.Name] = u
	}
	if u := found["erin"]; u.Status != statusNever || len(u.Roles) != 1 {
		t.Errorf("erin = %+v", u)
	}
	if u := found["root"]; !u.Admin {
		t.Errorf("root = %+v", u)
	}
}
