package config

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// TestGoldenInvocations is the end-to-end check for the example runners: client values in,
// exact argv, env and stdin out.
func TestGoldenInvocations(t *testing.T) {
	cat := loadExamples(t)
	root := repoRoot(t)

	tests := []struct {
		script     string
		values     map[string]any
		wantArgs   []string
		wantMasked []string // nil means same as wantArgs
		wantEnv    map[string]string
		wantStdin  []params.StdinInput
	}{
		{
			script: "Backup Database",
			values: map[string]any{
				"Database":         "orders",
				"Tables":           []any{"customers", "orders"},
				"Exclude patterns": []any{"*.tmp", "*.log"},
				"Label":            "nightly-1",
				"Encryption key":   "s3cr3t",
				"Storage token":    "tok",
				"Manifest":         "/uploads/manifest.txt",
				"Tool version":     "9.9.9", // constant: ignored
			},
			wantArgs: []string{
				"--database", "orders", "--level=6", "--verify",
				"--table", "customers", "--table", "orders",
				"--exclude", "*.tmp", "*.log", "--label", "nightly-1",
				"--host", "127.0.0.1", "--key", "s3cr3t",
				"--manifest", "/uploads/manifest.txt", "--tool-version", "2.4.1",
			},
			wantMasked: []string{
				"--database", "orders", "--level=6", "--verify",
				"--table", "customers", "--table", "orders",
				"--exclude", "*.tmp", "*.log", "--label", "nightly-1",
				"--host", "127.0.0.1", "--key", params.Mask,
				"--manifest", "/uploads/manifest.txt", "--tool-version", "2.4.1",
			},
			wantEnv: map[string]string{
				"PARAM_DATABASE":            "orders",
				"PARAM_COMPRESSION_LEVEL":   "6",
				"PARAM_VERIFY_AFTER_BACKUP": "true",
				"PARAM_LABEL":               "nightly-1",
				"PARAM_TARGET_HOST":         "127.0.0.1",
				"PARAM_ENCRYPTION_KEY":      "s3cr3t",
				"STORAGE_TOKEN":             "tok",
				"PARAM_MANIFEST":            "/uploads/manifest.txt",
				"PARAM_TOOL_VERSION":        "2.4.1",
				"EXECUTION_ID":              "42",
			},
		},
		{
			script: "🗂️ Rotate Logs",
			values: map[string]any{
				"Service":     "api",
				"Environment": "Production", // UI label
				"Instances":   []any{"web-1", "web-2"},
				"Log files":   []any{"app.log", "error.log"},
				"Archive":     []any{"2026", "10"},
			},
			wantArgs: []string{
				"api", "production", "web-1 web-2", "app.log", "error.log",
				filepath.Join(root, "examples/archives/2026/10"),
			},
			wantEnv: map[string]string{
				"PARAM_SERVICE":     "api",
				"PARAM_ENVIRONMENT": "production",
				"PARAM_ARCHIVE":     filepath.Join(root, "examples/archives/2026/10"),
				"EXECUTION_ID":      "42",
			},
			wantStdin: []params.StdinInput{{Value: "14"}},
		},
		{
			script:  "Disk Usage Report",
			values:  map[string]any{},
			wantEnv: map[string]string{"EXECUTION_ID": "42"},
		},
		{
			script:   "Restart Service",
			values:   map[string]any{"Service": "api", "Dry run": "yes"},
			wantArgs: []string{"--service", "api", "--dry-run", "yes", "--timeout", "30"},
			wantEnv: map[string]string{
				"PARAM_SERVICE":         "api",
				"PARAM_DRY_RUN":         "yes",
				"PARAM_TIMEOUT_SECONDS": "30",
				"EXECUTION_ID":          "42",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.script, func(t *testing.T) {
			s := mustScript(t, cat, tt.script)
			values, err := s.Values(tt.values)
			if err != nil {
				t.Fatalf("Values: %v", err)
			}
			inv := params.Build(s.Parameters, values, "42")

			if !slices.Equal(inv.Args, tt.wantArgs) {
				t.Errorf("args:\n got  %q\n want %q", inv.Args, tt.wantArgs)
			}
			wantMasked := tt.wantMasked
			if wantMasked == nil {
				wantMasked = tt.wantArgs
			}
			if !slices.Equal(inv.MaskedArgs, wantMasked) {
				t.Errorf("masked args:\n got  %q\n want %q", inv.MaskedArgs, wantMasked)
			}
			if !maps.Equal(inv.Env, tt.wantEnv) {
				t.Errorf("env:\n got  %v\n want %v", inv.Env, tt.wantEnv)
			}
			if !slices.Equal(inv.Stdin, tt.wantStdin) {
				t.Errorf("stdin = %v, want %v", inv.Stdin, tt.wantStdin)
			}
		})
	}
}

func TestValuesErrors(t *testing.T) {
	s := mustScript(t, loadExamples(t), "Backup Database")
	_, err := s.Values(map[string]any{
		"Label":   "NOT VALID",
		"Unknown": "x",
		"Tables":  []any{"customers", "not-a-table"},
	})
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{`"Database": is required`, `"Encryption key": is required`,
		`"Label": must match`, `"Tables": is not one of the allowed values`, `unknown parameter "Unknown"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "NOT VALID") {
		t.Error("error leaks a submitted value")
	}
}
