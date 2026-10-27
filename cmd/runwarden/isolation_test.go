package main

import (
	"strings"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
)

func TestCheckIsolation(t *testing.T) {
	cfg := config.App{DataDir: "/var/lib/runwarden", SecretsDir: "/runwarden/secrets", KubeDir: "/runwarden/kube", WorkspaceDir: "/var/lib/runwarden-work"} //nolint:gosec // paths, not credentials
	allowed := append(append([]string{"/opt/runwarden"}, sandboxRead...), sandboxWrite...)
	if err := checkIsolation(cfg, allowed); err != nil {
		t.Errorf("production layout refused: %v", err)
	}
	tests := []struct {
		name    string
		change  func(*config.App)
		allowed []string
		want    string
	}{
		{"secrets under /etc", func(c *config.App) { c.SecretsDir = "/etc/runwarden/secrets" }, nil, "SECRETS_DIR"},
		{"kubeconfigs under /etc", func(c *config.App) { c.KubeDir = "/etc/runwarden/kube" }, nil, "KUBE_DIR"},
		{"data under the base dir", func(c *config.App) { c.DataDir = "/opt/runwarden/data" }, nil, "DATA_DIR"},
		{"workspace root readable", func(*config.App) {}, []string{"/var/lib/runwarden-work"}, "WORKSPACE_DIR"},
		{"extra path inside the data dir", func(*config.App) {}, []string{"/var/lib/runwarden/tmp"}, "DATA_DIR"},
		{"extra path above everything", func(*config.App) {}, []string{"/var"}, "DATA_DIR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cfg
			tt.change(&c)
			err := checkIsolation(c, append(append([]string(nil), allowed...), tt.allowed...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error about %s", err, tt.want)
			}
		})
	}
	if err := checkIsolation(cfg, []string{"/var/lib/runwarden-other"}); err != nil {
		t.Errorf("sibling with a common prefix refused: %v", err)
	}
}

func TestDescribeIsolation(t *testing.T) {
	for _, tc := range []struct {
		st   sandbox.Status
		want string
	}{
		{sandbox.Status{Mode: "auto", Active: true, ABI: 7, SignalScoping: true}, "sandboxed (Landlock ABI 7, signals scoped)"},
		{sandbox.Status{Mode: "required", Active: true, ABI: 4}, "sandboxed (Landlock ABI 4, signals not scoped)"},
		{sandbox.Status{Mode: "auto", Reason: "landlock is not available"}, "not sandboxed (ISOLATION=auto): landlock is not available"},
	} {
		if got := describeIsolation(tc.st); got != tc.want {
			t.Errorf("describeIsolation(%+v) = %q, want %q", tc.st, got, tc.want)
		}
	}
}
