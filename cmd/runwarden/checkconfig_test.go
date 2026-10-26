package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckConfigExamples(t *testing.T) {
	t.Chdir("../..") // example paths are relative to the repo root
	var out bytes.Buffer
	if code := checkConfig([]string{"examples/runners"}, &out); code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "5 scripts, 1 warnings, 0 errors") || !strings.Contains(out.String(), "secrets DB_PASSWORD, API_TOKEN") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}
}

func TestCheckConfigUsage(t *testing.T) {
	var out bytes.Buffer
	if code := checkConfig(nil, &out); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if code := checkConfig([]string{"/does/not/exist"}, &out); code != 1 {
		t.Errorf("exit code %d for a missing dir, want 1", code)
	}
}

func TestCheckConfigKubernetes(t *testing.T) {
	runners, kubeDir := t.TempDir(), t.TempDir()
	cfg := `{"name": "Deploy", "script_path": "run.sh", "kubernetes": true,
  "environment_parameters": {"Env": "target"},
  "parameters": [{"name": "Env", "type": "list", "values": ["development", "production"]}]}`
	if err := os.WriteFile(filepath.Join(runners, "deploy.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(kubeDir, "development"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kubeDir, "development", "config"), []byte("current-context: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	var out bytes.Buffer
	if code := checkConfigWith([]string{runners}, &out, func(k string) string { return env[k] }); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "; kubernetes;") || !strings.Contains(out.String(), "0 warnings") {
		t.Errorf("without KUBE_DIR:\n%s", out.String())
	}
	env["KUBE_DIR"] = kubeDir
	out.Reset()
	if code := checkConfigWith([]string{runners}, &out, func(k string) string { return env[k] }); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), `environment "production" (parameter "Env") has no kubeconfig`) ||
		strings.Contains(out.String(), `"development"`) || !strings.Contains(out.String(), "1 warnings") {
		t.Errorf("with KUBE_DIR:\n%s", out.String())
	}
	env["RUN_HOME"] = "shared"
	out.Reset()
	checkConfigWith([]string{runners}, &out, func(k string) string { return env[k] })
	if !strings.Contains(out.String(), "need RUN_HOME=per-run") || !strings.Contains(out.String(), "2 warnings") {
		t.Errorf("with RUN_HOME=shared:\n%s", out.String())
	}
}
