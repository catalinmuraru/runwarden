//go:build unix

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/kube"
)

// kubeDir lays out one kubeconfig per environment like mounted Kubernetes Secrets.
func kubeDir(t *testing.T, envs ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, env := range envs {
		short := env[:3]
		cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: aks
  cluster: {server: "https://%[1]s.example:6443"}
contexts:
- name: aks-admin
  context: {cluster: aks, user: sp, namespace: %[1]s-ns}
users:
- name: sp
  user: {token: %[1]s-token}
current-context: aks-admin
`, short)
		if err := os.MkdirAll(filepath.Join(dir, env), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, env, kube.FileName), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRunKubeconfig(t *testing.T) {
	var (
		events []audit.Record
		mu     sync.Mutex
	)
	audit.SetSink(func(r audit.Record) { mu.Lock(); events = append(events, r); mu.Unlock() })
	t.Cleanup(func() { audit.SetSink(nil) })

	dir := kubeDir(t, "development", "production")
	alice := setupEnv(t, executor.Limits{}, executor.EnvPolicy{TempRoot: t.TempDir(), PrivateHome: true},
		Options{Kube: kube.Store{Dir: dir}})

	// Concurrent runs in two environments each get their own cluster, as context "<environment>".
	dev := alice.start("Kube", map[string]any{"To": "dev", "Wait": "0.5"})
	prod := alice.start("Kube", map[string]any{"To": "prod"})
	for _, c := range []struct {
		id, env string
		want    []string
	}{
		{dev, "development", []string{"context=development source=unset", "mode=600", "current-context: development",
			"server: https://dev.example:6443", "namespace: dev-ns", "token: dev-token"}},
		{prod, "production", []string{"context=production source=unset", "current-context: production", "server: https://pro.example:6443"}},
	} {
		meta := alice.finish(c.id)
		out := alice.output(c.id)
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s run misses %q:\n%s", c.env, w, out)
			}
		}
		if strings.Count(out, "server:") != 1 {
			t.Errorf("%s run holds more than its cluster:\n%s", c.env, out)
		}
		if len(meta.Clusters) != 1 || meta.Clusters[0].Environment != c.env || !strings.HasPrefix(meta.Clusters[0].Server, "https://"+c.env[:3]) {
			t.Errorf("%s history clusters = %+v", c.env, meta.Clusters)
		}
	}

	// Two-environment run: both contexts, the target current.
	id := alice.start("Kube", map[string]any{"From": "prod", "To": "dev"})
	meta := alice.finish(id)
	out := alice.output(id)
	for _, w := range []string{"context=development source=production", "current-context: development", "server: https://dev.example:6443", "server: https://pro.example:6443"} {
		if !strings.Contains(out, w) {
			t.Errorf("copy run misses %q:\n%s", w, out)
		}
	}
	if len(meta.Clusters) != 2 || meta.Clusters[0].Environment != "development" || meta.Clusters[1].Environment != "production" ||
		meta.Clusters[1].Namespace != "pro-ns" {
		t.Errorf("history clusters = %+v", meta.Clusters)
	}

	// Helpers get the cluster of the environment their dependency selects, none before one is chosen.
	for path, want := range map[string]string{"Context/values?To=prod": `"ctx-production"`, "Any/values": `"none"`} {
		code, body := alice.do(http.MethodGet, "/api/scripts/Kube/parameters/"+path, nil)
		if code != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("helper %s = %d %s, want %s", path, code, body, want)
		}
	}

	// An environment without a kubeconfig refuses runs (audited) and helpers.
	if err := os.RemoveAll(filepath.Join(dir, "production")); err != nil {
		t.Fatal(err)
	}
	code, body := alice.do(http.MethodPost, "/api/scripts/Kube/runs", map[string]any{"values": map[string]any{"From": "prod", "To": "dev"}})
	if code != http.StatusConflict || !strings.Contains(string(body), "no Kubernetes cluster is configured for production") {
		t.Errorf("missing kubeconfig = %d %s", code, body)
	}
	if code, body := alice.do(http.MethodGet, "/api/scripts/Kube/parameters/Context/values?To=prod", nil); code != http.StatusConflict {
		t.Errorf("helper without kubeconfig = %d %s", code, body)
	}
	mu.Lock()
	var refused *audit.Record
	for i := range events {
		if e := &events[i]; e.Action == audit.RunStart && e.Script == "Kube" && e.Outcome == "failure" {
			refused = e
		}
	}
	mu.Unlock()
	if refused == nil || !strings.Contains(refused.Reason, "production") {
		t.Errorf("refusal audit = %+v", refused)
	}

	// Scripts without "kubernetes": true get no kubeconfig.
	uses := []authz.EnvUse{{Environment: "development", Use: config.EnvTarget}}
	if kc, clusters, err := alice.srv.resolveKube(alice.script("Echo"), uses); kc != nil || clusters != nil || err != nil {
		t.Errorf("Echo kubeconfig = %v %v %v", kc, clusters, err)
	}
	if m := alice.finish(alice.start("Echo", map[string]any{"Name": "abc"})); len(m.Clusters) != 0 {
		t.Errorf("Echo clusters = %+v", m.Clusters)
	}
}

func TestRunKubeconfigNeedsPrivateHome(t *testing.T) {
	alice := setup(t, executor.Limits{}, Options{Kube: kube.Store{Dir: kubeDir(t, "development")}})
	code, body := alice.do(http.MethodPost, "/api/scripts/Kube/runs", map[string]any{"values": map[string]any{"To": "dev"}})
	if code != http.StatusInternalServerError {
		t.Errorf("shared HOME = %d %s", code, body)
	}
}

func TestAdminKubernetes(t *testing.T) {
	dir := kubeDir(t, "development", "nowhere")
	if err := os.MkdirAll(filepath.Join(dir, "production"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "production", kube.FileName), []byte("token: prod-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	acts, err := activity.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	alice := setup(t, executor.Limits{}, Options{Activity: acts, Kube: kube.Store{Dir: dir}})
	root := alice.as("root", "")
	code, body := root.do(http.MethodGet, "/api/admin/secrets", nil)
	if code != http.StatusOK || strings.Contains(string(body), "token") {
		t.Fatalf("admin secrets = %d %s", code, body)
	}
	var page struct {
		Kubernetes kubeOverview `json:"kubernetes"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	k := page.Kubernetes
	if !k.Configured || len(k.Clusters) != 3 || strings.Join(k.UnknownDirs, ",") != "nowhere" {
		t.Errorf("kubernetes = %+v", k)
	}
	byEnv := map[string]kubeCluster{}
	for _, c := range k.Clusters {
		byEnv[c.Environment] = c
	}
	if c := byEnv["development"]; c.Server != "https://dev.example:6443" || c.Namespace != "dev-ns" || c.Context != "aks-admin" || c.Error != "" {
		t.Errorf("development = %+v", c)
	}
	if c := byEnv["production"]; c.Error == "" || c.Server != "" {
		t.Errorf("production = %+v", c)
	}
	// Kube needs a cluster everywhere; only development has a usable one.
	if len(k.Scripts) != 1 || k.Scripts[0].Name != "Kube" || slices.Contains(k.Scripts[0].Missing, "development") ||
		!slices.Contains(k.Scripts[0].Missing, "production") {
		t.Errorf("scripts = %+v", k.Scripts)
	}
}
