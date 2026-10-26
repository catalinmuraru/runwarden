package kube

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// write creates dir/env/config with content.
func write(t *testing.T, dir, env, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, env), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, env, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// aks is what `az aks get-credentials` + `kubelogin convert-kubeconfig -l spn` produce.
func aks(cluster, server, namespace string) string {
	ns := ""
	if namespace != "" {
		ns = "\n    namespace: " + namespace
	}
	return `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTg==
    server: ` + server + `
  name: ` + cluster + `
contexts:
- context:
    cluster: ` + cluster + `
    user: clusterUser_rg_` + cluster + ns + `
  name: ` + cluster + `
current-context: ` + cluster + `
kind: Config
preferences: {}
users:
- name: clusterUser_rg_` + cluster + `
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      args:
      - get-token
      - --login
      - spn
      command: kubelogin
      env: null
      provideClusterInfo: false
`
}

// decoded is the part of a resolved kubeconfig the tests look at.
type decoded struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Clusters []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Exec struct {
				Command            string   `yaml:"command"`
				Args               []string `yaml:"args"`
				ProvideClusterInfo *bool    `yaml:"provideClusterInfo"`
			} `yaml:"exec"`
		} `yaml:"user"`
	} `yaml:"users"`
}

func decode(t *testing.T, data []byte) decoded {
	t.Helper()
	var d decoded
	if err := yaml.Unmarshal(data, &d); err != nil {
		t.Fatalf("resolved kubeconfig does not parse: %v\n%s", err, data)
	}
	return d
}

func setup(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "development", aks("aks-dev", "https://dev.example:443", ""))
	write(t, dir, "production", aks("aks-prod", "https://prod.example:443", ""))
	// Two environments on one cluster, told apart by the namespace of their contexts.
	write(t, dir, "devops", aks("aks-shared", "https://shared.example:443", "devops"))
	write(t, dir, "test", aks("aks-shared", "https://shared.example:443", "test"))
	// Kubernetes Secret layout: config is a symlink into a timestamped directory.
	data := filepath.Join(dir, "acceptance", "..2026_10_26")
	write(t, filepath.Join(dir, "acceptance"), "..2026_10_26", aks("aks-acc", "https://acc.example:443", ""))
	if err := os.Symlink(data, filepath.Join(dir, "acceptance", "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", FileName), filepath.Join(dir, "acceptance", FileName)); err != nil {
		t.Fatal(err)
	}
	return Store{Dir: dir}
}

func TestResolveTarget(t *testing.T) {
	s := setup(t)
	for env, server := range map[string]string{
		"development": "https://dev.example:443",
		"production":  "https://prod.example:443",
		"acceptance":  "https://acc.example:443",
	} {
		res, err := s.Resolve([]string{env}, nil)
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		d := decode(t, res.Kubeconfig)
		if d.CurrentContext != env || len(d.Contexts) != 1 || d.Contexts[0].Name != env ||
			d.Contexts[0].Context.Cluster != env || d.Contexts[0].Context.User != env {
			t.Errorf("%s: contexts = %+v, current %q", env, d.Contexts, d.CurrentContext)
		}
		if len(d.Clusters) != 1 || d.Clusters[0].Cluster.Server != server {
			t.Errorf("%s: clusters = %+v", env, d.Clusters)
		}
		if len(d.Users) != 1 || d.Users[0].User.Exec.Command != "kubelogin" || len(d.Users[0].User.Exec.Args) != 3 ||
			d.Users[0].User.Exec.ProvideClusterInfo == nil || *d.Users[0].User.Exec.ProvideClusterInfo {
			t.Errorf("%s: users = %+v (exec must be kept as is)", env, d.Users)
		}
		if res.Env[ContextVar] != env || len(res.Env) != 1 {
			t.Errorf("%s: env = %v", env, res.Env)
		}
		if len(res.Clusters) != 1 || res.Clusters[0].Server != server || res.Clusters[0].Environment != env {
			t.Errorf("%s: clusters info = %+v", env, res.Clusters)
		}
		if strings.Contains(string(res.Kubeconfig), "aks-") {
			t.Errorf("%s: original names leaked:\n%s", env, res.Kubeconfig)
		}
	}
}

func TestResolveSourceAndTarget(t *testing.T) {
	s := setup(t)
	res, err := s.Resolve([]string{"development"}, []string{"production"})
	if err != nil {
		t.Fatal(err)
	}
	d := decode(t, res.Kubeconfig)
	if d.CurrentContext != "development" || len(d.Contexts) != 2 || len(d.Clusters) != 2 || len(d.Users) != 2 {
		t.Fatalf("resolved = %+v", d)
	}
	servers := map[string]string{}
	for _, c := range d.Clusters {
		servers[c.Name] = c.Cluster.Server
	}
	if servers["development"] != "https://dev.example:443" || servers["production"] != "https://prod.example:443" {
		t.Errorf("servers = %v", servers)
	}
	if res.Env[ContextVar] != "development" || res.Env[SourceContextVar] != "production" {
		t.Errorf("env = %v", res.Env)
	}
	if len(res.Clusters) != 2 || res.Clusters[0].Environment != "development" || res.Clusters[1].Environment != "production" {
		t.Errorf("clusters info = %+v", res.Clusters)
	}

	// Same environment on both sides: one context, both variables.
	res, err = s.Resolve([]string{"production"}, []string{"production"})
	if err != nil {
		t.Fatal(err)
	}
	if d := decode(t, res.Kubeconfig); len(d.Contexts) != 1 || res.Env[SourceContextVar] != "production" {
		t.Errorf("same env: %+v, %v", d.Contexts, res.Env)
	}
}

func TestResolveSharedCluster(t *testing.T) {
	s := setup(t)
	res, err := s.Resolve([]string{"devops"}, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	d := decode(t, res.Kubeconfig)
	ns := map[string]string{}
	for _, c := range d.Contexts {
		ns[c.Name] = c.Context.Namespace
	}
	if ns["devops"] != "devops" || ns["test"] != "test" || len(d.Clusters) != 2 || len(d.Users) != 2 {
		t.Errorf("contexts = %+v, clusters = %+v", d.Contexts, d.Clusters)
	}
	if res.Clusters[0].Namespace != "devops" || res.Clusters[1].Namespace != "test" {
		t.Errorf("clusters info = %+v", res.Clusters)
	}
}

func TestResolveMultiContextFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "production", `{
  "apiVersion": "v1", "kind": "Config", "current-context": "prod",
  "clusters": [
    {"name": "prod", "cluster": {"server": "https://prod.example"}},
    {"name": "dev", "cluster": {"server": "https://dev.example"}}
  ],
  "contexts": [
    {"name": "dev", "context": {"cluster": "dev", "user": "dev"}},
    {"name": "prod", "context": {"cluster": "prod", "user": "prod", "namespace": "apps"}}
  ],
  "users": [
    {"name": "dev", "user": {"token": "dev-token"}},
    {"name": "prod", "user": {"token": "prod-token"}}
  ]
}`)
	res, err := Store{Dir: dir}.Resolve([]string{"production"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Kubeconfig)
	if strings.Contains(out, "dev") || !strings.Contains(out, "prod-token") || !strings.Contains(out, "https://prod.example") {
		t.Errorf("only the current context may be kept:\n%s", out)
	}
	if d := decode(t, res.Kubeconfig); d.Contexts[0].Context.Namespace != "apps" {
		t.Errorf("namespace lost: %+v", d.Contexts)
	}
}

func TestResolveErrors(t *testing.T) {
	s := setup(t)
	write(t, s.Dir, "big", "# "+strings.Repeat("x", maxSize)+"\n"+aks("c", "https://c", ""))
	write(t, s.Dir, "garbage", "clusters: [unclosed\n")
	write(t, s.Dir, "nocurrent", strings.Replace(aks("c", "https://c", ""), "current-context: c", "", 1))
	write(t, s.Dir, "wrongcurrent", strings.Replace(aks("c", "https://c", ""), "current-context: c", "current-context: other", 1))
	write(t, s.Dir, "noserver", strings.Replace(aks("c", "https://c", ""), "server: https://c", "", 1))
	write(t, s.Dir, "nouser", strings.Replace(aks("c", "https://c", ""), "- name: clusterUser_rg_c", "- name: someone", 1))
	write(t, s.Dir, "relative", strings.Replace(aks("c", "https://c", ""), "certificate-authority-data: LS0tLS1CRUdJTg==", "certificate-authority: ca.crt", 1))
	write(t, s.Dir, "relexec", strings.Replace(aks("c", "https://c", ""), "command: kubelogin", "command: ./bin/kubelogin", 1))
	tests := []struct {
		name             string
		targets, sources []string
		missing          bool
		want             error
		wantMsg          string
	}{
		{name: "missing environment", targets: []string{"acc2"}, missing: true},
		{name: "missing source", targets: []string{"development"}, sources: []string{"acc2"}, missing: true},
		{name: "several targets", targets: []string{"development", "production"}, want: ErrSeveralTargets},
		{name: "several sources", targets: []string{"development"}, sources: []string{"production", "acceptance"}, want: ErrSeveralTargets},
		{name: "no environment", targets: []string{"-"}, want: ErrNoEnvironment},
		{name: "invalid name", targets: []string{"../production"}, wantMsg: "invalid environment name"},
		{name: "oversize", targets: []string{"big"}, wantMsg: "larger than"},
		{name: "invalid", targets: []string{"garbage"}, wantMsg: "not a valid kubeconfig"},
		{name: "no current-context", targets: []string{"nocurrent"}, wantMsg: "current-context is not set"},
		{name: "unknown current-context", targets: []string{"wrongcurrent"}, wantMsg: `context "other" does not exist`},
		{name: "no server", targets: []string{"noserver"}, wantMsg: "has no server"},
		{name: "unknown user", targets: []string{"nouser"}, wantMsg: `user "clusterUser_rg_c" does not exist`},
		{name: "relative path", targets: []string{"relative"}, wantMsg: "must be an absolute path"},
		{name: "relative exec", targets: []string{"relexec"}, wantMsg: "exec command must be absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Resolve(tt.targets, tt.sources)
			var me *MissingError
			switch {
			case err == nil:
				t.Fatal("no error")
			case tt.missing && !errors.As(err, &me):
				t.Errorf("err = %v, want MissingError", err)
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Errorf("err = %v, want %v", err, tt.want)
			case tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg):
				t.Errorf("err = %v, want %q", err, tt.wantMsg)
			}
			if strings.Contains(err.Error(), "LS0tLS1CRUdJTg") || strings.Contains(err.Error(), "unclosed") {
				t.Errorf("error leaks file content: %v", err)
			}
		})
	}
	if _, err := (Store{}).Resolve([]string{"production"}, nil); !errors.As(err, new(*MissingError)) {
		t.Errorf("no dir: %v", err)
	}
}

// TestResolveRotation checks that a replaced file applies at the next start (no cache).
func TestResolveRotation(t *testing.T) {
	s := setup(t)
	write(t, s.Dir, "development", aks("aks-dev", "https://dev2.example:443", ""))
	res, err := s.Resolve([]string{"development"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Clusters[0].Server != "https://dev2.example:443" {
		t.Errorf("server = %q", res.Clusters[0].Server)
	}
}

func TestInventory(t *testing.T) {
	s := setup(t)
	write(t, s.Dir, "broken", "current-context: x\n")
	if err := os.MkdirAll(filepath.Join(s.Dir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	inv, err := s.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Status{}
	for _, st := range inv {
		got[st.Environment] = st
	}
	if len(got) != 7 {
		t.Errorf("inventory = %+v", inv)
	}
	if st := got["acceptance"]; st.Error != "" || st.Server != "https://acc.example:443" || st.Context != "aks-acc" {
		t.Errorf("acceptance = %+v", st)
	}
	if st := got["test"]; st.Namespace != "test" {
		t.Errorf("test = %+v", st)
	}
	if got["broken"].Error == "" || got["empty"].Error == "" {
		t.Errorf("broken = %+v, empty = %+v", got["broken"], got["empty"])
	}
	if !s.Has("production") || s.Has("empty") || s.Has("../x") || (Store{}).Has("production") {
		t.Error("Has")
	}
	if inv, err := (Store{Dir: filepath.Join(s.Dir, "nope")}).Inventory(); err != nil || inv != nil {
		t.Errorf("missing dir: %v %v", inv, err)
	}
}
